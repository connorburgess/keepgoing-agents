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
  assert.equal(started.ok, true); assert.equal(started.started, true); assert.equal(started.canStart, false); assert.equal(f.calls.filter(x => x === "start").length, 1);
  // A second start is declined by the re-check: ok, and explicitly not a start.
  f.state.controlState = "paused";
  const declined = await f.controller.call({ action: "start", threadId: "thr_example" });
  assert.equal(declined.ok, true); assert.notEqual(declined.started, true); assert.equal(f.calls.filter(x => x === "start").length, 1);
  assert.equal((await f.create(p.ticket)).ok, false);
});

test("loop-wide bounds given at preview are passed to every init and cannot be dropped or changed at creation", async () => {
  const f = fixture();
  const seen: unknown[] = [];
  const init = f.d.init;
  f.d.init = async (bin, target, name, task, dry, goal, bounds) => { seen.push(bounds); return init(bin, target, name, task, dry, goal, bounds); };
  const bounds = { maxSends: 40, noProgressTimeout: "24h" as const };
  const call = (action: "preview" | "create", ticket?: string, b?: typeof bounds) => f.controller.call({ action, threadId: "thr_example", name: "example-loop", task: "Fictional private task", ticket }, b);
  const dropped = await call("preview", undefined, bounds);
  assert.equal((await call("create", dropped.ticket)).ok, false, "a bounded preview is not created unbounded");
  const changed = await call("preview", undefined, bounds);
  assert.equal((await call("create", changed.ticket, { maxSends: 400, noProgressTimeout: "24h" })).ok, false);
  const added = await call("preview");
  assert.equal((await call("create", added.ticket, bounds)).ok, false, "an unbounded preview is not created bounded");
  assert.equal(f.records().length, 0); assert.ok(!f.calls.includes("create"));
  const kept = await call("preview", undefined, bounds);
  assert.equal((await call("create", kept.ticket, bounds)).ok, true);
  assert.deepEqual(seen.slice(-3), [bounds, bounds, bounds]); assert.equal(f.records().length, 1);
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

test("goal selection binds the preview and is passed through every init call", async () => {
  const f = fixture(); const choices: boolean[] = [];
  f.d.init = async (_bin, _target, _name, _task, dry, withGoal) => {
    choices.push(withGoal); f.calls.push(dry ? "preview" : "create");
    return { plan: { ...f.plan, ...(withGoal ? { withGoal: true } : {}) } };
  };
  const request = { threadId: f.target.threadId, name: "example-loop", task: "Fictional private task", withGoal: true };
  const p = await f.controller.call({ ...request, action: "preview" });
  assert.equal(p.plan?.withGoal, true);
  const changed = await f.controller.call({ ...request, action: "create", ticket: p.ticket, withGoal: false });
  assert.equal(changed.ok, false); assert.ok(!f.calls.includes("create"));
  const goal = { id: "goal_0123456789abcdef0123456789abcdef", revision: 1, state: "recorded", title: "PRIVATE TITLE" };
  Object.assign(f.state, { goal });
  const next = await f.controller.call({ ...request, action: "preview" });
  const created = await f.controller.call({ ...request, action: "create", ticket: next.ticket });
  assert.equal(created.ok, true); assert.equal(created.goal?.revision, 1); assert.equal(created.goalState, "recorded");
  assert.ok(!JSON.stringify(created).includes("PRIVATE")); assert.ok(!f.calls.includes("start"));
  assert.ok(choices.length >= 4 && choices.every(Boolean));
  Object.assign(f.state, { goalError: "PRIVATE FAILURE" });
  const held = await f.controller.call({ action: "start", threadId: f.target.threadId });
  assert.equal(held.canStart, false); assert.equal(held.goalState, "unavailable");
  assert.ok(!f.calls.includes("start")); assert.ok(!JSON.stringify(held).includes("PRIVATE"));
});

const refused = { code: "second_loop_on_session" as const, otherLoop: "other-loop" };

test("keepgoing's refusal to set up a second loop is shown in plain words with the other loop's name, and nothing is created", async () => {
  for (const at of ["preview", "create"] as const) {
    const f = fixture();
    const init = f.d.init;
    f.d.init = async (bin, target, name, task, dry, goal, bounds) => {
      // Refused at preview, or only when creation re-checks (another loop was enabled in between).
      if ((at === "preview" && dry) || (at === "create" && !dry)) { f.calls.push(dry ? "preview" : "create"); return { refused }; }
      return init(bin, target, name, task, dry, goal, bounds);
    };
    const p = await f.preview();
    const r = at === "preview" ? p : await f.create(p.ticket);
    assert.equal(r.ok, false, at); assert.deepEqual(r.refused, refused);
    assert.equal(r.message, "keepgoing refused to set up this loop: loop other-loop already supervises this thread's session, and two loops on one session would both prompt it. Nothing was created. To have this thread supervised here instead, pause or disable other-loop first.");
    assert.equal(f.records().length, 0, `${at}: no reference is saved`);
    assert.ok(!f.calls.includes("start"), `${at}: nothing is started`);
    assert.ok(!/allow-second-loop|override|anyway/i.test(r.message), "the plugin offers no way around the refusal");
  }
});

test("keepgoing's refusal to start is shown with the other loop's name and is not retried", async () => {
  const f = fixture();
  const p = await f.preview(); await f.create(p.ticket);
  const command = f.d.command;
  f.d.command = async (bin, record, op) => op === "start"
    ? (f.calls.push("start"), { code: 1, data: { schemaVersion: 1, outcome: "refused", refusal: refused } })
    : command(bin, record, op);
  const r = await f.controller.call({ action: "start", threadId: "thr_example" });
  assert.equal(r.ok, false); assert.equal(r.started, false); assert.equal(r.canStart, false); assert.deepEqual(r.refused, refused);
  assert.match(r.message, /^keepgoing refused to start this loop: loop other-loop already supervises this thread's session/);
  assert.match(r.message, /Nothing was started\./);
  assert.equal(f.calls.filter(c => c === "start").length, 1);
  // A start failure without a structured reason keeps the existing wording.
  f.d.command = async (bin, record, op) => op === "start" ? { code: 1, data: null } : command(bin, record, op);
  const plain = await f.controller.call({ action: "start", threadId: "thr_example" });
  assert.equal(plain.refused, undefined); assert.match(plain.message, /Start could not be confirmed/);
});

test("start gets --json only when the preview it was created from advertised it, with the same binary", async () => {
  for (const tc of [
    { name: "advertised", advertised: true, laterBin: "keepgoing", json: true },
    { name: "not advertised", advertised: false, laterBin: "keepgoing", json: false },
    { name: "advertised, then a different binary configured", advertised: true, laterBin: "/opt/other/keepgoing", json: false },
  ]) {
    const f = fixture();
    const init = f.d.init;
    f.d.init = async (...args) => {
      const r = await init(...args);
      return tc.advertised && "plan" in r ? { plan: { ...r.plan, startJson: true as const } } : r;
    };
    const seen: (true | undefined)[] = [];
    const command = f.d.command;
    f.d.command = async (bin, record, op, json) => { if (op === "start") seen.push(json); return command(bin, record, op, json); };
    const p = await f.preview(); const created = await f.create(p.ticket);
    assert.equal(created.ok, true, tc.name);
    assert.equal(f.records()[0].startJson, tc.advertised ? true : undefined, tc.name);
    f.d.binary = async () => tc.laterBin;
    // The fixture's target check does not depend on the binary.
    await f.controller.call({ action: "start", threadId: "thr_example" });
    assert.deepEqual(seen, [tc.json ? true : undefined], tc.name);
  }
});
