import { test } from "node:test";
import assert from "node:assert/strict";
import { createFakePluginHost, makeThreadResponse, makeHostResponse } from "@get-bb/plugin-sdk/testing";
import plugin from "./server.ts";
import { hostContract } from "./host-contract.ts";
import { setupResult } from "./setup-contract.ts";

// A setup that follows the standard route list, through server.ts and the
// SDK's fake host: offered, created, controlled, and inspected after it has
// handed off to one of its routes.
test("a following setup is offered, created, paused, resumed and inspected through the server", async () => {
  const thread = makeThreadResponse({ id: "thr_example", environmentId: "env_example", projectId: "proj_example", providerId: "codex" });
  const host = makeHostResponse({ id: "host_example", status: "connected" });
  const environment = { id: "env_example", projectId: "proj_example", hostId: host.id, path: "/projects/example", isGitRepo: true, status: "ready", hostLifecycle: "active", lifecycle: { phase: "active" } };
  const operations: string[] = [];
  const agent = { adapter: "bb", provider: "codex", bbThread: thread.id };
  const routes = { list: "standard" as const, handoffOn: "rate_limit" as const, apply: [{ id: "codex-large", model: "example-large", group: "codex-large" }] };
  // The loop has handed off to a standard route: this thread on another model.
  const state = { loop: "example-loop", target: "codex-large", controlState: "paused", control: {}, window: "open", state: {}, primaryAgent: agent, targetAgent: agent, daemon: { state: "not_recorded" }, routes: { source: "standard" }, redundancy: { healthy: [], eligible: [], distinct: 0, min: 1, span: "provider", meets: false } };
  let followSeen = 0;
  const { bb, harness } = createFakePluginHost({
    pluginId: "keepgoing", settings: { keepgoingPath: "keepgoing", configs: "" },
    sdk: { threads: { get: async () => thread }, environments: { get: async () => environment }, hosts: { get: async () => host } },
    experimental_callHostRpc: async ({ method, input }) => {
      if (method === "standardRoutes") { hostContract.standardRoutes.input.parse(input); operations.push("standardRoutes"); return { available: true }; }
      if (method === "init") {
        const p = hostContract.init.input.parse(input); operations.push(p.dryRun ? "preview" : "create");
        if (p.followStandard) followSeen++;
        return { plan: { loop: p.name, project: p.target.project, configPath: "/private/example-loop/keepgoing.yaml", stateDir: "/private/example-loop/state", threadId: p.target.threadId, provider: p.target.provider, limits: { interval: "5m", wakeEvery: "1h", stallAfter: "30m", backoff: "10m", maxNudges: 3, maxReprompts: 3 }, ...(p.followStandard ? { routes } : {}) } };
      }
      const p = hostContract.command.input.parse(input); operations.push(p.operation);
      return { code: 0, data: p.operation === "doctor" ? { schemaVersion: 1, targetAgent: agent, checks: [{ id: "agent.primary.target", status: "ok", message: "Fixture verified" }] } : p.operation === "status" ? state : null };
    },
  });
  let active = harness;
  try {
    await plugin(bb);
    const offered = setupResult.parse(await harness.behavior.callRpc("setup", { action: "inspect", threadId: thread.id }));
    assert.equal(offered.standardRoutes, true);
    const preview = setupResult.parse(await harness.behavior.callRpc("setup", { action: "preview", threadId: thread.id, name: "example-loop", task: "Fictional task", followStandard: true }));
    assert.equal(preview.ok, true, preview.message); assert.deepEqual(preview.plan?.routes, routes);
    const created = setupResult.parse(await harness.behavior.callRpc("setup", { action: "create", threadId: thread.id, ticket: preview.ticket, task: "Fictional task", followStandard: true }));
    assert.equal(created.ok, true, created.message); assert.equal(created.record?.followsStandard, true);
    assert.equal(followSeen, 3, "preview, the fresh check and the creation all followed the list");
    // Inspected while on a standard route: verifiable.
    assert.ok(created.checks?.length, created.message);
    // Pause, stop and resume all reach the CLI with the target moved.
    for (const action of ["pause", "stop", "resume"] as const) {
      const r = await harness.behavior.callRpc("control", { configPath: created.record!.configPath, managedId: created.record!.id, action, expectedThreadId: thread.id }) as { ok: boolean; message: string };
      assert.equal(r.ok, true, `${action}: ${r.message}`); assert.ok(operations.includes(action), action);
    }
    // An older plugin build dropped the flag; this one restores it from status.
    const saved = await bb.storage.kv.get("setups.v1") as Record<string, unknown>[];
    await bb.storage.kv.set("setups.v1", saved.map(({ followsStandard: _f, ...r }) => r));
    const reloaded = await harness.lifecycle.reload(plugin);
    active = reloaded.harness;
    const inspected = setupResult.parse(await active.behavior.callRpc("setup", { action: "inspect", threadId: thread.id }));
    assert.equal(inspected.ok, true, inspected.message); assert.equal(inspected.record?.followsStandard, true);
    assert.equal(((await reloaded.bb.storage.kv.get("setups.v1")) as { followsStandard?: true }[])[0].followsStandard, true);
    // A single-agent loop on another target is still refused.
    state.routes = undefined as never; state.redundancy = undefined as never;
    const refused = setupResult.parse(await active.behavior.callRpc("setup", { action: "inspect", threadId: thread.id }));
    assert.equal(refused.canStart, false);
  } finally { await active.lifecycle.dispose(); }
});
