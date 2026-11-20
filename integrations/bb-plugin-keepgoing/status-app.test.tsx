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
  details: { goalState: "recorded", goal: { id: "goal_0123456789abcdef0123456789abcdef", revision: 2, state: "recorded" }, readAt: "2026-01-01T00:00:00Z", completed: true, targetThread: "thr_example", targetAdapter: "bb", targetConfigured: true, runtime: { state: "completed", source: "control", freshness: "stale" }, daemon: { state: "not_recorded" }, lastCheck: { at: "2025-12-31T23:00:00Z", target: "primary", action: "wake", outcome: "failed" } },
};

test("card shows completion and evidence; failed refresh labels retained data and hides actions until recovery", async () => {
  let phase = "ready";
  const ui = renderSlot(app.threadContextCardRows[0]!, { threadId: "thr_status", projectId: "proj_example", environment: null }, { rpc: {
    forThread: () => { if (phase === "lost") throw new Error("PRIVATE RPC ERROR"); return { loops: [loop], errors: [] }; },
  } });
  try {
    const label = await ui.findByText("looptender: completed");
    const title = label.closest("[title]")?.getAttribute("title") ?? ui.container.querySelector("[title]")?.getAttribute("title") ?? "";
    assert.match(title, /Goal: goal_0123456789abcdef0123456789abcdef.*revision 2/);
    assert.match(title, /Task completion: declared; not independently verified/);
    assert.match(title, /Current target: thr_example/); assert.match(title, /Last recorded action: wake · failed/);
    assert.match(title, /running supervisor not confirmed/); assert.match(title, /Last sent: unavailable/);
    // A held loop says where a person resumes it; the plugin offers no Resume.
    assert.ok(ui.getByText(/A person resumes this from an ordinary terminal/)); assert.equal(ui.queryByRole("button", { name: "Resume" }), null);
    assert.ok(ui.getByText('keepgoing resume -c "/private/example/keepgoing.yaml"'));
    phase = "lost"; await ui.behavior.emitRealtime("changed", {});
    await ui.findByRole("alert"); assert.ok(ui.getByText(/previous snapshot/));
    assert.equal(ui.queryByText(/A person resumes this from an ordinary terminal/), null);
    assert.equal(ui.queryByRole("button", { name: "Supervise this thread" }), null);
    assert.ok(!ui.container.textContent?.includes("PRIVATE"));
    phase = "ready"; fireEvent.click(ui.getByRole("button", { name: "Refresh status" }));
    await ui.findByText(/A person resumes this from an ordinary terminal/); assert.equal(ui.queryByRole("alert"), null);
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
    await summary.findByRole("button", { name: "looptender: watching thr_example" });
    await details.findByText(/Current target: thr_example.*not this thread/);
    assert.ok(details.getByText(/Task completion: declared; not independently verified/));
    assert.ok(details.getByText(/Goal: goal_0123456789abcdef0123456789abcdef.*revision 2/));
    assert.ok(details.getByText(/Last recorded action: wake · failed/));
    assert.ok(details.getByText(/External scheduler health: not inspected/));
    assert.ok(details.getByText(/Status read:.*snapshot/));
  } finally { summary.lifecycle.unmount(); details.lifecycle.unmount(); cleanup(); }
});

test("an imported loop with a responsive daemon and failed checks is visibly failed, not operational",async()=>{
  const failed={...loop,imported:true,controlState:"enabled",details:{...loop.details,completed:false,failedChecks:3,daemon:{state:"responsive"},lastCheck:{at:"2030-01-01T00:00:00Z",target:"primary",action:"none",outcome:"failed"}}};
  const ui=renderSlot(app.threadContextCardRows[0]!,{threadId:"thr_imported_failure",projectId:"proj_example",environment:null},{rpc:{forThread:()=>({loops:[failed],errors:[]})}});
  try{const label=await ui.findByText("looptender: checks failing");const text=label.closest("[title]")?.getAttribute("title")??"";assert.match(text,/Checks failing: 3/);assert.match(text,/responsive daemon does not establish operational supervision/);}finally{ui.lifecycle.unmount();cleanup();}
});

test("thread setup exposes registry attachment through the typed RPC without creating a setup",async()=>{
  const calls:string[]=[];
  const ui=renderSlot(app.threadContextCardRows[0]!,{threadId:"thr_attach_flow",projectId:"proj_example",environment:null},{rpc:{
    forThread:()=>({loops:[],errors:[]}),setup:()=>({ok:true,message:"No saved setup"}),transfer:()=>({ok:true,message:"No moves",choices:[]}),
    attachment:(raw:unknown)=>{const input=raw as {action:string;threadId:string}; calls.push(input.action);assert.equal(input.threadId,"thr_attach_flow");return {ok:true,candidates:[],message:"Registry read"};},
  }});
  try{fireEvent.click(await ui.findByRole("button",{name:"Supervise this thread"}));fireEvent.click(await ui.findByRole("button",{name:"Find registered loops"}));await ui.findByText("Registry read");assert.deepEqual(calls,["list"]);}finally{ui.lifecycle.unmount();cleanup();}
});

test("responsive imported startup with no recorded first check stays unverified",async()=>{
  const waiting={...loop,imported:true,controlState:"enabled",details:{...loop.details,completed:false,failedChecks:0,daemon:{state:"responsive"},lastCheck:null}};
  const ui=renderSlot(app.threadContextCardRows[0]!,{threadId:"thr_imported_first_check",projectId:"proj_example",environment:null},{rpc:{forThread:()=>({loops:[waiting],errors:[]})}});
  try{await ui.findByText("looptender: no check recorded");assert.equal(ui.queryByText("looptender: enabled"),null);}finally{ui.lifecycle.unmount();cleanup();}
});

test("reference provenance does not imply CLI coverage or provider health", async () => {
  const segment = app.threadContextSegments[0]!;
  for (const referenceSource of ["plugin_setting", "plugin_setup", "registry_attachment"] as const) {
    const props = { threadId: `thr_provenance_${referenceSource}`, projectId: "proj_example", environment: null, segments: [], isExpanded: true, onToggle() {} };
    const data = { ...loop, referenceSource, ...(referenceSource === "plugin_setup" ? { publication: { state: "repair_needed", source: "cli_registry" } } : {}) };
    const ui = renderSlot({ component: segment.details! }, props, { rpc: { forThread: () => ({ loops: [data], errors: [] }) } });
    try {
      await ui.findByText(referenceSource === "plugin_setting" ? /Reference: plugin setting; CLI menu registration checked separately/ : referenceSource === "plugin_setup" ? /Reference: saved BB setup/ : /Reference: explicitly attached from CLI registry/);
      if (referenceSource === "plugin_setup") assert.ok(ui.getByText(/CLI menu publication: unverified; use explicit registry reconcile/));
      assert.ok(ui.getByText(/running supervisor not confirmed/));
      assert.equal(ui.queryByRole("button", { name: "Resume" }), null);
    } finally { ui.lifecycle.unmount(); cleanup(); }
  }
});
