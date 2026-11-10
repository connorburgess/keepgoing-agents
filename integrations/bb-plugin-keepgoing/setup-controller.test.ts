import { test } from "node:test";
import assert from "node:assert/strict";
import { createSetupController, type SetupDependencies } from "./setup-controller.ts";
import type { SetupRecord, SetupTarget, SetupPlan } from "./setup-contract.ts";

export function fixture() {
  const target: SetupTarget = { threadId: "thr_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: "/projects/example", provider: "codex" };
  const plan: SetupPlan = { loop: "example-loop", project: target.project, configPath: "/private/example-loop/keepgoing.yaml", stateDir: "/private/example-loop/state", threadId: target.threadId, provider: target.provider, limits: { interval: "5m0s", wakeEvery: "1h0m0s", stallAfter: "30m0s", backoff: "10m0s", maxNudges: 3, maxReprompts: 3 } };
  let saved: SetupRecord[] = [];
  const calls: string[] = [];
  const agent = { adapter: "bb", bbThread: target.threadId, provider: target.provider };
  const state = { target: "primary", primaryAgent: agent, targetAgent: agent, controlState: "disabled", daemon: { state: "not_recorded" } };
  const doctor = { schemaVersion: 1, targetAgent: agent, checks: [{ id: "agent.primary.target", status: "ok", message: "Verified fixture target" }] };
  const d: SetupDependencies = {
    target: async () => ({ ...target }), binary: async () => "keepgoing",
    records: async () => saved, save: async r => { saved = r; }, guard: async () => {},
    init: async (_bin, _target, name, task, dry) => { assert.equal(name, "example-loop"); assert.equal(task, "Fictional private task"); calls.push(dry ? "preview" : "create"); return { plan: structuredClone(plan) }; },
    command: async (_bin, record, op) => { assert.equal(record.hostId, "host_example"); calls.push(op); return { code: 0, data: op === "doctor" ? doctor : op === "status" ? state : null }; },
    changed: () => calls.push("changed"),
  };
  const controller = createSetupController(d);
  const preview = () => controller.call({ action: "preview", threadId: target.threadId, name: "example-loop", task: "Fictional private task" });
  const create = (ticket?: string) => controller.call({ action: "create", threadId: "thr_example", ticket, task: "Fictional private task" });
  return { controller, d, target, plan, calls, state, doctor, preview, create, records: () => saved };
}

test("preview, disabled create, diagnostics, and explicit start remain separate", async () => {
  const f = fixture();
  const p = await f.preview();
  assert.equal(p.ok, true); assert.ok(p.ticket); assert.deepEqual(f.calls, ["preview"]); assert.equal(f.records().length, 0);
  assert.ok(!JSON.stringify(p).includes("Fictional private task"));
  const created = await f.create(p.ticket);
  assert.equal(created.ok, true); assert.equal(created.canStart, true); assert.equal(f.records().length, 1);
  assert.ok(!f.calls.includes("start"));
  const started = await f.controller.call({ action: "start", threadId: "thr_example" });
  assert.equal(started.ok, true); assert.equal(started.canStart, false); assert.equal(f.calls.filter(x => x === "start").length, 1);
  assert.equal((await f.create(p.ticket)).ok, false);
});

test("changed task, target, binary, and resolved plan invalidate a preview", async () => {
  for (const change of ["task", "target", "binary", "plan"]) {
    const f = fixture(); const p = await f.preview();
    if (change === "target") f.target.environmentId = "env_other";
    if (change === "binary") f.d.binary = async () => "other-binary";
    if (change === "plan") f.plan.configPath = "/private/other/keepgoing.yaml";
    const r = change === "task" ? await f.controller.call({ action: "create", threadId: "thr_example", ticket: p.ticket, task: "Changed task" }) : await f.create(p.ticket);
    assert.equal(r.ok, false, change); assert.ok(!f.calls.includes("create")); assert.ok(!f.calls.includes("start")); assert.equal(f.records().length, 0);
  }
});

test("concurrent creates cannot duplicate a loop or replay a preview", async () => {
  const f = fixture(); const a = await f.preview(); const b = await f.preview();
  const results = await Promise.all([f.create(a.ticket), f.create(b.ticket), f.create(a.ticket)]);
  assert.equal(results.filter(r => r.ok).length, 1); assert.equal(f.records().length, 1); assert.equal(f.calls.filter(x => x === "create").length, 1);
});

test("holds, unknown target diagnostics, responding daemon and changed config block start", async () => {
  for (const scenario of ["paused", "stop requested", "awaiting user", "completed", "unknown", "error", "responsive", "wrong target", "provider", "fallbacks", "moved"]) {
    const f = fixture(); await f.create((await f.preview()).ticket); f.calls.length = 0;
    if (["paused", "stop requested", "awaiting user", "completed"].includes(scenario)) f.state.controlState = scenario;
    if (scenario === "unknown") f.doctor.checks[0].status = "unavailable";
    if (scenario === "error") f.doctor.checks.push({ id: "config", status: "error", message: "Fixture failure" });
    if (scenario === "responsive") f.state.daemon.state = "responsive";
    if (scenario === "wrong target") f.state.targetAgent = { ...f.state.targetAgent, bbThread: "thr_other" };
    if (scenario === "provider") f.state.primaryAgent = { ...f.state.primaryAgent, provider: "other" };
    if (scenario === "fallbacks") Object.assign(f.state, { redundancy: {} });
    if (scenario === "moved") f.target.hostId = "host_other";
    const r = await f.controller.call({ action: "start", threadId: "thr_example" });
    assert.notEqual(r.canStart, true, scenario); assert.ok(!f.calls.includes("start"), scenario);
  }
});

test("failed metadata, existing-loop guard and storage errors never start or leak task errors", async () => {
  for (const stage of ["target", "guard", "save"] as const) {
    const f = fixture(); const p = await f.preview();
    f.d[stage] = async () => { throw new Error("Fictional private task"); };
    const r = await f.create(p.ticket);
    assert.equal(r.ok, false); assert.ok(!JSON.stringify(r).includes("Fictional private task")); assert.ok(!f.calls.includes("start"));
    if (stage === "save") { assert.ok(r.record?.configPath); assert.equal(f.records().length, 0); }
    else assert.ok(!f.calls.includes("create"));
  }
});

test("reload/dispose invalidates in-memory previews without retaining task content", async () => {
  const f = fixture(); const p = await f.preview(); f.controller.dispose();
  assert.equal((await f.create(p.ticket)).ok, false); assert.ok(!f.calls.includes("create"));
});
