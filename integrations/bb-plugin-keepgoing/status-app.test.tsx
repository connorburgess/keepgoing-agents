import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "https://example.com" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });
const { fireEvent, cleanup } = await import("@testing-library/react");
const { loadPluginApp, renderSlot } = await import("@get-bb/plugin-sdk/testing/app");
const app = await loadPluginApp(() => import("./app.tsx"));
const loop = { loop: "example", configPath: "/private/example/keepgoing.yaml", controlState: "completed", note: null, target: "primary", supervising: true, lastState: null, lastWakeAt: "0001-01-01T00:00:00Z", checkedAt: null, handoffs: 0, fallbacks: null,
  details: { readAt: "2026-01-01T00:00:00Z", completed: true, targetThread: "thr_example", targetAdapter: "bb", targetConfigured: true, runtime: { state: "completed", source: "control", freshness: "stale" }, daemon: { state: "not_recorded" }, lastCheck: { at: "2025-12-31T23:00:00Z", target: "primary", action: "wake", outcome: "failed" } },
};

test("card shows completion and evidence; failed refresh labels retained data and hides actions until recovery", async () => {
  let phase = "ready";
  const ui = renderSlot(app.threadContextCardRows[0]!, { threadId: "thr_status", projectId: "proj_example", environment: null }, { rpc: {
    forThread: () => { if (phase === "lost") throw new Error("PRIVATE RPC ERROR"); return { loops: [loop], errors: [] }; },
  } });
  try {
    const label = await ui.findByText("keepgoing: completed");
    const title = label.closest("[title]")?.getAttribute("title") ?? ui.container.querySelector("[title]")?.getAttribute("title") ?? "";
    assert.match(title, /Task completion: declared; not independently verified/);
    assert.match(title, /Current target: thr_example/); assert.match(title, /Last recorded action: wake · failed/);
    assert.match(title, /running supervisor not confirmed/); assert.match(title, /Last sent: unavailable/);
    assert.ok(ui.getByRole("button", { name: "Resume" }));
    phase = "lost"; await ui.behavior.emitRealtime("changed", {});
    await ui.findByRole("alert"); assert.ok(ui.getByText(/previous snapshot/));
    assert.equal(ui.queryByRole("button", { name: "Resume" }), null);
    assert.equal(ui.queryByRole("button", { name: "Supervise this thread" }), null);
    assert.ok(!ui.container.textContent?.includes("PRIVATE"));
    phase = "ready"; fireEvent.click(ui.getByRole("button", { name: "Refresh status" }));
    await ui.findByRole("button", { name: "Resume" }); assert.equal(ui.queryByRole("alert"), null);
  } finally { ui.lifecycle.unmount(); cleanup(); }
});

test("returned lookup errors do not offer setup as though the thread had no supervision", async () => {
  const ui = renderSlot(app.threadContextCardRows[0]!, { threadId: "thr_unknown_status", projectId: "proj_example", environment: null }, { rpc: {
    forThread: () => ({ loops: [], errors: ["PRIVATE PATH"] }),
  } });
  try {
    await ui.findByRole("alert");
    assert.equal(ui.queryByRole("button", { name: "Supervise this thread" }), null);
    assert.ok(!ui.container.textContent?.includes("PRIVATE"));
  } finally { ui.lifecycle.unmount(); cleanup(); }
});

test("banner names a different target and expands historical evidence with explicit scheduler limits", async () => {
  const segment = app.threadContextSegments[0]!;
  const props = { threadId: "thr_banner_status", projectId: "proj_example", environment: null, segments: [], isExpanded: true, onToggle() {} };
  const rpc = { forThread: () => ({ loops: [{ ...loop, controlState: "enabled", supervising: false }], errors: [] }) };
  const summary = renderSlot({ component: segment.summary }, props, { rpc });
  const details = renderSlot({ component: segment.details! }, props, { rpc });
  try {
    await summary.findByRole("button", { name: "keepgoing: watching thr_example" });
    await details.findByText(/Current target: thr_example.*not this thread/);
    assert.ok(details.getByText(/Task completion: declared; not independently verified/));
    assert.ok(details.getByText(/Last recorded action: wake · failed/));
    assert.ok(details.getByText(/External scheduler health: not inspected/));
    assert.ok(details.getByText(/Status read:.*snapshot/));
  } finally { summary.lifecycle.unmount(); details.lifecycle.unmount(); cleanup(); }
});
