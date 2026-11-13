import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createFakePluginHost, makeHostResponse, makeMessageDispatchHookContext, makeThreadResponse } from "@get-bb/plugin-sdk/testing";
import plugin from "./server.ts";
import { hostContract } from "./host-contract.ts";
import { newThreadResult, marker, DEFAULT_BOUNDS } from "./new-thread-contract.ts";

const TASK = "Fictional private task. Done when the example passes.";

// A stand-in program that only answers `version`; running anything else is a failure.
async function world(options: { mode?: "manual" | "automatic"; storedMode?: unknown; program?: "present" | "missing"; hook?: "absent" | "refused"; setups?: number } = {}) {
  const dir = await mkdtemp(join(tmpdir(), "keepgoing-new-thread-"));
  const program = join(dir, "keepgoing-stand-in");
  await writeFile(program, "#!/bin/sh\n[ \"$1\" = version ] && exit 0\nexit 9\n", { mode: 0o700 });
  // BB creates the thread after the control asks for its request. The margin
  // keeps that order true however slowly this test runs.
  const created = Date.now() + 60_000;
  // BB titles a thread from its first message, so the title is task text too.
  const thread = makeThreadResponse({ id: "thr_new", environmentId: "env_example", projectId: "proj_example", providerId: "example-provider", title: "Fictional private task", status: "pending", createdAt: created });
  const host = makeHostResponse({ id: "host_example", status: "connected" });
  const environment = { id: "env_example", projectId: "proj_example", hostId: host.id, path: "/projects/example-app", isGitRepo: true, status: "provisioning", hostLifecycle: "active", lifecycle: { phase: "active" } };
  const agent = { adapter: "bb", provider: "example-provider", bbThread: thread.id };
  const state = { loop: "release-notes-for-2-4", target: "primary", controlState: "disabled", control: {}, window: "open", state: {}, primaryAgent: agent, targetAgent: agent, daemon: { state: "not_recorded" } };
  const hostCalls: { method: string; input: unknown }[] = [];
  const flags = { failStart: false };
  const { bb, harness } = createFakePluginHost({
    pluginId: "keepgoing", settings: { keepgoingPath: options.program === "missing" ? join(dir, "absent") : program, configs: "", ...(options.mode ? { newThreadStart: options.mode } : {}) },
    sdk: { threads: { get: async () => thread }, environments: { get: async () => environment }, hosts: { get: async () => host } },
    experimental_callHostRpc: async ({ method, input, hostId }) => {
      assert.equal(hostId, host.id); hostCalls.push({ method, input });
      if (method === "init") {
        const p = hostContract.init.input.parse(input);
        return { plan: { withGoal: true, loop: p.name, project: p.target.project, configPath: "/private/release-notes/keepgoing.yaml", stateDir: "/private/release-notes/state", threadId: p.target.threadId, provider: p.target.provider,
          limits: { interval: "5m0s", wakeEvery: "1h0m0s", stallAfter: "30m0s", backoff: "10m0s", maxNudges: 3, maxReprompts: 3, maxSends: p.bounds?.maxSends, noProgressTimeout: p.bounds ? "24h0m0s" : undefined } } };
      }
      const p = hostContract.command.input.parse(input);
      if (p.operation === "start" && flags.failStart) return { code: 1, data: null };
      if (p.operation === "start") state.controlState = "enabled";
      return { code: 0, data: p.operation === "doctor" ? { schemaVersion: 1, targetAgent: agent, checks: [{ id: "agent.primary.target", status: "ok", message: "Fixture verified" }] } : p.operation === "status" ? state : null };
    },
  });
  if (options.setups) await bb.storage.kv.set("setups.v1", Array.from({ length: options.setups }, (_, i) => ({ id: `record-${i}`, loop: `example-${i}`, configPath: `/private/example-${i}/keepgoing.yaml`, threadId: `thr_other_${i}`, environmentId: "env_other", projectId: "proj_other", hostId: "host_other", project: "/projects/other", provider: "example-provider" })));
  // A BB without the experimental dispatch hook, or one that refuses it.
  // A stored value the setting's own list does not contain, as an older or
  // hand-edited configuration could hold.
  const settings = { define: (descriptors: Parameters<typeof bb.settings.define>[0]) => {
    const handle = bb.settings.define(descriptors);
    return new Proxy(handle, { get: (target, key, receiver) => key === "get" ? async () => ({ ...(await target.get()), newThreadStart: options.storedMode }) : Reflect.get(target, key, receiver) });
  } };
  const api = options.hook || "storedMode" in options
    ? new Proxy(bb, { get: (target, key, receiver) => {
      if (key === "experimental_hooks" && options.hook) return options.hook === "absent" ? undefined : { on() { throw new Error("hook removed"); } };
      if (key === "settings" && "storedMode" in options) return settings;
      return Reflect.get(target, key, receiver);
    } })
    : bb;
  await plugin(api);
  const rpc = async (input: unknown) => newThreadResult.parse(await harness.behavior.callRpc("newThread", input));
  const hook = (over: Parameters<typeof makeMessageDispatchHookContext>[0] = {}) => {
    const handler = harness.inspection.registrations.hooks["message.dispatch"]; assert.ok(handler, "the dispatch hook is registered");
    return handler(makeMessageDispatchHookContext({ thread, input: { text: TASK }, attempt: "start-turn", initiator: "user", senderThreadId: null, ...over }));
  };
  // Setup runs after the hook returns. Wait until the host has been quiet for a moment.
  const settle = async () => {
    for (let quiet = 0, seen = -1, i = 0; quiet < 4 && i < 200; i++) {
      await new Promise(resolve => setTimeout(resolve, 15));
      quiet = hostCalls.length === seen ? quiet + 1 : 0; seen = hostCalls.length;
    }
  };
  // BB starts the thread's first turn: the message was delivered.
  const turnStarts = async () => { thread.status = "active"; await harness.behavior.emitThreadEvent("thread.active", { thread }); await settle(); };
  const done = async () => { await harness.lifecycle.dispose(); await rm(dir, { recursive: true, force: true }); };
  return { bb, harness, thread, environment, hostCalls, rpc, hook, settle, done, state, turnStarts, set failStart(v: boolean) { flags.failStart = v; } };
}

test("review-first mode: a marked first message creates one bounded, switched-off setup for its thread once the environment is ready", async () => {
  const w = await world({ mode: "manual" });
  try {
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "manual" });
    assert.equal(armed.ok, true); assert.equal(armed.mode, "manual");
    const decision = await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    assert.deepEqual(decision, { action: "proceed" }, "the send is never delayed or refused");
    await w.settle();
    assert.equal(w.hostCalls.length, 0, "nothing runs before the first turn has started");
    assert.equal((await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome?.state, "waiting");
    // The environment is ready, but BB has not started the turn: still nothing.
    w.environment.status = "ready";
    assert.equal((await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome?.state, "waiting");
    await w.settle(); assert.equal(w.hostCalls.length, 0);

    await w.turnStarts();
    const inits = w.hostCalls.filter(c => c.method === "init").map(c => hostContract.init.input.parse(c.input));
    assert.deepEqual(inits.map(i => i.dryRun), [true, true, false]);
    for (const i of inits) {
      // Named after the project directory, not the message or the title made from it.
      assert.match(i.name, /^example-app-[a-z0-9]{4}$/); assert.ok(!/fictional|private|task/.test(i.name));
      assert.equal(i.task, TASK); assert.equal(i.withGoal, true); assert.deepEqual(i.bounds, DEFAULT_BOUNDS); assert.equal(i.target.threadId, "thr_new");
    }
    assert.equal(new Set(inits.map(i => i.name)).size, 1);
    const operations = w.hostCalls.filter(c => c.method === "command").map(c => hostContract.command.input.parse(c.input).operation);
    assert.ok(!operations.includes("start"), "manual mode starts nothing");

    const outcome = (await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome!;
    assert.equal(outcome.state, "ready"); assert.equal(outcome.loop, inits[0].name); assert.deepEqual(outcome.bounds, DEFAULT_BOUNDS);
    // The plugin learned of the thread; it created, messaged and changed none.
    for (const call of ["threads.spawn", "threads.send", "threads.fork", "threads.update", "threads.create", "threads.archive"]) assert.deepEqual(w.harness.inspection.sdk.callsTo(call), [], call);
    // The task is in no stored value, log line or reply.
    for (const key of ["setups.v1", "moves.v1"]) assert.ok(!/fictional/i.test(JSON.stringify(await w.bb.storage.kv.get(key) ?? null)), "no task text, and no title made from it, is stored");
    assert.equal(w.harness.inspection.logEntries.length, 0);
    assert.ok(!JSON.stringify([armed, outcome]).includes("Fictional"));
    // The person's own Start is the ordinary setup start.
    await w.harness.behavior.callRpc("setup", { action: "start", threadId: "thr_new" });
    assert.equal(w.hostCalls.filter(c => c.method === "command" && hostContract.command.input.parse(c.input).operation === "start").length, 1);
  } finally { await w.done(); }
});

test("by default the bounded loop is started after creation, once; the mode is read on the server", async () => {
  const w = await world();
  try {
    w.environment.status = "ready";
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: { maxSends: 12, noProgressTimeout: "6h" }, shownMode: "automatic" });
    assert.equal(armed.mode, "automatic");
    await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    await w.turnStarts();
    const inits = w.hostCalls.filter(c => c.method === "init").map(c => hostContract.init.input.parse(c.input));
    assert.ok(inits.length > 0); for (const i of inits) assert.deepEqual(i.bounds, { maxSends: 12, noProgressTimeout: "6h" });
    const operations = w.hostCalls.filter(c => c.method === "command").map(c => hostContract.command.input.parse(c.input).operation);
    assert.equal(operations.filter(o => o === "start").length, 1);
    assert.equal((await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome?.startedAutomatically, true);
    // Later lifecycle events and status reads never start it again.
    await w.harness.behavior.emitThreadEvent("thread.idle", { thread: w.thread, lastAssistantText: null });
    await w.rpc({ action: "outcome", threadId: "thr_new" }); await w.settle();
    assert.equal(w.hostCalls.filter(c => c.method === "command" && hostContract.command.input.parse(c.input).operation === "start").length, 1);
  } finally { await w.done(); }
});

test("an uncertain automatic start is not retried: the setup stays switched off with the reason", async () => {
  const w = await world();
  try {
    w.environment.status = "ready"; w.failStart = true;
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "automatic" });
    await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    await w.turnStarts();
    await w.harness.behavior.emitThreadEvent("thread.active", { thread: w.thread });
    await w.harness.behavior.emitThreadEvent("thread.idle", { thread: w.thread, lastAssistantText: null });
    const outcome = (await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome!;
    await w.settle();
    assert.equal(outcome.state, "ready"); assert.equal(outcome.startedAutomatically, false);
    assert.match(outcome.message, /could not be confirmed, and it was not tried again/);
    assert.equal(w.hostCalls.filter(c => c.method === "command" && hostContract.command.input.parse(c.input).operation === "start").length, 1);
  } finally { await w.done(); }
});

test("messages without this plugin's marker pass through untouched: plain Enter, the CLI, another plugin, a forged marker", async () => {
  const w = await world();
  try {
    w.environment.status = "ready";
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "automatic" });
    const passes = [
      {}, // a plain Enter, or a thread started from the CLI
      { experimental_submission: null },
      { experimental_submission: { pluginId: "another-plugin", data: marker(armed.intent!) } },
      { experimental_submission: { pluginId: "keepgoing", data: { v: 1, intent: "never-issued" } } },
      { experimental_submission: { pluginId: "keepgoing", data: "not a marker" } },
    ];
    for (const over of passes) assert.deepEqual(await w.hook(over), { action: "proceed" });
    await w.turnStarts();
    await w.harness.behavior.emitThreadEvent("thread.idle", { thread: w.thread, lastAssistantText: null });
    await w.settle();
    assert.equal(w.hostCalls.length, 0, "no setup for a thread that was not marked");
    // The unissued marker names this thread with certainty, so the thread is told; nothing is attached.
    assert.equal((await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome?.state, "failed");
  } finally { await w.done(); }
});

test("a missing program keeps the control off with an explanation, and a deleted thread forgets the request", async () => {
  const missing = await world({ program: "missing" });
  try {
    for (const input of [{ action: "check" }, { action: "arm", draftKey: "d", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "manual" }]) {
      const r = await missing.rpc(input);
      assert.equal(r.ok, false); assert.match(r.message, /was not found or did not run/); assert.equal(r.intent, undefined);
    }
  } finally { await missing.done(); }
  const w = await world();
  try {
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "automatic" });
    await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    await w.harness.behavior.emitThreadEvent("thread.deleted", { thread: w.thread });
    w.environment.status = "ready";
    await w.turnStarts();
    assert.equal(w.hostCalls.length, 0);
    assert.equal((await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome?.state, "none");
  } finally { await w.done(); }
});

test("a send BB rejected after the hook ran creates and starts nothing, even once the thread later runs a turn", async () => {
  const w = await world();
  try {
    w.environment.status = "ready";
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "automatic" });
    await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    // BB rejects the send; the control withdraws the request.
    assert.equal((await w.rpc({ action: "cancel", intent: armed.intent })).ok, true);
    await w.turnStarts();
    assert.equal(w.hostCalls.length, 0);
    const outcome = (await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome!;
    assert.equal(outcome.state, "failed"); assert.match(outcome.message, /was not sent/);
  } finally { await w.done(); }
  // The browser is gone, so nothing withdraws the request, and the person
  // later sends a different first message by hand: that ends it too.
  const later = await world();
  try {
    later.environment.status = "ready";
    const armed = await later.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "automatic" });
    await later.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    await later.hook({ input: { text: "A different fictional message" } });
    await later.turnStarts();
    assert.equal(later.hostCalls.length, 0);
    assert.equal((await later.rpc({ action: "outcome", threadId: "thr_new" })).outcome?.state, "failed");
  } finally { await later.done(); }
});

test("an unreadable start setting is treated as review-first", async () => {
  for (const storedMode of ["whenever", 42, null, undefined]) {
  const w = await world({ storedMode });
  try {
    w.environment.status = "ready";
    const check = await w.rpc({ action: "check" });
    assert.equal(check.mode, "manual"); assert.deepEqual(check.setups, { used: 0, max: 32 });
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "automatic" });
    assert.equal(armed.mode, "manual");
    await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    await w.turnStarts();
    const operations = w.hostCalls.filter(c => c.method === "command").map(c => hostContract.command.input.parse(c.input).operation);
    assert.ok(w.hostCalls.some(c => c.method === "init")); assert.ok(!operations.includes("start"), String(storedMode));
  } finally { await w.done(); }
  }
});

test("the setup limit is checked before the send is armed, with the count", async () => {
  const w = await world({ setups: 32 });
  try {
    for (const input of [{ action: "check" }, { action: "arm", draftKey: "d", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "automatic" }]) {
      const r = await w.rpc(input);
      assert.equal(r.ok, false); assert.match(r.message, /already holds 32 of 32 setups/); assert.equal(r.intent, undefined);
    }
  } finally { await w.done(); }
});

test("without BB's dispatch hook only the New thread control is lost: status and controls on existing loops still load", async () => {
  for (const hook of ["absent", "refused"] as const) {
    const w = await world({ hook });
    try {
      for (const input of [{ action: "check" }, { action: "arm", draftKey: "d", projectId: "proj_example", bounds: DEFAULT_BOUNDS, shownMode: "automatic" }]) {
        const r = await w.rpc(input);
        assert.equal(r.ok, false, hook); assert.match(r.message, /does not offer the hook Keep going needs/); assert.equal(r.intent, undefined);
      }
      // The rest of the plugin registered and answers.
      const loops = await w.harness.behavior.callRpc("forThread", { threadId: "thr_new" }) as { loops: unknown[]; errors: unknown[] };
      assert.deepEqual(loops, { loops: [], errors: [] });
      const status = await w.harness.behavior.runCli(["status"]);
      assert.equal(status.exitCode, 0);
      const control = await w.harness.behavior.callRpc("control", { configPath: "/private/unlisted/keepgoing.yaml", action: "pause" }) as { ok: boolean; message: string };
      assert.equal(control.ok, false); assert.match(control.message, /not in the plugin's configured list/);
      assert.equal((await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome?.state, "none");
    } finally { await w.done(); }
  }
});
