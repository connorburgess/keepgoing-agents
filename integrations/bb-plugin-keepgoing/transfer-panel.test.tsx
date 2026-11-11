import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import { transferInput, type TransferResult } from "./transfer-contract.ts";
const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "https://example.com" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });
const { render, fireEvent, cleanup } = await import("@testing-library/react");
const { TransferPanel } = await import("./transfer-panel.tsx");
const choices = [{ id: "record", loop: "example-loop", from: "thr_source", pending: false }];
const summary = { loop: "example-loop", from: "thr_source", to: "thr_destination", provider: "codex", holds: { enabled: true, paused: true, stop: true, awaitingUser: false, completed: true } };
test("move panel shows targets and holds, requires preview and move clicks, never resumes", async () => {
  const calls: string[] = [];
  const call = async ({ action }: { action: string }): Promise<TransferResult> => {
    calls.push(action);
    if (action === "list") return { ok: true, choices, message: "Choose a loop" };
    if (action === "preview") return { ok: true, summary, ticket: "ticket", message: "Preview only" };
    return { ok: true, moved: true, message: "Moved; awaiting explicit resume" };
  };
  const ui = render(<TransferPanel threadId="thr_destination" call={call} onChanged={() => {}} />);
  try {
    await ui.findByRole("option", { name: /example-loop/ });
    fireEvent.change(ui.getByLabelText("Existing loop"), { target: { value: "record" } });
    assert.equal(ui.queryByRole("button", { name: "Move and keep held" }), null);
    fireEvent.click(ui.getByRole("button", { name: "Preview move" }));
    await ui.findByText("Preview only"); assert.ok(ui.getByText(/paused=true, stop=true/)); assert.ok(ui.getByText(/thr_source → thr_destination/));
    assert.ok(!calls.includes("apply"));
    fireEvent.click(ui.getByRole("button", { name: "Move and keep held" }));
    await ui.findByText("Moved; awaiting explicit resume"); assert.deepEqual(calls, ["list", "preview", "apply"]);
    assert.equal(ui.queryByRole("button", { name: "Move and keep held" }), null);
  } finally { cleanup(); }
});
test("selection changes discard tickets and lost responses expose status without replay", async () => {
  const calls: string[] = [];
  const call = async ({ action }: { action: string }): Promise<TransferResult> => {
    calls.push(action);
    if (action === "list") return { ok: true, choices, message: "Choose" };
    if (action === "preview") return { ok: true, summary, ticket: "ticket", message: "Preview" };
    if (action === "inspect") return { ok: false, canRecover: true, message: "Pending recovery" };
    if (action === "recover") return { ok: true, moved: true, message: "Recovered and held" };
    throw new Error("PRIVATE-ERROR");
  };
  const ui = render(<TransferPanel threadId="thr_destination" call={call} onChanged={() => {}} />);
  try {
    await ui.findByRole("option", { name: /example-loop/ });
    fireEvent.change(ui.getByLabelText("Existing loop"), { target: { value: "record" } });
    fireEvent.click(ui.getByRole("button", { name: "Preview move" })); await ui.findByText("Preview");
    fireEvent.change(ui.getByLabelText("Existing loop"), { target: { value: "" } }); assert.equal(ui.queryByRole("button", { name: "Move and keep held" }), null);
    fireEvent.change(ui.getByLabelText("Existing loop"), { target: { value: "record" } });
    fireEvent.click(ui.getByRole("button", { name: "Preview move" })); await ui.findByText("Preview");
    fireEvent.click(ui.getByRole("button", { name: "Move and keep held" })); await ui.findByRole("alert");
    assert.ok(!ui.container.textContent?.includes("PRIVATE")); assert.equal(ui.queryByRole("button", { name: "Move and keep held" }), null);
    assert.equal(calls.filter(c => c === "apply").length, 1);
    fireEvent.click(ui.getByRole("button", { name: "Check move status" })); await ui.findByText("Pending recovery"); assert.ok(!calls.includes("recover"));
    fireEvent.click(ui.getByRole("button", { name: "Recover move" })); await ui.findByText("Recovered and held");
  } finally { cleanup(); }
});

test("context card keeps an in-flight move panel mounted while realtime adds the pending loop", async () => {
  const { loadPluginApp, renderSlot } = await import("@get-bb/plugin-sdk/testing/app");
  const app = await loadPluginApp(() => import("./app.tsx"));
  let phase = "none", release!: () => void, entered!: () => void;
  const gate = new Promise<void>(r => { release = r; }), ready = new Promise<void>(r => { entered = r; });
  const loop = { loop: "example-loop", configPath: "/private/example/keepgoing.yaml", managedId: "record", managedThreadId: "thr_source", controlState: "move requires inspection", note: null, target: "primary", supervising: false, lastState: null, lastWakeAt: null, checkedAt: null, handoffs: 0, fallbacks: null };
  const ui = renderSlot(app.threadContextCardRows[0]!, { threadId: "thr_destination", projectId: "proj_example", environment: null }, { rpc: {
    forThread: () => ({ loops: phase === "none" ? [] : [loop], errors: [] }),
    setup: () => ({ ok: true, message: phase === "done" ? "Destination diagnostics refreshed" : "Setup idle" }),
    transfer: async (raw: unknown) => {
      const input = transferInput.parse(raw);
      if (input.action === "list") return { ok: true, choices, message: "Choose a move" };
      if (input.action === "preview") return { ok: true, summary, ticket: "ticket", message: "Preview ready" };
      entered(); await gate; phase = "done";
      return { ok: true, moved: true, message: "Move confirmed and held" };
    },
  } });
  try {
    fireEvent.click(await ui.findByRole("button", { name: "Supervise this thread" }));
    await ui.findByRole("option", { name: /example-loop/ });
    fireEvent.change(ui.getByLabelText("Existing loop"), { target: { value: "record" } });
    fireEvent.click(ui.getByRole("button", { name: "Preview move" })); await ui.findByText("Preview ready");
    fireEvent.click(ui.getByRole("button", { name: "Move and keep held" })); await ready;
    phase = "pending"; await ui.behavior.emitRealtime("changed", {});
    await ui.findByText("keepgoing: move requires inspection");
    release();
    await ui.findByText("Move confirmed and held");
    await ui.findByText("Destination diagnostics refreshed");
  } finally { release?.(); ui.lifecycle.unmount(); cleanup(); }
});
