import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import type { NewThreadInput, NewThreadResult } from "./new-thread-contract.ts";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "https://example.com" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });
const { render, fireEvent, waitFor, cleanup } = await import("@testing-library/react");
const { KeepGoingBanner, KeepGoingSwitch, resetDrafts, turnOn } = await import("./new-thread-control.tsx");
const { NewThreadOutcome, outcomeLabel } = await import("./new-thread-outcome.tsx");
const { DEFAULT_BOUNDS, boundsPhrase } = await import("./new-thread-contract.ts");

function world(over: { mode?: "manual" | "automatic"; unavailable?: string; armFails?: string; submitFails?: string; blocked?: string; projectId?: string | null } = {}) {
  const calls: NewThreadInput[] = [];
  const submits: unknown[] = [];
  const state = { ...over };
  const call = async (input: NewThreadInput): Promise<NewThreadResult> => {
    calls.push(input);
    if (input.action === "check") return state.unavailable ? { ok: false, message: state.unavailable } : { ok: true, message: "", mode: state.mode ?? "manual" };
    if (input.action === "arm") return state.armFails ? { ok: false, message: state.armFails } : { ok: true, message: "", mode: state.mode ?? "manual", intent: `intent-${calls.length}`, expiresAt: 1 };
    return { ok: true, message: "" };
  };
  const composer = {
    scope: { kind: "new-thread", projectId: over.projectId === undefined ? "proj_example" : over.projectId }, key: "draft-example",
    isSubmittingBlocked: !!over.blocked, submittingBlockedReason: over.blocked ?? null,
    submit: async (options?: unknown) => { if (state.submitFails) throw new Error(state.submitFails); submits.push(options); },
  };
  const ui = render(<><KeepGoingBanner composer={composer} call={call} /><KeepGoingSwitch composer={composer} /></>);
  const done = () => { ui.unmount(); cleanup(); resetDrafts(); };
  return { ui, calls, submits, state, done };
}

test("off shows only the switch; on explains once, states the bound and the plain-Enter rule, and keeps numbers behind Details", async () => {
  const w = world();
  try {
    const toggle = w.ui.getByRole("switch", { name: "Keep going" });
    assert.equal(toggle.getAttribute("aria-checked"), "false");
    assert.equal(w.ui.queryByLabelText("Keep going"), null); assert.equal(w.calls.length, 0);
    fireEvent.click(toggle);
    await w.ui.findByText(/keepgoing will supervise the thread this message creates\. It is created switched off; you start it from the thread\./);
    assert.equal(toggle.getAttribute("aria-checked"), "true");
    assert.ok(w.ui.getByText(boundsPhrase(DEFAULT_BOUNDS)));
    assert.ok(w.ui.getByText("A plain Enter sends without supervision."));
    assert.equal(w.ui.queryAllByText(/Keep going is on/).length, 0, "the banner does not repeat the switch");
    assert.equal(w.ui.container.querySelector("details")?.open, false);
    assert.deepEqual(w.calls.map(c => c.action), ["check"]); assert.equal(w.submits.length, 0);
    fireEvent.click(w.ui.getByRole("button", { name: "Turn off" }));
    assert.equal(w.ui.queryByLabelText("Keep going"), null); assert.equal(toggle.getAttribute("aria-checked"), "false");
  } finally { w.done(); }
});

test("Send and keep going asks for a single-use request, then sends the message with it and turns off", async () => {
  const w = world();
  try {
    fireEvent.click(w.ui.getByRole("switch"));
    fireEvent.click(await w.ui.findByRole("button", { name: "Send and keep going" }));
    await waitFor(() => assert.equal(w.submits.length, 1));
    assert.deepEqual(w.calls[1], { action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: DEFAULT_BOUNDS });
    assert.deepEqual(w.submits[0], { experimental_data: { v: 1, intent: "intent-2" } });
    await waitFor(() => assert.equal(w.ui.getByRole("switch").getAttribute("aria-checked"), "false"));
    assert.ok(!JSON.stringify(w.calls).includes("task"), "no message text goes through the plugin's own requests");
  } finally { w.done(); }
});

test("the bound is edited in Details and travels with the request", async () => {
  const w = world();
  try {
    fireEvent.click(w.ui.getByRole("switch"));
    fireEvent.change(await w.ui.findByLabelText("Automatic sends before stopping"), { target: { value: "12" } });
    fireEvent.change(w.ui.getByLabelText("Time without a Git change before stopping"), { target: { value: "6h" } });
    assert.ok(w.ui.getByText("Stops for you after 12 automatic sends, or 6 hours without a Git change."));
    fireEvent.click(w.ui.getByRole("button", { name: "Send and keep going" }));
    await waitFor(() => assert.equal(w.submits.length, 1));
    assert.deepEqual(w.calls.find(c => c.action === "arm"), { action: "arm", draftKey: "draft-example", projectId: "proj_example", bounds: { maxSends: 12, noProgressTimeout: "6h" } });
  } finally { w.done(); }
});

test("a bound outside 1 to 1000 is refused in the control and nothing is sent", async () => {
  const w = world();
  try {
    fireEvent.click(w.ui.getByRole("switch"));
    fireEvent.change(await w.ui.findByLabelText("Automatic sends before stopping"), { target: { value: "0" } });
    fireEvent.click(w.ui.getByRole("button", { name: "Send and keep going" }));
    assert.match((await w.ui.findByRole("alert")).textContent ?? "", /1 to 1000/);
    assert.ok(!w.calls.some(c => c.action === "arm")); assert.equal(w.submits.length, 0);
  } finally { w.done(); }
});

test("automatic mode says in the control that supervision starts by itself", async () => {
  const w = world({ mode: "automatic" });
  try {
    fireEvent.click(w.ui.getByRole("switch"));
    await w.ui.findByText(/starts by itself once the thread exists/);
    assert.ok(w.ui.getByText(boundsPhrase(DEFAULT_BOUNDS)));
  } finally { w.done(); }
});

test("a missing program is explained, the switch stays off, and Check again retries", async () => {
  const w = world({ unavailable: "The keepgoing program was not found or did not run." });
  try {
    fireEvent.click(w.ui.getByRole("switch"));
    assert.match((await w.ui.findByRole("alert")).textContent ?? "", /was not found/);
    assert.equal(w.ui.getByRole("switch").getAttribute("aria-checked"), "false");
    assert.equal(w.ui.queryByRole("button", { name: "Send and keep going" }), null);
    w.state.unavailable = undefined;
    fireEvent.click(w.ui.getByRole("button", { name: "Check again" }));
    await w.ui.findByRole("button", { name: "Send and keep going" });
    assert.equal(w.ui.getByRole("switch").getAttribute("aria-checked"), "true");
  } finally { w.done(); }
});

test("a refused request sends nothing; a send that fails withdraws the request and says why", async () => {
  const refused = world({ armFails: "Too many Keep going requests are waiting." });
  try {
    fireEvent.click(refused.ui.getByRole("switch"));
    fireEvent.click(await refused.ui.findByRole("button", { name: "Send and keep going" }));
    assert.match((await refused.ui.findByRole("alert")).textContent ?? "", /Too many/);
    assert.equal(refused.submits.length, 0);
  } finally { refused.done(); }
  const failed = world({ submitFails: "Choose a model first." });
  try {
    fireEvent.click(failed.ui.getByRole("switch"));
    fireEvent.click(await failed.ui.findByRole("button", { name: "Send and keep going" }));
    assert.equal((await failed.ui.findByRole("alert")).textContent, "Choose a model first.");
    assert.deepEqual(failed.calls.map(c => c.action), ["check", "arm", "cancel"]);
    assert.deepEqual(failed.calls[2], { action: "cancel", intent: "intent-2" });
    assert.equal(failed.ui.getByRole("switch").getAttribute("aria-checked"), "true", "the person can correct the draft and send again");
  } finally { failed.done(); }
});

test("the send is disabled with BB's own reason while the composer cannot submit, and needs a project", async () => {
  const blocked = world({ blocked: "Type a message first." });
  try {
    fireEvent.click(blocked.ui.getByRole("switch"));
    const send = await blocked.ui.findByRole("button", { name: "Send and keep going" }) as HTMLButtonElement;
    assert.equal(send.disabled, true); assert.equal(send.title, "Type a message first.");
  } finally { blocked.done(); }
  const unresolved = world({ projectId: null });
  try {
    fireEvent.click(unresolved.ui.getByRole("switch"));
    fireEvent.click(await unresolved.ui.findByRole("button", { name: "Send and keep going" }));
    assert.equal((await unresolved.ui.findByRole("alert")).textContent, "Choose a project first.");
    assert.ok(!unresolved.calls.some(c => c.action === "arm")); assert.equal(unresolved.submits.length, 0);
  } finally { unresolved.done(); }
});

test("the row in the menu beside Send turns the same switch on; nothing shows outside the New thread composer", async () => {
  const w = world();
  try {
    const { act } = await import("@testing-library/react");
    await act(async () => { turnOn("draft-example"); });
    await w.ui.findByRole("button", { name: "Send and keep going" });
    assert.equal(w.ui.getByRole("switch").getAttribute("aria-checked"), "true");
  } finally { w.done(); }
  const thread = { scope: { kind: "thread" }, key: "thread-draft", isSubmittingBlocked: false, submittingBlockedReason: null, submit: async () => {} };
  turnOn("thread-draft");
  const ui = render(<><KeepGoingBanner composer={thread} call={async () => ({ ok: true, message: "", mode: "manual" as const })} /><KeepGoingSwitch composer={thread} /></>);
  try { assert.equal(ui.container.textContent, ""); } finally { ui.unmount(); cleanup(); resetDrafts(); }
});

const ready = { state: "ready" as const, message: "Created from this thread's first message. It is switched off until you start it.", loop: "release-notes-for-2-4", bounds: DEFAULT_BOUNDS, cadence: "Checks every 5m0s.", startedAutomatically: false };

test("a thread created with Keep going on shows one button that says what it starts, with the bound", async () => {
  const starts: unknown[] = []; let changed = 0;
  const ui = render(<NewThreadOutcome threadId="thr_example" outcome={ready} controlState="disabled" supervising={true}
    start={async input => { starts.push(input); return { ok: true, message: "Background supervision started. Existing timers were not changed." }; }} onChanged={() => changed++} />);
  try {
    assert.ok(ui.getByText("keepgoing: ready, not started"));
    assert.ok(ui.getByText(/Loop release-notes-for-2-4\. Created from this thread's first message/));
    assert.ok(ui.getByText(boundsPhrase(DEFAULT_BOUNDS)));
    assert.equal(ui.getAllByRole("button").length, 1);
    fireEvent.click(ui.getByRole("button", { name: "Start keeping this going" }));
    assert.match((await ui.findByRole("status")).textContent ?? "", /started/);
    assert.deepEqual(starts, [{ action: "start", threadId: "thr_example" }]); assert.equal(changed, 1);
  } finally { ui.unmount(); cleanup(); }
});

test("after it is started the same words are used and the button is gone; waiting and failure are stated", async () => {
  const start = async () => ({ ok: true, message: "" });
  const started = render(<NewThreadOutcome threadId="thr_example" outcome={{ ...ready, startedAutomatically: true }} controlState="enabled" supervising={true} start={start} onChanged={() => {}} />);
  try {
    assert.ok(started.getByText("keepgoing: keeping this going"));
    assert.ok(started.getByText(/Started automatically when this thread was created\./));
    assert.equal(started.queryByRole("button"), null);
  } finally { started.unmount(); cleanup(); }
  const waiting = render(<NewThreadOutcome threadId="thr_example" outcome={{ state: "waiting", message: "keepgoing is waiting for this thread's environment." }} controlState={undefined} supervising={undefined} start={start} onChanged={() => {}} />);
  try { assert.ok(waiting.getByText("keepgoing: setting up")); assert.match(waiting.getByRole("status").textContent ?? "", /waiting/); assert.equal(waiting.queryByRole("button"), null); }
  finally { waiting.unmount(); cleanup(); }
  const failed = render(<NewThreadOutcome threadId="thr_example" outcome={{ state: "failed", message: "Another loop already supervises this thread. Nothing was created or started." }} controlState={undefined} supervising={undefined} start={start} onChanged={() => {}} />);
  try { assert.match(failed.getByRole("alert").textContent ?? "", /already supervises/); assert.equal(failed.queryByRole("button"), null); }
  finally { failed.unmount(); cleanup(); }
  const none = render(<NewThreadOutcome threadId="thr_example" outcome={{ state: "none", message: "" }} controlState={undefined} supervising={undefined} start={start} onChanged={() => {}} />);
  try { assert.equal(none.container.textContent, ""); } finally { none.unmount(); cleanup(); }
  assert.equal(outcomeLabel({ state: "none", message: "" }, "enabled", true), null);
  assert.equal(outcomeLabel({ state: "failed", message: "x" }, undefined, undefined), "keepgoing: not set up");
  assert.equal(outcomeLabel(ready, "paused", true), null, "holds are reported by the ordinary status row");
});
