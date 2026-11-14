import { test } from "node:test";
import assert from "node:assert/strict";
import { createFakePluginHost, makeThreadResponse, makeHostResponse } from "@get-bb/plugin-sdk/testing";
import plugin from "./server.ts";
import { hostContract } from "./host-contract.ts";
import { transferResult } from "./transfer-contract.ts";

async function fixture(extra: Record<string, unknown> = {}) {
  const record = { id: "record-example", threadId: "thr_source", projectId: "proj_example", environmentId: "env_example", hostId: "host_example", project: "/projects/example", provider: "codex", loop: "example-loop", configPath: "/private/example/keepgoing.yaml" , ...extra };
  let target = record.threadId, bound = false, lost = false;
  let waitApply: (() => Promise<void>) | undefined;
  const controls = { enabled: true, paused: true, stop: true, completed: true, awaitingUser: false };
  const operations: string[] = [];
  const host = makeHostResponse({ id: record.hostId, status: "connected" });
  const environment = { id: record.environmentId, projectId: record.projectId, hostId: host.id, path: record.project, isGitRepo: true, status: "ready", hostLifecycle: "active", lifecycle: { phase: "active" } };
  const { bb, harness } = createFakePluginHost({
    pluginId: "keepgoing", settings: { keepgoingPath: "keepgoing", configs: "" },
    sdk: {
      threads: { get: async ({ threadId }: { threadId: string }) => makeThreadResponse({ id: threadId, environmentId: record.environmentId, projectId: record.projectId, providerId: "codex" }) },
      environments: { get: async () => environment }, hosts: { get: async () => host },
    },
    experimental_callHostRpc: async ({ method, input, hostId }) => {
      assert.equal(hostId, record.hostId);
      if (method === "transfer") {
        const p = hostContract.transfer.input.parse(input); operations.push(p.operation);
        assert.equal(p.record.configPath, record.configPath); assert.equal(p.destination.threadId, "thr_destination");
        if (p.operation === "preview") {
          const identity = (thread: string) => ({ thread, provider: "codex", project: record.projectId, environment: record.environmentId, host: record.hostId });
          return { code: 0, data: { schemaVersion: 1, outcome: "preview", compatibility: "compatible_metadata", applySupported: true, from: identity(target), to: identity(p.destination.threadId), configuredProvider: "codex", configuredQuotaGroup: "primary", controls, owner: record.threadId } };
        }
        if (waitApply) await waitApply();
        target = p.destination.threadId; bound = true; controls.awaitingUser = true;
        if (lost) throw new Error("PRIVATE-LOST-RESPONSE");
        return { code: 0, data: { outcome: "applied" } };
      }
      assert.equal(method, "command", "setup must not create a competing loop");
      const p = hostContract.command.input.parse(input); operations.push(p.operation);
      const agent = { adapter: "bb", bbThread: target, provider: record.provider };
      if (p.operation === "status") return { code: 0, data: { loop: record.loop, target: "primary", primaryAgent: agent, targetAgent: agent, controlState: controls.awaitingUser ? "awaiting user" : "paused", control: { ...controls, ...(bound ? { bbTarget: target, ownership: { threadId: target, generation: 8 } } : {}) }, window: "open", state: {}, daemon: { state: "not_recorded" } } };
      if (p.operation === "doctor") return { code: 0, data: { schemaVersion: 1, targetAgent: agent, checks: [{ id: "agent.primary.target", status: "ok", message: "Fixture" }] } };
      if (p.operation === "resume") controls.awaitingUser = false;
      return { code: 0, data: null };
    },
  });
  await bb.storage.kv.set("setups.v1", [record]);
  await plugin(bb);
  return { bb, harness, record, operations, environment, setLost: () => { lost = true; }, setWait: (fn: () => Promise<void>) => { waitApply = fn; }, input: { threadId: "thr_destination", recordId: record.id } };
}

test("SDK transfer routes verified host identities and moves the registration without resuming", async () => {
  const f = await fixture();
  try {
    const p = transferResult.parse(await f.harness.behavior.callRpc("transfer", { ...f.input, action: "preview", configPath: "/untrusted", bin: "untrusted" }));
    assert.equal(p.ok, true, p.message);
    const r = transferResult.parse(await f.harness.behavior.callRpc("transfer", { ...f.input, action: "apply", ticket: p.ticket }));
    assert.equal(r.moved, true, r.message); assert.ok(!f.operations.includes("resume")); assert.ok(!f.operations.includes("start"));
    const old = await f.harness.behavior.callRpc("forThread", { threadId: "thr_source" }) as { loops: unknown[] }; assert.equal(old.loops.length, 0);
    const next = await f.harness.behavior.callRpc("forThread", { threadId: "thr_destination" }) as { loops: { supervising: boolean; controlState: string }[] };
    assert.equal(next.loops[0].supervising, true); assert.equal(next.loops[0].controlState, "awaiting user");
    for (const op of ["threads.spawn", "threads.fork", "threads.send"]) assert.deepEqual(f.harness.inspection.sdk.callsTo(op), []);
    const setup = await f.harness.behavior.callRpc("setup", { threadId: "thr_destination", action: "inspect" }) as { canStart: boolean }; assert.equal(setup.canStart, false);
    const stale = await f.harness.behavior.callRpc("control", { configPath: f.record.configPath, managedId: f.record.id, action: "resume", expectedThreadId: "thr_source" }) as { ok: boolean };
    assert.equal(stale.ok, false); assert.ok(!f.operations.includes("resume"));
    await f.harness.behavior.callRpc("control", { configPath: f.record.configPath, managedId: f.record.id, action: "resume", expectedThreadId: "thr_destination" });
    assert.equal(f.operations.filter(o => o === "resume").length, 1);
  } finally { await f.harness.lifecycle.dispose(); }
});

test("SDK reload retains uncertain move, blocks resume/start and reconciles without replay", async () => {
  const f = await fixture(); let harness = f.harness;
  try {
    const p = transferResult.parse(await harness.behavior.callRpc("transfer", { ...f.input, action: "preview" })); f.setLost();
    const r = transferResult.parse(await harness.behavior.callRpc("transfer", { ...f.input, action: "apply", ticket: p.ticket })); assert.equal(r.ok, false); assert.ok(!JSON.stringify(r).includes("PRIVATE"));
    harness = (await harness.lifecycle.reload(plugin)).harness;
    const view = await harness.behavior.callRpc("forThread", { threadId: "thr_destination" }) as { loops: { controlState: string }[] }; assert.equal(view.loops[0].controlState, "move requires inspection");
    const resume = await harness.behavior.callRpc("control", { managedId: f.record.id, configPath: f.record.configPath, action: "resume" }) as { ok: boolean }; assert.equal(resume.ok, false);
    const start = await harness.behavior.callRpc("setup", { action: "start", threadId: "thr_destination" }) as { ok: boolean }; assert.equal(start.ok, false);
    assert.ok(!f.operations.includes("resume")); assert.ok(!f.operations.includes("start"));
    const done = transferResult.parse(await harness.behavior.callRpc("transfer", { ...f.input, action: "inspect" })); assert.equal(done.moved, true, done.message);
    assert.equal(f.operations.filter(o => o === "apply").length, 1);
  } finally { await harness.lifecycle.dispose(); }
});

test("concurrent setup/apply cannot duplicate a destination; restrictive stop does not wait for transfer", async () => {
  const f = await fixture();
  try {
    const p = transferResult.parse(await f.harness.behavior.callRpc("transfer", { ...f.input, action: "preview" }));
    let release!: () => void, entered!: () => void;
    const gate = new Promise<void>(r => { release = r; }), ready = new Promise<void>(r => { entered = r; });
    f.setWait(async () => { entered(); await gate; });
    const applying = f.harness.behavior.callRpc("transfer", { ...f.input, action: "apply", ticket: p.ticket });
    await ready;
    const duplicate = f.harness.behavior.callRpc("transfer", { ...f.input, action: "apply", ticket: p.ticket });
    const creating = f.harness.behavior.callRpc("setup", { threadId: "thr_destination", action: "preview", name: "duplicate", task: "Fictional task" });
    try {
      await f.harness.behavior.callRpc("control", { configPath: f.record.configPath, managedId: f.record.id, action: "stop" });
      assert.ok(f.operations.includes("stop"));
    } finally { release(); }
    assert.equal(transferResult.parse(await applying).moved, true);
    assert.equal(transferResult.parse(await duplicate).ok, false);
    assert.equal((await creating as { ok: boolean }).ok, false);
    assert.equal(f.operations.filter(o => o === "apply").length, 1);
  } finally { await f.harness.lifecycle.dispose(); }
});

test("a setup that follows the standard route list is moved by asking the CLI, not refused by the plugin", async () => {
  const f = await fixture({ followsStandard: true });
  try {
    const p = transferResult.parse(await f.harness.behavior.callRpc("transfer", { ...f.input, action: "preview" }));
    assert.ok(f.operations.includes("preview"), `the CLI was not asked: ${p.message}`);
    assert.doesNotMatch(p.message, /Thread, provider or environment changed/);
  } finally { await f.harness.lifecycle.dispose(); }
});
