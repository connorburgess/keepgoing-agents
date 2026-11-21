import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "https://example.com" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });
const { fireEvent, cleanup, within, waitFor } = await import("@testing-library/react");
const { loadPluginApp, renderSlot } = await import("@get-bb/plugin-sdk/testing/app");
const app = await loadPluginApp(() => import("./app.tsx"));
const base = { note: null, target: "primary", supervising: true, lastState: null, lastWakeAt: "0001-01-01T00:00:00Z", checkedAt: null, handoffs: 0, fallbacks: null };
function fixture(name: string, state = "enabled") { return { ...base, loop: name, configPath: `/example/${name}/looptender.yaml`, managedId: `record-${name}`, controlState: state }; }
const retired = fixture("retired-example", "stopped");
const active = fixture("active-example");
const completed = fixture("completed-example", "completed");

test("card retains retired, active and completed matches; pending controls follow identity through reorder and removal", async () => {
  let loops = [retired, active, completed]; let failed = false;
  const calls: unknown[] = []; let resolve!: (value: { ok: boolean; message: string }) => void;
  const ui = renderSlot(app.threadContextCardRows[0]!, { threadId: "thr_example_matches", projectId: "proj_example", environment: null }, { rpc: {
    forThread: () => { if (failed) throw new Error("PRIVATE ERROR"); return { loops, errors: [] }; },
    control: (input: unknown) => { calls.push(input); return new Promise(r => { resolve = r; }); },
  } });
  try {
    const region = (name: string) => within(ui.getByRole("region", { name: `Loop ${name}` }));
    await ui.findByRole("region", { name: `Loop ${active.loop}` });
    assert.equal(ui.getAllByRole("region").length, 3);
    assert.ok(region(retired.loop).getByText(/ordinary terminal/));
    assert.ok(region(completed.loop).getByText("looptender: completed"));
    assert.equal(ui.queryByRole("button", { name: "Resume" }), null);
    fireEvent.click(region(active.loop).getByRole("button", { name: "Pause" }));
    assert.deepEqual(calls, [{ configPath: active.configPath, action: "pause", managedId: active.managedId, expectedThreadId: "thr_example_matches" }]);
    loops = [completed, retired, active]; await ui.behavior.emitRealtime("changed", {});
    assert.equal((region(active.loop).getByRole("button", { name: "Pause" }) as HTMLButtonElement).disabled, true);
    loops = [completed, active]; await ui.behavior.emitRealtime("changed", {});
    assert.equal(ui.queryByRole("region", { name: `Loop ${retired.loop}` }), null);
    assert.equal((region(active.loop).getByRole("button", { name: "Pause" }) as HTMLButtonElement).disabled, true);
    resolve({ ok: true, message: "Paused" });
    await waitFor(() => assert.equal((region(active.loop).getByRole("button", { name: "Pause" }) as HTMLButtonElement).disabled, false));
    loops = [completed]; await ui.behavior.emitRealtime("changed", {});
    assert.equal(ui.queryByRole("button", { name: "Pause" }), null);
    loops = [active, retired]; await ui.behavior.emitRealtime("changed", {});
    await ui.findByRole("button", { name: "Pause" });
    failed = true; await ui.behavior.emitRealtime("changed", {});
    await ui.findByRole("alert"); assert.equal(ui.queryByRole("button", { name: "Pause" }), null);
    assert.ok(!ui.container.textContent?.includes("PRIVATE")); assert.equal(calls.length, 1);
  } finally { ui.lifecycle.unmount(); cleanup(); }
});

test("banner counts all matches and expanded controls use each loop's configuration", async () => {
  const segment = app.threadContextSegments[0]!;
  const props = { threadId: "thr_example_banner_matches", projectId: "proj_example", environment: null, segments: [], isExpanded: true, onToggle() {} };
  const second = fixture("second-example"); const calls: unknown[] = [];
  const rpc = { forThread: () => ({ loops: [retired, active, completed, second], errors: [] }), control: (input: unknown) => { calls.push(input); return { ok: true }; } };
  const summary = renderSlot({ component: segment.summary }, props, { rpc });
  const details = renderSlot({ component: segment.details! }, props, { rpc });
  try {
    await summary.findByRole("button", { name: "looptender: 4 matching loops" });
    await details.findByRole("region", { name: `Loop ${second.loop}` });
    assert.equal(details.getAllByRole("region", { name: /^Loop / }).length, 4);
    fireEvent.click(within(details.getByRole("region", { name: `Loop ${second.loop}` })).getByRole("button", { name: "Pause" }));
    await waitFor(() => assert.equal(calls.length, 1));
    assert.deepEqual(calls[0], { configPath: second.configPath, action: "pause", managedId: second.managedId, expectedThreadId: props.threadId });
  } finally { summary.lifecycle.unmount(); details.lifecycle.unmount(); cleanup(); }
});

test("new-thread Pause and Stop bind the unique setup behind retired matches, never a display-name match", async () => {
  const threadId = "thr_example_created_matches";
  const setup = { ...active, managedThreadId: threadId, referenceSource: "plugin_setup" };
  const calls: unknown[] = [];
  const ui = renderSlot(app.threadContextCardRows[0]!, { threadId, projectId: "proj_example", environment: null }, { rpc: {
    forThread: () => ({ loops: [{ ...retired, loop: active.loop }, setup], errors: [] }),
    newThread: () => ({ ok: true, message: "", outcome: { state: "ready", message: "Started", loop: active.loop, bounds: { maxAutomaticMessages: 40, noProgressHours: 24 }, startedAutomatically: true, diagnostics: [] } }),
    control: (input: unknown) => { calls.push(input); return { ok: true }; },
  } });
  try {
    fireEvent.click(await ui.findByRole("button", { name: "Stop" }));
    await waitFor(() => assert.equal(calls.length, 1));
    assert.deepEqual(calls[0], { configPath: setup.configPath, action: "stop", managedId: setup.managedId, expectedThreadId: threadId });
    assert.equal(ui.getAllByRole("button", { name: "Pause" }).length, 1);
  } finally { ui.lifecycle.unmount(); cleanup(); }
});

test("ambiguous saved setups do not gain new-thread Start or Stop through array order", async () => {
  const threadId = "thr_example_ambiguous_matches";
  const loops = [active, fixture("other-example")].map(l => ({ ...l, managedThreadId: threadId, referenceSource: "plugin_setup" }));
  const ui = renderSlot(app.threadContextCardRows[0]!, { threadId, projectId: "proj_example", environment: null }, { rpc: {
    forThread: () => ({ loops, errors: [] }),
    newThread: () => ({ ok: true, message: "", outcome: { state: "ready", message: "Started", loop: active.loop, startedAutomatically: true, diagnostics: [] } }),
  } });
  try {
    await ui.findByRole("region", { name: `Loop ${active.loop}` });
    assert.equal(ui.getAllByRole("button", { name: "Pause" }).length, 2);
    assert.equal(ui.queryByRole("button", { name: "Stop" }), null);
    assert.equal(ui.queryByRole("button", { name: "Start keeping this going" }), null);
  } finally { ui.lifecycle.unmount(); cleanup(); }
});

test("duplicate names have stable visible references and explicit path disclosure in card and banner", async () => {
  for (const surface of ["card", "banner"] as const) {
    const threadId = `thr_example_duplicate_${surface}`;
    const first = { ...fixture("same-example"), configPath: "/example/first/looptender.yaml", managedId: "first-record" };
    const second = { ...first, configPath: "/example/second/looptender.yaml", managedId: "second-record" };
    let loops = [first, second]; const calls: unknown[] = [];
    const rpc = {
      forThread: () => ({ loops, errors: [] }),
      control: (input: unknown) => { calls.push(input); return { ok: true }; },
    };
    const props = { threadId, projectId: "proj_example", environment: null };
    const ui = surface === "card"
      ? renderSlot(app.threadContextCardRows[0]!, props, { rpc })
      : renderSlot({ component: app.threadContextSegments[0]!.details! }, { ...props, segments: [], isExpanded: true, onToggle() {} }, { rpc });
    try {
      const name = "Loop same-example · reference 2";
      await ui.findByRole("region", { name });
      const region = () => within(ui.getByRole("region", { name }));
      const disclosure = region().getByText("Configuration reference").closest("details")!;
      assert.equal(disclosure.open, false);
      fireEvent.click(region().getByText("Configuration reference"));
      assert.equal(disclosure.open, true);
      assert.ok(region().getByText(second.configPath));
      assert.ok(!ui.container.textContent?.includes(second.managedId));
      loops = [second, first]; await ui.behavior.emitRealtime("changed", {});
      assert.equal(region().getByText("Configuration reference").closest("details")!.open, true);
      fireEvent.click(region().getByRole("button", { name: "Pause" }));
      await waitFor(() => assert.equal(calls.length, 1));
      assert.deepEqual(calls[0], { configPath: second.configPath, action: "pause", managedId: second.managedId, expectedThreadId: threadId });
      // Removing a different match must not recycle reference 1 onto this record.
      loops = [second]; await ui.behavior.emitRealtime("changed", {});
      loops = [second, first]; await ui.behavior.emitRealtime("changed", {});
      assert.ok(region().getByText(second.configPath));
      assert.equal(ui.getAllByRole("region", { name: /^Loop / }).length, 2);
    } finally { ui.lifecycle.unmount(); cleanup(); }
  }
});
