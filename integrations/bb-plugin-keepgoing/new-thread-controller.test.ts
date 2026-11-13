import { test } from "node:test";
import assert from "node:assert/strict";
import { createNewThreadController, INTENT_TTL_MS, JOB_WAIT_MS, type Dispatch, type NewThreadDependencies } from "./new-thread-controller.ts";
import { DEFAULT_BOUNDS, boundsPhrase, durationMs, fallbackName, loopName, marker, type StartMode } from "./new-thread-contract.ts";
import type { SetupResult } from "./setup-contract.ts";

const TASK = "Fictional private task. Done when the example passes.";
const limits = { interval: "5m0s", wakeEvery: "1h0m0s", stallAfter: "30m0s", backoff: "10m0s", maxNudges: 3, maxReprompts: 3 };

function fixture() {
  const w = {
    now: 1_000_000, ids: 0, mode: "manual" as StartMode, unavailable: null as string | null,
    ready: { title: "Release notes for 2.4" } as Awaited<ReturnType<NewThreadDependencies["ready"]>>,
    supervision: "none" as "none" | "supervised" | "unverified",
    setups: [] as { action: string; threadId: string; name?: string; task?: string; bounds: unknown }[],
    previewFails: [] as string[], createOk: true, canStart: true, startOk: true, changed: 0,
  };
  const d: NewThreadDependencies = {
    now: () => w.now, id: () => `intent-${++w.ids}`, mode: async () => w.mode, unavailable: async () => w.unavailable,
    ready: async () => w.ready, supervision: async () => w.supervision,
    setup: async (input, bounds): Promise<SetupResult> => {
      w.setups.push({ action: input.action, threadId: input.threadId, name: input.name, task: input.task, bounds });
      const plan = { loop: input.name ?? "example", project: "/projects/example", configPath: "/private/example/keepgoing.yaml", stateDir: "/private/example/state", threadId: input.threadId, provider: "example-provider", limits };
      const record = { id: "record", loop: w.setups.find(s => s.action === "preview" && !w.previewFails.includes(s.name ?? ""))?.name ?? "example", configPath: plan.configPath, threadId: input.threadId, environmentId: "env_example", projectId: "proj_a", hostId: "host_example", project: plan.project, provider: plan.provider };
      if (input.action === "preview") return w.previewFails.includes(input.name ?? "") ? { ok: false, message: "Setup could not complete." } : { ok: true, message: "Preview only.", ticket: `ticket-${w.setups.length}`, plan };
      if (input.action === "create") return w.createOk ? { ok: true, message: "Review the diagnostics.", record, canStart: w.canStart } : { ok: false, message: "A disabled setup may exist; inspect the destination before retrying." };
      return { ok: w.startOk, message: w.startOk ? "Background supervision started." : "Start could not be confirmed." };
    },
    changed: () => { w.changed++; },
  };
  const c = createNewThreadController(d);
  const arm = async (draftKey = "draft-a", projectId = "proj_a") => {
    const r = await c.call({ action: "arm", draftKey, projectId, bounds: DEFAULT_BOUNDS });
    assert.equal(r.ok, true, r.message); return r.intent!;
  };
  const dispatch = (intent: string, over: Partial<Dispatch> = {}): Dispatch => ({
    data: marker(intent), threadId: "thr_new", projectId: "proj_a", threadCreatedAt: w.now, attempt: "start-turn", initiator: "user",
    senderThreadId: null, parentThreadId: null, sourceThreadId: null, queued: 0, text: TASK, ...over,
  });
  const outcome = async (threadId = "thr_new") => (await c.call({ action: "outcome", threadId })).outcome!;
  return { w, c, d, arm, dispatch, outcome };
}

test("an armed send creates a switched-off, bounded setup for the thread it created and starts nothing", async () => {
  const f = fixture();
  const armed = await f.c.call({ action: "arm", draftKey: "draft-a", projectId: "proj_a", bounds: DEFAULT_BOUNDS });
  assert.equal(armed.mode, "manual"); assert.equal(armed.message, boundsPhrase(DEFAULT_BOUNDS));
  assert.ok(!JSON.stringify(armed).includes("Fictional"));
  f.c.dispatched(f.dispatch(armed.intent!));
  assert.equal(f.w.setups.length, 0, "the hook itself runs no setup");
  await f.c.advance("thr_new");
  assert.deepEqual(f.w.setups.map(s => s.action), ["preview", "create"]);
  assert.equal(f.w.setups[0].name, "release-notes-for-2-4");
  for (const s of f.w.setups) { assert.equal(s.task, TASK); assert.deepEqual(s.bounds, DEFAULT_BOUNDS); assert.equal(s.threadId, "thr_new"); }
  const o = await f.outcome();
  assert.equal(o.state, "ready"); assert.equal(o.loop, "release-notes-for-2-4"); assert.deepEqual(o.bounds, DEFAULT_BOUNDS);
  assert.equal(o.startedAutomatically, false); assert.match(o.cadence!, /Checks every 5m0s.*at most 3 times/);
  assert.ok(!JSON.stringify(o).includes("Fictional"), "the task never appears in what the browser is told");
  assert.ok(!JSON.stringify(o).includes("/private"), "no path is shown");
  // Later lifecycle events and status reads repeat nothing.
  await f.c.advance("thr_new"); await f.outcome();
  assert.equal(f.w.setups.length, 2);
});

test("automatic start needs the mode shown when the control was turned on and still set afterwards", async () => {
  const auto = fixture(); auto.w.mode = "automatic";
  auto.c.dispatched(auto.dispatch(await auto.arm())); await auto.c.advance("thr_new");
  assert.deepEqual(auto.w.setups.map(s => s.action), ["preview", "create", "start"]);
  assert.equal((await auto.outcome()).startedAutomatically, true);

  const turnedOff = fixture(); turnedOff.w.mode = "automatic";
  const intent = await turnedOff.arm(); turnedOff.w.mode = "manual";
  turnedOff.c.dispatched(turnedOff.dispatch(intent)); await turnedOff.c.advance("thr_new");
  assert.ok(!turnedOff.w.setups.some(s => s.action === "start"));

  const turnedOn = fixture();
  const manual = await turnedOn.arm(); turnedOn.w.mode = "automatic";
  turnedOn.c.dispatched(turnedOn.dispatch(manual)); await turnedOn.c.advance("thr_new");
  assert.ok(!turnedOn.w.setups.some(s => s.action === "start"), "a person who saw manual mode gets manual mode");

  const cannot = fixture(); cannot.w.mode = "automatic"; cannot.w.canStart = false;
  cannot.c.dispatched(cannot.dispatch(await cannot.arm())); await cannot.c.advance("thr_new");
  assert.ok(!cannot.w.setups.some(s => s.action === "start"));

  const unconfirmed = fixture(); unconfirmed.w.mode = "automatic"; unconfirmed.w.startOk = false;
  unconfirmed.c.dispatched(unconfirmed.dispatch(await unconfirmed.arm())); await unconfirmed.c.advance("thr_new");
  const o = await unconfirmed.outcome();
  assert.equal(o.startedAutomatically, false); assert.match(o.message, /could not be confirmed/);
});

test("two New thread tabs each reach only the thread their own message created", async () => {
  const f = fixture();
  const a = await f.arm("draft-a", "proj_a"); const b = await f.arm("draft-b", "proj_a");
  assert.notEqual(a, b);
  f.c.dispatched(f.dispatch(b, { threadId: "thr_b", text: "Fictional task B" }));
  f.c.dispatched(f.dispatch(a, { threadId: "thr_a", text: "Fictional task A" }));
  await f.c.advance("thr_a"); await f.c.advance("thr_b");
  const byThread = (id: string) => f.w.setups.filter(s => s.threadId === id).map(s => s.task);
  assert.deepEqual(byThread("thr_a"), ["Fictional task A", "Fictional task A"]);
  assert.deepEqual(byThread("thr_b"), ["Fictional task B", "Fictional task B"]);
});

test("turning the control on again for the same draft replaces the earlier request", async () => {
  const f = fixture();
  const first = await f.arm("draft-a"); const second = await f.arm("draft-a");
  f.c.dispatched(f.dispatch(first, { threadId: "thr_old" })); await f.c.advance("thr_old");
  assert.equal(f.w.setups.length, 0); assert.equal((await f.outcome("thr_old")).state, "failed");
  f.c.dispatched(f.dispatch(second)); await f.c.advance("thr_new");
  assert.equal(f.w.setups.length, 2);
});

test("a send that failed leaves nothing behind", async () => {
  const f = fixture();
  const intent = await f.arm();
  await f.c.call({ action: "cancel", intent });
  f.c.dispatched(f.dispatch(intent)); await f.c.advance("thr_new");
  assert.equal(f.w.setups.length, 0);
  const o = await f.outcome();
  assert.equal(o.state, "failed"); assert.match(o.message, /nothing was created or started/);
});

test("a thread from the CLI, another plugin or an agent at the same moment is never supervised", async () => {
  const cases: [string, Partial<Dispatch>][] = [
    ["another project", { projectId: "proj_other" }],
    ["joins a running turn", { attempt: "join-turn" }],
    ["sent by an agent", { initiator: "agent" }],
    ["sent from another thread", { senderThreadId: "thr_sender" }],
    ["a child thread", { parentThreadId: "thr_parent" }],
    ["a fork", { sourceThreadId: "thr_source" }],
    ["a queued re-attempt", { queued: 1 }],
    ["a thread that existed before the control was turned on", { threadCreatedAt: 999_999 }],
  ];
  for (const [name, over] of cases) {
    const f = fixture();
    const intent = await f.arm();
    f.c.dispatched(f.dispatch(intent, over)); await f.c.advance("thr_new");
    assert.equal(f.w.setups.length, 0, name);
    assert.equal((await f.outcome()).state, "failed", name);
    // Single use: the same marker cannot be tried again on the right thread.
    f.c.dispatched(f.dispatch(intent, { threadId: "thr_right" })); await f.c.advance("thr_right");
    assert.equal(f.w.setups.length, 0, `${name}: replay`);
  }
  // Without a marker, or with a malformed one, nothing is recorded at all.
  const f = fixture(); await f.arm();
  for (const data of [null, undefined, "intent-1", { v: 2, intent: "intent-1" }, { v: 1, intent: "intent-1", extra: true }, { v: 1 }]) {
    f.c.dispatched(f.dispatch("intent-1", { data })); await f.c.advance("thr_new");
  }
  assert.equal(f.w.setups.length, 0); assert.equal((await f.outcome()).state, "none");
});

test("a marker is single use: the same message seen again changes nothing, another thread gets nothing", async () => {
  const f = fixture();
  const intent = await f.arm();
  f.c.dispatched(f.dispatch(intent));
  f.c.dispatched(f.dispatch(intent)); // BB re-runs the hook after a restart or drain
  f.c.dispatched(f.dispatch(intent, { threadId: "thr_other" }));
  await f.c.advance("thr_new"); await f.c.advance("thr_other");
  assert.deepEqual(f.w.setups.map(s => s.threadId), ["thr_new", "thr_new"]);
  assert.equal((await f.outcome()).state, "ready"); assert.equal((await f.outcome("thr_other")).state, "failed");
});

test("a browser reload between send and creation changes nothing; a plugin reload or an expired request creates nothing", async () => {
  const reloadedBrowser = fixture();
  const intent = await reloadedBrowser.arm(); // the browser is gone after this; the request lives on the server
  reloadedBrowser.w.now += 30_000;
  reloadedBrowser.c.dispatched(reloadedBrowser.dispatch(intent, { threadCreatedAt: reloadedBrowser.w.now })); await reloadedBrowser.c.advance("thr_new");
  assert.equal((await reloadedBrowser.outcome()).state, "ready");

  const before = fixture(); const lost = await before.arm();
  const after = fixture(); // a reloaded plugin has no memory of the request
  after.c.dispatched(after.dispatch(lost)); await after.c.advance("thr_new");
  assert.equal(after.w.setups.length, 0); assert.equal((await after.outcome()).state, "failed");

  const late = fixture(); const old = await late.arm();
  late.w.now += INTENT_TTL_MS + 1;
  late.c.dispatched(late.dispatch(old, { threadCreatedAt: late.w.now })); await late.c.advance("thr_new");
  assert.equal(late.w.setups.length, 0); assert.equal((await late.outcome()).state, "failed");
});

test("setup waits for the thread's environment, gives up after ten minutes, and does not keep the task", async () => {
  const f = fixture(); f.w.ready = null;
  f.c.dispatched(f.dispatch(await f.arm()));
  await f.c.advance("thr_new");
  assert.equal(f.w.setups.length, 0); assert.equal((await f.outcome()).state, "waiting");
  f.w.ready = { title: "Release notes for 2.4" };
  assert.equal((await f.outcome()).state, "ready", "reading the outcome moves a waiting request forward");
  assert.equal(f.w.setups.length, 2);

  const slow = fixture(); slow.w.ready = null;
  slow.c.dispatched(slow.dispatch(await slow.arm()));
  slow.w.now += JOB_WAIT_MS + 1; slow.w.ready = { title: "Late" };
  const o = await slow.outcome();
  assert.equal(o.state, "failed"); assert.match(o.message, /not ready in time/); assert.equal(slow.w.setups.length, 0);

  const never = fixture(); never.w.ready = { reason: "This thread does not work in a Git repository." };
  never.c.dispatched(never.dispatch(await never.arm())); await never.c.advance("thr_new");
  assert.equal((await never.outcome()).message, "This thread does not work in a Git repository."); assert.equal(never.w.setups.length, 0);

  const gone = fixture(); gone.w.ready = null;
  gone.c.dispatched(gone.dispatch(await gone.arm())); gone.c.forget("thr_new");
  gone.w.ready = { title: "x" }; await gone.c.advance("thr_new");
  assert.equal(gone.w.setups.length, 0); assert.equal((await gone.outcome()).state, "none");
});

test("an existing loop, an unverified status, a missing program and failed setup are explained, never silent", async () => {
  const supervised = fixture(); supervised.w.supervision = "supervised";
  supervised.c.dispatched(supervised.dispatch(await supervised.arm())); await supervised.c.advance("thr_new");
  assert.equal(supervised.w.setups.length, 0); assert.match((await supervised.outcome()).message, /already supervises this thread/);

  const unverified = fixture(); unverified.w.supervision = "unverified";
  unverified.c.dispatched(unverified.dispatch(await unverified.arm())); await unverified.c.advance("thr_new");
  assert.equal(unverified.w.setups.length, 0); assert.match((await unverified.outcome()).message, /could not check/);

  const missing = fixture(); missing.w.unavailable = "The keepgoing program was not found.";
  for (const action of ["check", "arm"] as const) {
    const r = await missing.c.call(action === "check" ? { action } : { action, draftKey: "d", projectId: "p", bounds: DEFAULT_BOUNDS });
    assert.equal(r.ok, false); assert.equal(r.message, "The keepgoing program was not found."); assert.equal(r.intent, undefined);
  }

  const failed = fixture(); failed.w.createOk = false;
  failed.c.dispatched(failed.dispatch(await failed.arm())); await failed.c.advance("thr_new");
  assert.match((await failed.outcome()).message, /disabled setup may exist/);
  await failed.c.advance("thr_new"); await failed.outcome();
  assert.equal(failed.w.setups.filter(s => s.action === "create").length, 1, "a creation is never retried");

  const empty = fixture();
  empty.c.dispatched(empty.dispatch(await empty.arm(), { text: "   " })); await empty.c.advance("thr_new");
  assert.equal(empty.w.setups.length, 0); assert.match((await empty.outcome()).message, /no text keepgoing can use/);
});

test("the loop is named after the thread title, with a fallback when the title gives no name or the name is taken", async () => {
  assert.equal(loopName("Release notes for 2.4!"), "release-notes-for-2-4");
  assert.equal(loopName("  Émigré — café  "), "emigre-cafe");
  assert.equal(loopName("日本語"), ""); assert.equal(loopName(null), "");
  assert.match(loopName("x".repeat(200)), /^x{48}$/);
  assert.equal(fallbackName("thr_Example123"), "bb-thr-example123");
  assert.equal(durationMs("24h0m0s"), durationMs("24h")); assert.equal(durationMs("1h30m"), 5_400_000); assert.equal(durationMs("soon"), null); assert.equal(durationMs(""), null);

  const untitled = fixture(); untitled.w.ready = { title: null };
  untitled.c.dispatched(untitled.dispatch(await untitled.arm())); await untitled.c.advance("thr_new");
  assert.equal(untitled.w.setups[0].name, "bb-thr-new");

  const taken = fixture(); taken.w.previewFails = ["release-notes-for-2-4"];
  taken.c.dispatched(taken.dispatch(await taken.arm())); await taken.c.advance("thr_new");
  assert.deepEqual(taken.w.setups.map(s => `${s.action}:${s.name ?? ""}`), ["preview:release-notes-for-2-4", "preview:bb-thr-new", "create:"]);
  assert.equal((await taken.outcome()).state, "ready");
});

test("bounds outside the closed list are refused before anything is recorded", async () => {
  const f = fixture();
  for (const bounds of [{ maxSends: 0, noProgressTimeout: "24h" }, { maxSends: 5000, noProgressTimeout: "24h" }, { maxSends: 40, noProgressTimeout: "0" }, { maxSends: 40 }]) {
    const r = await f.c.call({ action: "arm", draftKey: "d", projectId: "p", bounds } as never);
    assert.equal(r.ok, false); assert.equal(r.intent, undefined);
  }
});
