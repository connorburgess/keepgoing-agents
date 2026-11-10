import { test } from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { createFakePluginHost, makeThreadResponse, makeHostResponse, experimental_scanPublicSdkOnly } from "@get-bb/plugin-sdk/testing";
import plugin from "./server.ts";
import { hostContract, setupResult } from "./setup-contract.ts";

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
  const state = { loop: "example-loop", target: "primary", controlState: "disabled", control: {}, window: "open", state: {}, primaryAgent: agent, targetAgent: agent, daemon: { state: "not_recorded" } };
  const { bb, harness } = createFakePluginHost({
    pluginId: "keepgoing", settings: { keepgoingPath: "keepgoing", configs: "" },
    sdk: { threads: { get: async () => thread }, environments: { get: async () => environment }, hosts: { get: async () => host } },
    experimental_callHostRpc: async ({ method, input, hostId }) => {
      assert.equal(hostId, host.id);
      if (method === "init") {
        const p = hostContract.init.input.parse(input); operations.push(p.dryRun ? "preview" : "create");
        assert.equal(p.target.project, environment.path); assert.equal(p.target.provider, "codex");
        return { plan: { loop: p.name, project: p.target.project, configPath: "/private/example-loop/keepgoing.yaml", stateDir: "/private/example-loop/state", threadId: p.target.threadId, provider: p.target.provider, limits: { interval: "5m", wakeEvery: "1h", stallAfter: "30m", backoff: "10m", maxNudges: 3, maxReprompts: 3 } } };
      }
      const p = hostContract.command.input.parse(input); operations.push(p.operation);
      return { code: 0, data: p.operation === "doctor" ? { schemaVersion: 1, targetAgent: agent, checks: [{ id: "agent.primary.target", status: "ok", message: "Fixture verified" }] } : p.operation === "status" ? state : null };
    },
  });
  let activeHarness = harness;
  try {
    await plugin(bb);
    const preview = setupResult.parse(await harness.behavior.callRpc("setup", { action: "preview", threadId: thread.id, name: "example-loop", task: "Fictional task" }));
    assert.equal(preview.ok, true);
    const created = setupResult.parse(await harness.behavior.callRpc("setup", { action: "create", threadId: thread.id, ticket: preview.ticket, task: "Fictional task" }));
    assert.ok(created.record); assert.equal(created.canStart, true); assert.ok(!operations.includes("start"));
    const views = await harness.behavior.callRpc("forThread", { threadId: thread.id }) as { loops: { managedId: string }[] };
    assert.equal(views.loops[0].managedId, created.record.id);
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
