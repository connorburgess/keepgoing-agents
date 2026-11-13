import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "https://example.com" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });
const { fireEvent, cleanup, waitFor, act } = await import("@testing-library/react");
const { loadPluginApp, renderSlot } = await import("@get-bb/plugin-sdk/testing/app");
const { resetDrafts } = await import("./new-thread-control.tsx");
const { DEFAULT_BOUNDS, boundsPhrase } = await import("./new-thread-contract.ts");
const app = await loadPluginApp(() => import("./app.tsx"));

test("the plugin adds its control to the New thread composer only", () => {
  const registrations = app.composerCustomizations;
  assert.equal(registrations.length, 1);
  const c = registrations[0]!;
  assert.deepEqual(c.scopes, ["new-thread"]);
  assert.deepEqual(c.actions?.map(a => a.id), ["keep-going"]);
  assert.deepEqual(c.banners?.map(b => [b.id, b.chrome]), [["keep-going", "bare"]]);
  assert.deepEqual(c.sendMenu?.map(m => m.label), ["Keep going…"]);
  assert.equal(c.plusMenu, undefined);
});

test("in BB's composer the switch opens the banner and its send carries the request", async () => {
  const c = app.composerCustomizations[0]!;
  const calls: { action: string }[] = [];
  const rpc = { newThread: (raw: unknown) => { const input = raw as { action: string }; calls.push(input); return input.action === "arm" ? { ok: true, message: "", mode: "automatic", intent: "intent-example", expiresAt: 1 } : { ok: true, message: "", mode: "automatic" }; } };
  const composer = { text: "Fictional task", scope: { kind: "new-thread" as const, projectId: "proj_example" } };
  const banner = renderSlot({ component: c.banners![0]!.component }, {}, { rpc, composer });
  try {
    assert.equal(banner.container.textContent, "", "nothing is drawn while the switch is off");
    // The menu row beside Send reaches the same switch, including in the compact layout.
    await act(async () => { await c.sendMenu![0]!.run({ composer: { scope: banner.inspection.composer.scope, key: banner.inspection.composer.key } as never }); });
    await banner.findByText("keepgoing will supervise the thread this message creates, starting once the thread exists.");
    assert.ok(banner.getByText(boundsPhrase(DEFAULT_BOUNDS)));
    fireEvent.click(banner.getByRole("button", { name: "Send and keep going" }));
    await waitFor(() => assert.equal(banner.inspection.composer.submits.length, 1));
    assert.deepEqual(banner.inspection.composer.submits[0], { experimental_data: { v: 1, intent: "intent-example" } });
    assert.deepEqual(calls.map(x => x.action), ["check", "arm"]);
    assert.ok(!JSON.stringify(calls).includes("Fictional task"));
  } finally { banner.lifecycle.unmount(); cleanup(); resetDrafts(); }
  const action = renderSlot({ component: c.actions![0]!.component }, {}, { rpc, composer });
  try {
    const toggle = action.getByRole("switch", { name: "Keep going" });
    fireEvent.click(toggle); assert.equal(toggle.getAttribute("aria-checked"), "true");
    fireEvent.click(toggle); assert.equal(toggle.getAttribute("aria-checked"), "false");
  } finally { action.lifecycle.unmount(); cleanup(); resetDrafts(); }
});

const loop = { loop: "release-notes-for-2-4", configPath: "/private/release-notes/keepgoing.yaml", managedId: "record-example", managedThreadId: "thr_created", controlState: "disabled", note: null, target: "primary", supervising: true, lastState: null, lastWakeAt: null, checkedAt: null, handoffs: 0, fallbacks: null };
const ready = { state: "ready", message: "Created from this thread's first message. It is switched off until you start it.", loop: loop.loop, bounds: DEFAULT_BOUNDS, cadence: "Checks every 5m0s.", startedAutomatically: false };

test("the created thread's card shows the bounded, switched-off loop and one button that starts it", async () => {
  const starts: unknown[] = [];
  const ui = renderSlot(app.threadContextCardRows[0]!, { threadId: "thr_created", projectId: "proj_example", environment: null }, { rpc: {
    forThread: () => ({ loops: [loop], errors: [] }),
    newThread: () => ({ ok: true, message: "", outcome: ready }),
    setup: (input: unknown) => { starts.push(input); return (input as { action: string }).action === "start" ? { ok: true, message: "Background supervision started. Existing timers were not changed." } : { ok: true, message: "Review the diagnostics, then start background supervision when ready.", record: { id: "record-example", loop: loop.loop, configPath: loop.configPath, threadId: "thr_created", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: "/projects/example", provider: "example-provider" }, canStart: true, checks: [] }; },
  } });
  try {
    await waitFor(() => assert.equal(ui.getAllByText("keepgoing: ready, not started").length, 2), { timeout: 3000 });
    assert.ok(ui.getByText(boundsPhrase(DEFAULT_BOUNDS)));
    // Opening the setup panel adds diagnostics, not a second way to start.
    fireEvent.click(ui.getByRole("button", { name: "Setup" }));
    await ui.findByRole("button", { name: "Refresh diagnostics" });
    assert.equal(ui.queryByRole("button", { name: "Start background supervision" }), null);
    assert.equal(ui.getAllByRole("button", { name: /^Start/ }).length, 1);
    fireEvent.click(ui.getByRole("button", { name: "Start keeping this going" }));
    await waitFor(() => assert.deepEqual(starts.filter(s => (s as { action: string }).action === "start"), [{ action: "start", threadId: "thr_created" }]));
  } finally { ui.lifecycle.unmount(); cleanup(); }
});

test("a started thread says it is running, with one Pause and one Stop that go to the loop's own configuration", async () => {
  const controls: unknown[] = [];
  // The plugin caches status per thread, so this thread has its own id.
  const running = { ...loop, controlState: "enabled", managedThreadId: "thr_running" };
  const ui = renderSlot(app.threadContextCardRows[0]!, { threadId: "thr_running", projectId: "proj_example", environment: null }, { rpc: {
    forThread: () => ({ loops: [running], errors: [] }),
    newThread: () => ({ ok: true, message: "", outcome: { ...ready, message: "Started when this thread was created.", startedAutomatically: true } }),
    control: (input: unknown) => { controls.push(input); return { ok: true, message: "stop done" }; },
  } });
  try {
    await waitFor(() => assert.equal(ui.getAllByText("keepgoing: keeping this going").length, 2), { timeout: 3000 });
    assert.ok(ui.getByText(/Started when this thread was created\./)); assert.ok(ui.getByText(boundsPhrase(DEFAULT_BOUNDS)));
    assert.equal(ui.getAllByRole("button", { name: "Pause" }).length, 1); assert.equal(ui.getAllByRole("button", { name: "Stop" }).length, 1);
    assert.equal(ui.queryByRole("button", { name: /^Start/ }), null);
    fireEvent.click(ui.getByRole("button", { name: "Stop" }));
    await waitFor(() => assert.deepEqual(controls, [{ configPath: loop.configPath, action: "stop", managedId: "record-example", expectedThreadId: "thr_running" }]));
  } finally { ui.lifecycle.unmount(); cleanup(); }
});

test("the banner summary names setup that is under way or could not go ahead; an ordinary thread is unchanged", async () => {
  const segment = app.threadContextSegments[0]!;
  const props = (threadId: string) => ({ threadId, projectId: "proj_example", environment: null, segments: [], isExpanded: true, onToggle() {} });
  const waiting = renderSlot({ component: segment.summary }, props("thr_waiting"), { rpc: { forThread: () => ({ loops: [], errors: [] }), newThread: () => ({ ok: true, message: "", outcome: { state: "waiting", message: "Waiting for the environment." } }) } });
  try { await waiting.findByRole("button", { name: "keepgoing: setting up" }); } finally { waiting.lifecycle.unmount(); cleanup(); }
  const failed = renderSlot({ component: segment.details! }, props("thr_failed"), { rpc: { forThread: () => ({ loops: [], errors: [] }), setup: () => ({ ok: true, message: "Enter a task and preview a new loop for this thread." }), newThread: () => ({ ok: true, message: "", outcome: { state: "failed", message: "Another loop already supervises this thread. Nothing was created or started." } }) } });
  try {
    assert.equal((await failed.findByText("Another loop already supervises this thread. Nothing was created or started.")).getAttribute("role"), "alert");
    // Nothing was attached, so the ordinary setup offer is still there.
    assert.ok(await failed.findByRole("button", { name: "Preview setup" }));
  } finally { failed.lifecycle.unmount(); cleanup(); }
  const ordinary = renderSlot({ component: segment.summary }, props("thr_ordinary"), { rpc: { forThread: () => ({ loops: [], errors: [] }), newThread: () => ({ ok: true, message: "", outcome: { state: "none", message: "" } }) } });
  try { await ordinary.findByRole("button", { name: "Supervise this thread" }); } finally { ordinary.lifecycle.unmount(); cleanup(); }
});
