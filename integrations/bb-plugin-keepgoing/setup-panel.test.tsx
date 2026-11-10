import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import type { SetupResult } from "./setup-contract.ts";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "https://example.com" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });
const { render, fireEvent, waitFor, cleanup } = await import("@testing-library/react");
const { SetupPanel } = await import("./setup-panel.tsx");
const plan = { loop: "example-loop", project: "/projects/example", configPath: "/private/example-loop/keepgoing.yaml", stateDir: "/private/example-loop/state", threadId: "thr_example", provider: "codex", limits: { interval: "5m", wakeEvery: "1h", stallAfter: "30m", backoff: "10m", maxNudges: 3, maxReprompts: 3 } };
const record = { id: "example-record", loop: plan.loop, configPath: plan.configPath, threadId: plan.threadId, project: plan.project, provider: plan.provider, environmentId: "env_example", projectId: "proj_example", hostId: "host_example" };

test("UI requires preview, create, and start clicks; task edits invalidate preview", async () => {
  const calls: string[] = [];
  let changed = 0;
  const call = async (input: { action: string }): Promise<SetupResult> => {
    calls.push(input.action);
    if (input.action === "preview") return { ok: true, message: "Preview only", plan, ticket: "example-ticket" };
    if (input.action === "create") return { ok: true, message: "Saved disabled", record, canStart: true, checks: [] };
    if (input.action === "start") return { ok: true, message: "Background supervision started", record, canStart: false };
    return { ok: true, message: "Enter a task" };
  };
  const ui = render(<SetupPanel threadId="thr_example" call={call} onChanged={() => changed++} />);
  try {
    await ui.findByText("Enter a task");
    fireEvent.change(ui.getByLabelText("Task and completion criteria"), { target: { value: "Fictional task" } });
    fireEvent.click(ui.getByRole("button", { name: "Preview setup" }));
    await ui.findByText("Preview only");
    assert.ok(ui.getByText(`Configuration: ${plan.configPath}`));
    fireEvent.change(ui.getByLabelText("Task and completion criteria"), { target: { value: "Revised fictional task" } });
    assert.equal(ui.queryByRole("button", { name: "Create disabled setup" }), null);
    fireEvent.click(ui.getByRole("button", { name: "Preview setup" }));
    await ui.findByRole("button", { name: "Create disabled setup" });
    assert.ok(!calls.includes("create"));
    fireEvent.click(ui.getByRole("button", { name: "Create disabled setup" }));
    await ui.findByText("Saved disabled");
    assert.ok(!calls.includes("start")); assert.equal(ui.queryByLabelText("Task and completion criteria"), null);
    fireEvent.click(ui.getByRole("button", { name: "Start background supervision" }));
    await ui.findByText("Background supervision started");
    assert.equal(calls.filter(c => c === "start").length, 1); assert.equal(changed, 2);
    assert.equal(ui.queryByRole("button", { name: "Start background supervision" }), null);
  } finally { cleanup(); }
});

test("unverified saved setup shows diagnostics refresh without a start action", async () => {
  const ui = render(<SetupPanel threadId="thr_example" call={async () => ({ ok: true, record, canStart: false, message: "Target unavailable" })} onChanged={() => {}} />);
  try {
    await ui.findByText("Target unavailable");
    assert.ok(ui.getByRole("button", { name: "Refresh diagnostics" }));
    assert.equal(ui.queryByRole("button", { name: "Start background supervision" }), null);
  } finally { cleanup(); }
});

test("transport errors are visible and are never retried automatically", async () => {
  let calls = 0;
  const ui = render(<SetupPanel threadId="thr_example" call={async () => { calls++; throw new Error("fictional-private-error"); }} onChanged={() => {}} />);
  try {
    await waitFor(() => assert.ok(ui.getByRole("alert")));
    assert.equal(calls, 1); assert.ok(!ui.container.textContent?.includes("fictional-private-error"));
  } finally { cleanup(); }
});

test("lost creation response retains the destination without a replay button", async () => {
  const call = async (input: { action: string }): Promise<SetupResult> => {
    if (input.action === "preview") return { ok: true, plan, ticket: "example-ticket", message: "Preview only" };
    if (input.action === "create") throw new Error("lost response");
    return { ok: true, message: "Enter a task" };
  };
  const ui = render(<SetupPanel threadId="thr_example" call={call} onChanged={() => {}} />);
  try {
    await ui.findByText("Enter a task");
    fireEvent.change(ui.getByLabelText("Task and completion criteria"), { target: { value: "Fictional task" } });
    fireEvent.click(ui.getByRole("button", { name: "Preview setup" }));
    await ui.findByRole("button", { name: "Create disabled setup" });
    fireEvent.click(ui.getByRole("button", { name: "Create disabled setup" }));
    await ui.findByRole("alert");
    assert.ok(ui.getByText(`Configuration: ${plan.configPath}`));
    assert.equal(ui.queryByRole("button", { name: "Create disabled setup" }), null);
    assert.equal(ui.queryByRole("button", { name: "Start background supervision" }), null);
  } finally { cleanup(); }
});
