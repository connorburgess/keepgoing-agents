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
async function world(options: { mode?: "manual" | "automatic"; program?: "present" | "missing" } = {}) {
  const dir = await mkdtemp(join(tmpdir(), "keepgoing-new-thread-"));
  const program = join(dir, "keepgoing-stand-in");
  await writeFile(program, "#!/bin/sh\n[ \"$1\" = version ] && exit 0\nexit 9\n", { mode: 0o700 });
  // BB creates the thread after the control asks for its request. The margin
  // keeps that order true however slowly this test runs.
  const created = Date.now() + 60_000;
  const thread = makeThreadResponse({ id: "thr_new", environmentId: "env_example", projectId: "proj_example", providerId: "example-provider", title: "Release notes for 2.4", createdAt: created });
  const host = makeHostResponse({ id: "host_example", status: "connected" });
  const environment = { id: "env_example", projectId: "proj_example", hostId: host.id, path: "/projects/example", isGitRepo: true, status: "provisioning", hostLifecycle: "active", lifecycle: { phase: "active" } };
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
  await plugin(bb);
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
  const done = async () => { await harness.lifecycle.dispose(); await rm(dir, { recursive: true, force: true }); };
  return { bb, harness, thread, environment, hostCalls, rpc, hook, settle, done, state, set failStart(v: boolean) { flags.failStart = v; } };
}

test("review-first mode: a marked first message creates one bounded, switched-off setup for its thread once the environment is ready", async () => {
  const w = await world({ mode: "manual" });
  try {
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS });
    assert.equal(armed.ok, true); assert.equal(armed.mode, "manual");
    const decision = await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    assert.deepEqual(decision, { action: "proceed" }, "the send is never delayed or refused");
    await w.settle();
    assert.equal(w.hostCalls.length, 0, "nothing runs while the environment is still being prepared");
    assert.equal((await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome?.state, "waiting");

    w.environment.status = "ready";
    await w.harness.behavior.emitThreadEvent("thread.active", { thread: w.thread });
    await w.settle();
    const inits = w.hostCalls.filter(c => c.method === "init").map(c => hostContract.init.input.parse(c.input));
    assert.deepEqual(inits.map(i => i.dryRun), [true, true, false]);
    for (const i of inits) { assert.equal(i.name, "release-notes-for-2-4"); assert.equal(i.task, TASK); assert.equal(i.withGoal, true); assert.deepEqual(i.bounds, DEFAULT_BOUNDS); assert.equal(i.target.threadId, "thr_new"); }
    const operations = w.hostCalls.filter(c => c.method === "command").map(c => hostContract.command.input.parse(c.input).operation);
    assert.ok(!operations.includes("start"), "manual mode starts nothing");

    const outcome = (await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome!;
    assert.equal(outcome.state, "ready"); assert.equal(outcome.loop, "release-notes-for-2-4"); assert.deepEqual(outcome.bounds, DEFAULT_BOUNDS);
    // The plugin learned of the thread; it created, messaged and changed none.
    for (const call of ["threads.spawn", "threads.send", "threads.fork", "threads.update", "threads.create", "threads.archive"]) assert.deepEqual(w.harness.inspection.sdk.callsTo(call), [], call);
    // The task is in no stored value, log line or reply.
    for (const key of ["setups.v1", "moves.v1"]) assert.ok(!JSON.stringify(await w.bb.storage.kv.get(key) ?? null).includes("Fictional"));
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
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: { maxSends: 12, noProgressTimeout: "6h" } });
    assert.equal(armed.mode, "automatic");
    await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    await w.settle();
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
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS });
    await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    await w.settle();
    await w.harness.behavior.emitThreadEvent("thread.active", { thread: w.thread });
    await w.harness.behavior.emitThreadEvent("thread.idle", { thread: w.thread, lastAssistantText: null });
    const outcome = (await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome!;
    await w.settle();
    assert.equal(outcome.state, "ready"); assert.equal(outcome.startedAutomatically, false);
    assert.match(outcome.message, /could not be confirmed and was not tried again/);
    assert.equal(w.hostCalls.filter(c => c.method === "command" && hostContract.command.input.parse(c.input).operation === "start").length, 1);
  } finally { await w.done(); }
});

test("messages without this plugin's marker pass through untouched: plain Enter, the CLI, another plugin, a forged marker", async () => {
  const w = await world();
  try {
    w.environment.status = "ready";
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS });
    const passes = [
      {}, // a plain Enter, or a thread started from the CLI
      { experimental_submission: null },
      { experimental_submission: { pluginId: "another-plugin", data: marker(armed.intent!) } },
      { experimental_submission: { pluginId: "keepgoing", data: { v: 1, intent: "never-issued" } } },
      { experimental_submission: { pluginId: "keepgoing", data: "not a marker" } },
    ];
    for (const over of passes) assert.deepEqual(await w.hook(over), { action: "proceed" });
    await w.harness.behavior.emitThreadEvent("thread.active", { thread: w.thread });
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
    for (const input of [{ action: "check" }, { action: "arm", draftKey: "d", projectId: "proj_example", bounds: DEFAULT_BOUNDS }]) {
      const r = await missing.rpc(input);
      assert.equal(r.ok, false); assert.match(r.message, /was not found or did not run/); assert.equal(r.intent, undefined);
    }
  } finally { await missing.done(); }
  const w = await world();
  try {
    const armed = await w.rpc({ action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS });
    await w.hook({ experimental_submission: { pluginId: "keepgoing", data: marker(armed.intent!) } });
    await w.harness.behavior.emitThreadEvent("thread.deleted", { thread: w.thread });
    w.environment.status = "ready";
    await w.harness.behavior.emitThreadEvent("thread.active", { thread: w.thread });
    await w.settle();
    assert.equal(w.hostCalls.length, 0);
    assert.equal((await w.rpc({ action: "outcome", threadId: "thr_new" })).outcome?.state, "none");
  } finally { await w.done(); }
});
