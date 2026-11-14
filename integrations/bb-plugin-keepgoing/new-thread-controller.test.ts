import { test } from "node:test";
import assert from "node:assert/strict";
import { createNewThreadController, INTENT_TTL_MS, JOB_WAIT_MS, MAX_INTENTS, type Dispatch, type NewThreadDependencies } from "./new-thread-controller.ts";
import { DEFAULT_BOUNDS, boundsPhrase, durationMs, loopName, marker, slug, stricter, type StartMode } from "./new-thread-contract.ts";
import type { SetupResult } from "./setup-contract.ts";

const TASK = "Fictional private task. Done when the example passes.";
const limits = { interval: "5m0s", wakeEvery: "1h0m0s", stallAfter: "30m0s", backoff: "10m0s", maxNudges: 3, maxReprompts: 3 };
type Ready = Awaited<ReturnType<NewThreadDependencies["ready"]>>;

function fixture() {
  const w = {
    now: 1_000_000, ids: 0, mode: "manual" as StartMode, unavailable: null as string | null,
    ready: { project: "/projects/Example App" } as Ready,
    supervision: "none" as "none" | "supervised" | "unverified", used: 3,
    setups: [] as { action: string; threadId: string; name?: string; task?: string; bounds: unknown }[],
    previewOk: true, createOk: true, canStart: true, start: "started" as "started" | "declined" | "failed", changed: 0,
    // What the host's plan reports as loop-wide limits: those asked for, unless a test says otherwise.
    planLimits: undefined as undefined | ((bounds: { maxSends: number; noProgressTimeout: string }) => { maxSends?: number; noProgressTimeout?: string }),
    during: undefined as undefined | ((action: string) => Promise<void> | void),
  };
  const d: NewThreadDependencies = {
    now: () => w.now, id: () => `k${++w.ids}f9-example`, mode: async () => w.mode, unavailable: async () => w.unavailable,
    capacity: async () => ({ used: w.used, max: 32 }),
    ready: async () => w.ready, supervision: async () => w.supervision,
    setup: async (input, bounds): Promise<SetupResult> => {
      w.setups.push({ action: input.action, threadId: input.threadId, name: input.name, task: input.task, bounds });
      await w.during?.(input.action);
      const name = w.setups.find(s => s.action === "preview" && s.threadId === input.threadId)?.name ?? "example";
      const reported = w.planLimits ? w.planLimits(bounds) : { maxSends: bounds.maxSends, noProgressTimeout: `${parseInt(bounds.noProgressTimeout)}h0m0s` };
      const plan = { loop: name, project: "/projects/Example App", configPath: `/private/${name}/keepgoing.yaml`, stateDir: `/private/${name}/state`, threadId: input.threadId, provider: "example-provider", limits: { ...limits, ...reported } };
      const record = { id: "record", loop: name, configPath: plan.configPath, threadId: input.threadId, environmentId: "env_example", projectId: "proj_a", hostId: "host_example", project: plan.project, provider: plan.provider };
      if (input.action === "preview") return w.previewOk ? { ok: true, message: "Preview only.", ticket: `ticket-${w.setups.length}`, plan } : { ok: false, message: "Setup could not complete." };
      if (input.action === "create") return w.createOk ? { ok: true, message: "Review the diagnostics.", record, canStart: w.canStart } : { ok: false, message: "Setup result changed." };
      if (w.start === "started") return { ok: true, started: true, message: "Background supervision started." };
      // Setup answers ok without starting when its own re-check declines.
      if (w.start === "declined") return { ok: true, canStart: false, message: "Existing holds, a responding daemon, or diagnostic failures prevent another start here." };
      return { ok: false, started: false, message: "Start could not be confirmed." };
    },
    changed: () => { w.changed++; },
  };
  const c = createNewThreadController(d);
  const arm = async (draftKey = "draft-a", projectId = "proj_a", shownMode: StartMode = w.mode) => {
    const r = await c.call({ action: "arm", draftKey, projectId, bounds: DEFAULT_BOUNDS, shownMode });
    assert.equal(r.ok, true, r.message); return r.intent!;
  };
  const dispatch = (intent: string, over: Partial<Dispatch> = {}): Dispatch => ({
    data: marker(intent), threadId: "thr_new", projectId: "proj_a", threadCreatedAt: w.now, attempt: "start-turn", initiator: "user",
    senderThreadId: null, parentThreadId: null, sourceThreadId: null, queued: 0, text: TASK, threadStatus: "pending", ...over,
  });
  const plain = (over: Partial<Dispatch> = {}): Dispatch => dispatch("", { data: null, text: "", ...over });
  // Reading asks for a pass without waiting for it; wait for passes, then read.
  const outcome = async (threadId = "thr_new") => { await c.call({ action: "outcome", threadId }); await c.whenIdle(); return (await c.call({ action: "outcome", threadId })).outcome!; };
  return { w, c, d, arm, dispatch, plain, outcome };
}

test("a marked send creates a switched-off, bounded setup for the thread it created and starts nothing in review-first mode", async () => {
  const f = fixture();
  const armed = await f.c.call({ action: "arm", draftKey: "draft-a", projectId: "proj_a", bounds: DEFAULT_BOUNDS, shownMode: "manual" });
  assert.equal(armed.mode, "manual"); assert.equal(armed.message, boundsPhrase(DEFAULT_BOUNDS)); assert.deepEqual(armed.setups, { used: 3, max: 32 });
  assert.ok(!JSON.stringify(armed).includes("Fictional"));
  f.c.dispatched(f.dispatch(armed.intent!));
  assert.equal(f.w.setups.length, 0, "the hook itself runs no setup");
  assert.equal(f.c.held().taskBytes, Buffer.byteLength(TASK));
  await f.c.advance("thr_new");
  assert.deepEqual(f.w.setups.map(s => s.action), ["preview", "create"]);
  for (const s of f.w.setups) { assert.equal(s.task, TASK); assert.deepEqual(s.bounds, DEFAULT_BOUNDS); assert.equal(s.threadId, "thr_new"); }
  const o = await f.outcome();
  assert.equal(o.state, "ready"); assert.match(o.loop!, /^example-app-[a-z0-9]{4}$/); assert.deepEqual(o.bounds, DEFAULT_BOUNDS);
  assert.equal(o.startedAutomatically, false); assert.match(o.cadence!, /Checks every 5m0s.*at most 3 times/);
  assert.ok(!JSON.stringify(o).includes("Fictional"), "the task never appears in what the browser is told");
  assert.equal(o.configPath, undefined, "a path is shown only when something may have been left behind");
  assert.equal(f.c.held().taskBytes, 0, "the task is dropped once setup has been asked to create the loop");
  // Later lifecycle events and status reads repeat nothing.
  await f.c.advance("thr_new"); await f.outcome();
  assert.equal(f.w.setups.length, 2);
});

test("the loop name comes from the project and a random suffix, never from the message or a title", async () => {
  assert.equal(loopName("/projects/Example App", "K3F9-0000"), "example-app-k3f9");
  assert.equal(loopName("C:\\work\\café 项目", "ab"), "cafe-ab");
  assert.equal(loopName("/", "!!"), "loop-0000");
  assert.match(loopName("/p/" + "x".repeat(200), "abcd1234"), /^x{48}-abcd$/);
  assert.equal(slug("Release notes for 2.4!"), "release-notes-for-2-4"); assert.equal(slug("日本語"), ""); assert.equal(slug(null), "");
  assert.equal(durationMs("24h0m0s"), durationMs("24h")); assert.equal(durationMs("1h30m"), 5_400_000); assert.equal(durationMs("soon"), null); assert.equal(durationMs(""), null);
  const f = fixture();
  f.c.dispatched(f.dispatch(await f.arm(), { text: "Secret launch plan for Project Nightjar" })); await f.c.advance("thr_new");
  const name = f.w.setups[0].name!;
  assert.match(name, /^example-app-[a-z0-9]{4}$/);
  for (const word of ["secret", "launch", "nightjar"]) assert.ok(!name.includes(word));
});

test("automatic start needs the mode shown at the press, the setting still on, and a start that was confirmed", async () => {
  const auto = fixture(); auto.w.mode = "automatic";
  auto.c.dispatched(auto.dispatch(await auto.arm())); await auto.c.advance("thr_new");
  assert.deepEqual(auto.w.setups.map(s => s.action), ["preview", "create", "start"]);
  assert.equal((await auto.outcome()).startedAutomatically, true);

  const turnedOff = fixture(); turnedOff.w.mode = "automatic";
  const intent = await turnedOff.arm(); turnedOff.w.mode = "manual";
  turnedOff.c.dispatched(turnedOff.dispatch(intent)); await turnedOff.c.advance("thr_new");
  assert.ok(!turnedOff.w.setups.some(s => s.action === "start"));

  // The banner showed review-first; the setting became automatic before the press reached the server.
  const shown = fixture(); shown.w.mode = "automatic";
  const reviewFirst = await shown.c.call({ action: "arm", draftKey: "draft-a", projectId: "proj_a", bounds: DEFAULT_BOUNDS, shownMode: "manual" });
  assert.equal(reviewFirst.mode, "manual");
  shown.c.dispatched(shown.dispatch(reviewFirst.intent!)); await shown.c.advance("thr_new");
  assert.ok(!shown.w.setups.some(s => s.action === "start"), "what the person was shown is the most that happens");
  assert.equal(stricter("automatic", "automatic"), "automatic");
  for (const [a, b] of [["manual", "automatic"], ["automatic", "manual"], ["manual", "manual"]] as const) assert.equal(stricter(a, b), "manual");

  const cannot = fixture(); cannot.w.mode = "automatic"; cannot.w.canStart = false;
  cannot.c.dispatched(cannot.dispatch(await cannot.arm())); await cannot.c.advance("thr_new");
  assert.ok(!cannot.w.setups.some(s => s.action === "start"));
  assert.match((await cannot.outcome()).message, /left switched off: Review the diagnostics/);

  for (const how of ["declined", "failed"] as const) {
    const f = fixture(); f.w.mode = "automatic"; f.w.start = how;
    f.c.dispatched(f.dispatch(await f.arm())); await f.c.advance("thr_new");
    const o = await f.outcome();
    assert.equal(o.state, "ready"); assert.equal(o.startedAutomatically, false, `${how}: an unconfirmed start is never reported as started`);
    assert.match(o.message, how === "declined" ? /not started automatically: Existing holds/ : /could not be confirmed, and it was not tried again/);
    await f.c.advance("thr_new"); await f.outcome();
    assert.equal(f.w.setups.filter(s => s.action === "start").length, 1, `${how}: a start is never retried`);
  }
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

test("pressing again for the same draft replaces the earlier request, and at most sixteen requests wait", async () => {
  const f = fixture();
  const first = await f.arm("draft-a"); const second = await f.arm("draft-a");
  f.c.dispatched(f.dispatch(first, { threadId: "thr_old" })); await f.c.advance("thr_old");
  assert.equal(f.w.setups.length, 0); assert.equal((await f.outcome("thr_old")).state, "failed");
  f.c.dispatched(f.dispatch(second)); await f.c.advance("thr_new");
  assert.equal(f.w.setups.length, 2);

  const many = fixture();
  for (let i = 0; i < MAX_INTENTS; i++) await many.arm(`draft-${i}`);
  const refused = await many.c.call({ action: "arm", draftKey: "draft-extra", projectId: "proj_a", bounds: DEFAULT_BOUNDS, shownMode: "manual" });
  assert.equal(refused.ok, false); assert.equal(refused.intent, undefined); assert.match(refused.message, /Too many/);
  assert.equal(many.c.held().requests, MAX_INTENTS);
  many.w.now += INTENT_TTL_MS + 1;
  await many.arm("draft-extra");
  assert.equal(many.c.held().requests, 1, "expired requests are dropped");
});

test("a send BB did not deliver never leads to supervision", async () => {
  // Withdrawn before BB reported a thread.
  const before = fixture();
  const early = await before.arm();
  await before.c.call({ action: "cancel", intent: early });
  before.c.dispatched(before.dispatch(early)); await before.c.advance("thr_new");
  assert.equal(before.w.setups.length, 0); assert.equal((await before.outcome()).state, "failed");

  // Withdrawn after the hook ran: BB rejected the send afterwards.
  const after = fixture(); after.w.mode = "automatic";
  const late = await after.arm();
  after.c.dispatched(after.dispatch(late));
  await after.c.call({ action: "cancel", intent: late });
  assert.equal(after.c.held().taskBytes, 0);
  await after.c.advance("thr_new"); const o = await after.outcome();
  assert.equal(after.w.setups.length, 0, "no setup and no start after a withdrawn send");
  assert.equal(o.state, "failed"); assert.match(o.message, /may not have been sent/);

  // Withdrawn while the preview was running: nothing is created.
  const during = fixture(); during.w.mode = "automatic";
  const mid = await during.arm();
  during.c.dispatched(during.dispatch(mid));
  during.w.during = action => { if (action === "preview") during.c.forget("thr_new"); };
  await during.c.advance("thr_new");
  assert.deepEqual(during.w.setups.map(s => s.action), ["preview"]);

  // The thread never starts its first turn: the request expires with its task.
  const never = fixture(); never.w.ready = null;
  never.c.dispatched(never.dispatch(await never.arm()));
  await never.c.advance("thr_new"); assert.equal((await never.outcome()).state, "waiting");
  never.w.now += JOB_WAIT_MS + 1; never.w.ready = { project: "/projects/late" };
  const expired = await never.outcome();
  assert.equal(expired.state, "failed"); assert.match(expired.message, /did not start in time/);
  assert.equal(never.w.setups.length, 0); assert.equal(never.c.held().taskBytes, 0);

  // A different message starts the thread's first turn instead.
  const other = fixture();
  other.c.dispatched(other.dispatch(await other.arm()));
  other.c.dispatched(other.plain());
  await other.c.advance("thr_new");
  assert.equal(other.w.setups.length, 0); assert.equal((await other.outcome()).state, "failed"); assert.equal(other.c.held().taskBytes, 0);
  // A message joining the running first turn, or a drained re-attempt, does not end the request.
  for (const over of [{ attempt: "join-turn" }, { queued: 1 }]) {
    const f = fixture();
    f.c.dispatched(f.dispatch(await f.arm())); f.c.dispatched(f.plain(over));
    await f.c.advance("thr_new");
    assert.equal((await f.outcome()).state, "ready", JSON.stringify(over));
  }
  // A plain message on a thread with no request records nothing.
  const none = fixture(); none.c.dispatched(none.plain());
  assert.deepEqual(none.c.held(), { requests: 0, threads: 0, taskBytes: 0 });
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
    ["a thread that existed before the button was pressed", { threadCreatedAt: 999_999 }],
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
  // A malformed marker records nothing at all.
  const f = fixture(); await f.arm();
  for (const data of ["k1f9-example", { v: 2, intent: "k1f9-example" }, { v: 1, intent: "k1f9-example", extra: true }, { v: 1 }]) {
    f.c.dispatched(f.dispatch("k1f9-example", { data })); await f.c.advance("thr_new");
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

test("setup waits for the first turn, one pass at a time, and a deleted thread forgets the request", async () => {
  const f = fixture(); f.w.ready = null;
  f.c.dispatched(f.dispatch(await f.arm()));
  await f.c.advance("thr_new");
  assert.equal(f.w.setups.length, 0); assert.equal((await f.outcome()).state, "waiting");
  f.w.ready = { project: "/projects/Example App" };
  // BB reports the turn starting and the thread view asks at the same moment.
  let running = 0, overlapped = false;
  f.w.during = async () => { running++; overlapped ||= running > 1; await new Promise(r => setTimeout(r, 5)); running--; };
  await Promise.all([f.c.advance("thr_new"), f.c.advance("thr_new"), f.outcome(), f.c.advance("thr_new")]);
  assert.deepEqual(f.w.setups.map(s => s.action), ["preview", "create"], "concurrent passes create one setup");
  assert.equal(overlapped, false);

  const never = fixture(); never.w.ready = { reason: "This thread does not work in a Git repository." };
  never.c.dispatched(never.dispatch(await never.arm())); await never.c.advance("thr_new");
  assert.equal((await never.outcome()).message, "This thread does not work in a Git repository."); assert.equal(never.w.setups.length, 0);

  const gone = fixture(); gone.w.ready = null;
  gone.c.dispatched(gone.dispatch(await gone.arm())); gone.c.forget("thr_new");
  assert.equal(gone.c.held().taskBytes, 0);
  gone.w.ready = { project: "/projects/x" }; await gone.c.advance("thr_new");
  assert.equal(gone.w.setups.length, 0); assert.equal((await gone.outcome()).state, "none");
});

test("an existing loop, an unverified status, a missing program and a full plugin are explained, never silent", async () => {
  const supervised = fixture(); supervised.w.supervision = "supervised";
  supervised.c.dispatched(supervised.dispatch(await supervised.arm())); await supervised.c.advance("thr_new");
  assert.equal(supervised.w.setups.length, 0); assert.match((await supervised.outcome()).message, /already supervises this thread/);

  const unverified = fixture(); unverified.w.supervision = "unverified";
  unverified.c.dispatched(unverified.dispatch(await unverified.arm())); await unverified.c.advance("thr_new");
  assert.equal(unverified.w.setups.length, 0); assert.match((await unverified.outcome()).message, /could not check/);

  const missing = fixture(); missing.w.unavailable = "The keepgoing program was not found.";
  for (const action of ["check", "arm"] as const) {
    const r = await missing.c.call(action === "check" ? { action } : { action, draftKey: "d", projectId: "p", bounds: DEFAULT_BOUNDS, shownMode: "manual" });
    assert.equal(r.ok, false); assert.equal(r.message, "The keepgoing program was not found."); assert.equal(r.intent, undefined);
  }

  // The limit is checked before the send is armed, and says the count.
  const full = fixture(); full.w.used = 32;
  for (const action of ["check", "arm"] as const) {
    const r = await full.c.call(action === "check" ? { action } : { action, draftKey: "d", projectId: "p", bounds: DEFAULT_BOUNDS, shownMode: "manual" });
    assert.equal(r.ok, false); assert.match(r.message, /already holds 32 of 32 setups/); assert.deepEqual(r.setups, { used: 32, max: 32 }); assert.equal(r.intent, undefined);
  }
  // It filled up between the press and the thread being ready.
  const filled = fixture();
  filled.c.dispatched(filled.dispatch(await filled.arm())); filled.w.used = 32;
  await filled.c.advance("thr_new");
  assert.equal(filled.w.setups.length, 0); assert.match((await filled.outcome()).message, /32 of 32 setups/);

  const empty = fixture();
  empty.c.dispatched(empty.dispatch(await empty.arm(), { text: "   " })); await empty.c.advance("thr_new");
  assert.equal(empty.w.setups.length, 0); assert.match((await empty.outcome()).message, /no text keepgoing can use/);

  const noPreview = fixture(); noPreview.w.previewOk = false;
  noPreview.c.dispatched(noPreview.dispatch(await noPreview.arm())); await noPreview.c.advance("thr_new");
  const p = await noPreview.outcome();
  assert.equal(p.state, "failed"); assert.equal(p.configPath, undefined, "nothing was asked to be created, so there is nothing to look for");
});

test("a creation that could not be confirmed names what may exist and where, and is never retried", async () => {
  const failed = fixture(); failed.w.createOk = false;
  failed.c.dispatched(failed.dispatch(await failed.arm())); await failed.c.advance("thr_new");
  const o = await failed.outcome();
  assert.equal(o.state, "failed");
  assert.match(o.loop!, /^example-app-[a-z0-9]{4}$/); assert.equal(o.configPath, `/private/${o.loop}/keepgoing.yaml`);
  assert.match(o.message, new RegExp(`A switched-off setup named ${o.loop} may exist`)); assert.match(o.message, /was not started/); assert.match(o.message, /delete its directory/);
  assert.ok(!JSON.stringify(o).includes("Fictional"));
  await failed.c.advance("thr_new"); await failed.outcome();
  assert.equal(failed.w.setups.filter(s => s.action === "create").length, 1);

  const threw = fixture();
  threw.w.during = action => { if (action === "create") throw new Error("PRIVATE HOST ERROR /secret/path"); };
  threw.c.dispatched(threw.dispatch(await threw.arm())); await threw.c.advance("thr_new");
  const t = await threw.outcome();
  assert.equal(t.state, "failed"); assert.ok(t.configPath); assert.ok(!JSON.stringify(t).includes("PRIVATE")); assert.equal(threw.c.held().taskBytes, 0);
});

test("keepgoing's own refusal reaches the thread in its words, with the other loop's name", async () => {
  const atPreview = fixture();
  const setup = atPreview.d.setup;
  atPreview.d.setup = async (input, bounds) => input.action === "preview"
    ? (atPreview.w.setups.push({ action: "preview", threadId: input.threadId, name: input.name, task: input.task, bounds }), { ok: false, refused: { code: "second_loop_on_session", otherLoop: "other-loop" }, message: "keepgoing refused to set up this loop: loop other-loop already supervises this thread's session." })
    : setup(input, bounds);
  atPreview.c.dispatched(atPreview.dispatch(await atPreview.arm())); await atPreview.c.advance("thr_new");
  const o = await atPreview.outcome();
  assert.equal(o.state, "failed"); assert.equal(o.message, "keepgoing refused to set up this loop: loop other-loop already supervises this thread's session.");
  assert.deepEqual(atPreview.w.setups.map(s => s.action), ["preview"]); assert.equal(o.configPath, undefined);

  const atStart = fixture(); atStart.w.mode = "automatic";
  const setup2 = atStart.d.setup;
  atStart.d.setup = async (input, bounds) => input.action === "start"
    ? (atStart.w.setups.push({ action: "start", threadId: input.threadId, bounds }), { ok: false, started: false, refused: { code: "second_loop_on_session", otherLoop: "other-loop" }, message: "keepgoing refused to start this loop: loop other-loop already supervises this thread's session. Nothing was started." })
    : setup2(input, bounds);
  atStart.c.dispatched(atStart.dispatch(await atStart.arm())); await atStart.c.advance("thr_new");
  const s = await atStart.outcome();
  assert.equal(s.state, "ready"); assert.equal(s.startedAutomatically, false);
  assert.match(s.message, /left switched off\. keepgoing refused to start this loop: loop other-loop/);
  await atStart.c.advance("thr_new"); await atStart.outcome();
  assert.equal(atStart.w.setups.filter(x => x.action === "start").length, 1, "never retried");
});

test("bounds outside the closed list are refused before anything is recorded", async () => {
  const f = fixture();
  for (const bounds of [{ maxSends: 0, noProgressTimeout: "24h" }, { maxSends: 5000, noProgressTimeout: "24h" }, { maxSends: 40, noProgressTimeout: "0" }, { maxSends: 40 }]) {
    const r = await f.c.call({ action: "arm", draftKey: "d", projectId: "p", bounds, shownMode: "manual" } as never);
    assert.equal(r.ok, false); assert.equal(r.intent, undefined);
  }
  const noMode = await f.c.call({ action: "arm", draftKey: "d", projectId: "p", bounds: DEFAULT_BOUNDS } as never);
  assert.equal(noMode.ok, false, "a request that does not say what the person was shown is refused");
  assert.equal(f.c.held().requests, 0);
});

test("the limits a thread shows are the ones keepgoing's plan reports, and a plan without the asked-for limits is refused", async () => {
  for (const [name, reported] of [
    ["no limits at all", () => ({})],
    ["a send limit only", (b: { maxSends: number }) => ({ maxSends: b.maxSends })],
    ["a different send limit", () => ({ maxSends: 400, noProgressTimeout: "24h0m0s" })],
    ["a different timeout", (b: { maxSends: number }) => ({ maxSends: b.maxSends, noProgressTimeout: "2h0m0s" })],
    ["a timeout the control does not offer", (b: { maxSends: number }) => ({ maxSends: b.maxSends, noProgressTimeout: "24h0m1s" })],
  ] as const) {
    const f = fixture(); f.w.mode = "automatic"; f.w.planLimits = reported as never;
    f.c.dispatched(f.dispatch(await f.arm())); await f.c.advance("thr_new");
    const o = await f.outcome();
    assert.equal(o.state, "failed", name); assert.match(o.message, /did not report the loop-wide limits/, name);
    assert.deepEqual(f.w.setups.map(s => s.action), ["preview"], `${name}: nothing created or started`);
    assert.equal(o.bounds, undefined, `${name}: no limits are claimed`);
  }
  // Equal limits printed another way are the same limits, and the thread shows them.
  const f = fixture(); f.w.planLimits = (b) => ({ maxSends: b.maxSends, noProgressTimeout: "1440m" });
  f.c.dispatched(f.dispatch(await f.arm())); await f.c.advance("thr_new");
  const o = await f.outcome();
  assert.equal(o.state, "ready"); assert.deepEqual(o.bounds, DEFAULT_BOUNDS);
});

test("an unmarked message ends the request only while the thread is still pending", async () => {
  // A: the first turn started (the marked message was delivered), then a quick
  // follow-up started the next turn before setup ran: setup still runs.
  const a = fixture(); a.w.ready = null;
  a.c.dispatched(a.dispatch(await a.arm()));
  a.c.dispatched(a.plain({ threadStatus: "idle" }));
  a.c.dispatched(a.plain({ threadStatus: "active", attempt: "join-turn" }));
  a.w.ready = { project: "/projects/Example App" };
  await a.c.advance("thr_new");
  assert.equal((await a.outcome()).state, "ready"); assert.deepEqual(a.w.setups.map(s => s.action), ["preview", "create"]);

  // B: a message that does end the request while setup is running stops the
  // pass at its next step: nothing is created, or what was created is not started.
  for (const at of ["preview", "create"] as const) {
    const b = fixture(); b.w.mode = "automatic";
    b.c.dispatched(b.dispatch(await b.arm()));
    b.w.during = action => { if (action === at) b.c.dispatched(b.plain()); };
    await b.c.advance("thr_new");
    const o = await b.outcome();
    assert.ok(!b.w.setups.some(s => s.action === "start"), `${at}: never started`);
    if (at === "preview") { assert.equal(o.state, "failed"); assert.ok(!b.w.setups.some(s => s.action === "create")); }
    else { assert.equal(o.state, "ready"); assert.equal(o.startedAutomatically, false); assert.match(o.message, /left switched off, because the request was withdrawn/); }
  }
  // The same for a send the browser withdraws between creation and start.
  const c = fixture(); c.w.mode = "automatic";
  const intent = await c.arm();
  c.c.dispatched(c.dispatch(intent));
  c.w.during = async action => { if (action === "create") await c.c.call({ action: "cancel", intent }); };
  await c.c.advance("thr_new");
  assert.ok(!c.w.setups.some(s => s.action === "start"));
  assert.match((await c.outcome()).message, /may not have been sent/);
});

test("a refusal at creation shows keepgoing's words only, with no path and no clean-up advice", async () => {
  const f = fixture();
  const setup = f.d.setup;
  f.d.setup = async (input, bounds) => input.action === "create"
    ? (f.w.setups.push({ action: "create", threadId: input.threadId, bounds }), { ok: false, refused: { code: "second_loop_on_session", otherLoop: "other-loop" }, message: "keepgoing refused to set up this loop: loop other-loop already supervises this thread's session, and two loops on one session would both prompt it. Nothing was created." })
    : setup(input, bounds);
  f.c.dispatched(f.dispatch(await f.arm())); await f.c.advance("thr_new");
  const o = await f.outcome();
  assert.equal(o.state, "failed");
  assert.equal(o.message, "keepgoing refused to set up this loop: loop other-loop already supervises this thread's session, and two loops on one session would both prompt it. Nothing was created.");
  assert.equal(o.configPath, undefined); assert.ok(!/may exist|delete its directory/.test(o.message));
});

test("reading one thread's outcome does not wait for another thread's setup", async () => {
  const f = fixture();
  let release!: () => void;
  const held = new Promise<void>(r => { release = r; });
  f.w.during = async action => { if (action === "preview") await held; };
  f.c.dispatched(f.dispatch(await f.arm("draft-a"), { threadId: "thr_slow" }));
  void f.c.advance("thr_slow");
  await new Promise(r => setTimeout(r, 5));
  const started = Date.now();
  const r = await f.c.call({ action: "outcome", threadId: "thr_other" });
  assert.equal(r.outcome?.state, "none"); assert.ok(Date.now() - started < 50, "the read did not wait");
  const slow = await f.c.call({ action: "outcome", threadId: "thr_slow" });
  assert.equal(slow.outcome?.state, "waiting", "a thread's own read answers at once too");
  release(); await f.c.whenIdle();
  assert.equal((await f.outcome("thr_slow")).state, "ready");
});
