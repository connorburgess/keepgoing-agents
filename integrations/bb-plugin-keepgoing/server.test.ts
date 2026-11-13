import { hostContract } from "./host-contract.ts";
import { test } from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { mkdir, mkdtemp, readFile, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createFakePluginHost, makeThreadResponse, makeHostResponse, experimental_scanPublicSdkOnly } from "@get-bb/plugin-sdk/testing";
import plugin from "./server.ts";
import { setupResult } from "./setup-contract.ts";

test("plugin uses public SDK surfaces only", () => {
  const scan = experimental_scanPublicSdkOnly(fileURLToPath(new URL(".", import.meta.url)), { allow: [/^react(?:-dom)?(?:\/.*)?$/, /^@testing-library\/react$/, /^jsdom$/] });
  assert.deepEqual(scan.violations, []); assert.deepEqual(scan.privateDependencies, []);
});

test("SDK RPC resolves host metadata, persists references, and routes controls without spawning threads", async () => {
  const thread = makeThreadResponse({ id: "thr_example", environmentId: "env_example", projectId: "proj_example", providerId: "codex" });
  const host = makeHostResponse({ id: "host_example", status: "connected" });
  const environment = { id: "env_example", projectId: "proj_example", hostId: host.id, path: "/projects/example", isGitRepo: true, status: "ready", hostLifecycle: "active", lifecycle: { phase: "active" } };
  const operations: string[] = [];
  const agent = { adapter: "bb", provider: "codex", bbThread: thread.id };
  const goal = { id: "goal_0123456789abcdef0123456789abcdef", revision: 1, state: "recorded" };
  const state = { goal: { ...goal, title: "PRIVATE TITLE" }, loop: "example-loop", target: "primary", controlState: "disabled", control: {}, window: "open", state: {}, primaryAgent: agent, targetAgent: agent, daemon: { state: "not_recorded" } };
  const { bb, harness } = createFakePluginHost({
    pluginId: "keepgoing", settings: { keepgoingPath: "keepgoing", configs: "" },
    sdk: { threads: { get: async () => thread }, environments: { get: async () => environment }, hosts: { get: async () => host } },
    experimental_callHostRpc: async ({ method, input, hostId }) => {
      assert.equal(hostId, host.id);
      if (method === "init") {
        const p = hostContract.init.input.parse(input); operations.push(p.dryRun ? "preview" : "create");
        assert.equal(p.withGoal, true);
        assert.equal(p.target.project, environment.path); assert.equal(p.target.provider, "codex");
        return { plan: { withGoal: true, loop: p.name, project: p.target.project, configPath: "/private/example-loop/keepgoing.yaml", stateDir: "/private/example-loop/state", threadId: p.target.threadId, provider: p.target.provider, limits: { interval: "5m", wakeEvery: "1h", stallAfter: "30m", backoff: "10m", maxNudges: 3, maxReprompts: 3 } } };
      }
      const p = hostContract.command.input.parse(input); operations.push(p.operation);
      return { code: 0, data: p.operation === "doctor" ? { schemaVersion: 1, targetAgent: agent, checks: [{ id: "agent.primary.target", status: "ok", message: "Fixture verified" }] } : p.operation === "status" ? state : null };
    },
  });
  let activeHarness = harness;
  try {
    await plugin(bb);
    const preview = setupResult.parse(await harness.behavior.callRpc("setup", { action: "preview", threadId: thread.id, name: "example-loop", task: "Fictional task", withGoal: true }));
    assert.equal(preview.ok, true);
    const created = setupResult.parse(await harness.behavior.callRpc("setup", { action: "create", threadId: thread.id, ticket: preview.ticket, task: "Fictional task", withGoal: true }));
    assert.deepEqual(created.goal, goal); assert.ok(!JSON.stringify(created).includes("PRIVATE TITLE"));
    assert.ok(created.record); assert.equal(created.canStart, true); assert.ok(!operations.includes("start"));
    const views = await harness.behavior.callRpc("forThread", { threadId: thread.id }) as { loops: { managedId: string }[] };
    assert.equal(views.loops[0].managedId, created.record.id);
    assert.ok(JSON.stringify(views).includes(goal.id)); assert.ok(!JSON.stringify(views).includes("PRIVATE TITLE"));
    const wrong = await harness.behavior.callRpc("control", { configPath: "/private/other.yaml", managedId: created.record.id, action: "resume" }) as { ok: boolean };
    assert.equal(wrong.ok, false); assert.ok(!operations.includes("resume"));
    await harness.behavior.callRpc("control", { configPath: created.record.configPath, managedId: created.record.id, action: "pause" });
    assert.ok(operations.includes("pause"));
    await harness.behavior.callRpc("setup", { action: "start", threadId: thread.id });
    assert.equal(operations.filter(o => o === "start").length, 1);
    assert.deepEqual(harness.inspection.sdk.callsTo("threads.spawn"), []);
    assert.deepEqual(harness.inspection.sdk.callsTo("threads.send"), []);
    assert.deepEqual(harness.inspection.sdk.callsTo("threads.fork"), []);
    assert.equal(harness.inspection.logEntries.length, 0);
    const saved = await bb.storage.kv.get("setups.v1");
    assert.ok(!JSON.stringify(saved).includes("Fictional task"));
    activeHarness = (await harness.lifecycle.reload(plugin)).harness;
    const inspected = setupResult.parse(await activeHarness.behavior.callRpc("setup", { action: "inspect", threadId: thread.id }));
    assert.equal(inspected.record?.id, created.record.id, JSON.stringify(inspected));
    thread.environmentId = "env_other";
    const before = operations.length;
    const blocked = setupResult.parse(await activeHarness.behavior.callRpc("setup", { action: "start", threadId: thread.id }));
    assert.equal(blocked.canStart, false); assert.equal(operations.length, before);
  } finally { await activeHarness.lifecycle.dispose(); }
});

// status exits 1 when control.json is damaged; the loop must stay listed so
// stop and pause can still reach it. They keep the hold in control-hold.json.
test("bb keepgoing stop and pause reach a loop whose control record is damaged", { skip: !process.env.KEEPGOING_TEST_BINARY }, async () => {
  const binary = process.env.KEEPGOING_TEST_BINARY!;
  const root = await realpath(await mkdtemp(join(tmpdir(), "keepgoing-damaged-control-")));
  try {
    const config = join(root, "keepgoing.yaml"); const stateDir = join(root, "state");
    await mkdir(stateDir, { mode: 0o700 });
    await writeFile(config, "version: 1\nloop:\n  name: example-loop\n  state_dir: state\n  adapter: bb\n  provider: example\n  bb: {thread: thr_example, cli: nonexistent-provider-fixture}\n  prompt_file: missing-task.txt\n", { mode: 0o600 });
    await writeFile(join(stateDir, "control.json"), "{", { mode: 0o600 });
    const { bb, harness } = createFakePluginHost({ pluginId: "keepgoing", settings: { keepgoingPath: binary, configs: config } });
    try {
      await plugin(bb);
      const listed = await harness.behavior.callRpc("forThread", { threadId: "thr_example" }) as { loops: { loop: string; controlState: string }[]; errors: string[] };
      assert.deepEqual(listed.loops.map(l => l.loop), ["example-loop"], JSON.stringify(listed));
      for (const [action, field] of [["pause", "paused"], ["stop", "stop"]]) {
        const r = await harness.behavior.runCli([action, "example-loop"]);
        assert.equal(r.exitCode, 0, `${action}: ${r.stdout}${r.stderr}`);
        const hold = JSON.parse(await readFile(join(stateDir, "control-hold.json"), "utf8"));
        assert.equal(hold[field], true, JSON.stringify(hold));
      }
      assert.equal(await readFile(join(stateDir, "control.json"), "utf8"), "{");
      const held = await harness.behavior.callRpc("forThread", { threadId: "thr_example" }) as { loops: { controlState: string }[] };
      assert.match(held.loops[0]?.controlState ?? "", /stopped/);
      // Resume is refused until the record is restored; the hold stays.
      const resumed = await harness.behavior.runCli(["resume", "example-loop"]);
      assert.notEqual(resumed.exitCode, 0);
      assert.equal(JSON.parse(await readFile(join(stateDir, "control-hold.json"), "utf8")).stop, true);
    } finally { await harness.lifecycle.dispose(); }
  } finally { await rm(root, { recursive: true, force: true }); }
});
