import { test } from "node:test";
import assert from "node:assert/strict";
import { createTransferController, type TransferDependencies } from "./transfer-controller.ts";
import type { SetupRecord, SetupTarget } from "./setup-contract.ts";
import type { MoveIntent } from "./transfer-contract.ts";

const source: SetupTarget = { threadId: "thr_source", projectId: "proj_example", environmentId: "env_example", hostId: "host_example", project: "/projects/example", provider: "codex" };
const destination = { ...source, threadId: "thr_destination" };
const record: SetupRecord = { ...source, id: "record-example", loop: "example-loop", configPath: "/private/example/keepgoing.yaml" };
const holds = { enabled: true, paused: true, stop: true, awaitingUser: false, completed: true };
function fixture() {
  let records: SetupRecord[] = [structuredClone(record)], intents: MoveIntent[] = [];
  let thread = source.threadId, pending: { version: number; id: string; from: string; to: string } | undefined;
  let bound = false, held = false;
  const calls: string[] = [];
  const identity = (t: SetupTarget) => ({ thread: t.threadId, project: t.projectId, environment: t.environmentId, host: t.hostId, provider: t.provider });
  const preview = { schemaVersion: 1, outcome: "preview", applySupported: true, compatibility: "compatible_metadata", from: identity(source), to: identity(destination), controls: { ...holds }, owner: source.threadId, goal: { id: "goal_example", revision: 2, state: "recorded" }, configuredProvider: "codex", configuredModel: "example-model", configuredQuotaGroup: "primary", privateNote: "PRIVATE-SECRET" };
  const finish = () => { thread = destination.threadId; pending = undefined; bound = true; held = true; };
  const d: TransferDependencies = {
    target: async id => structuredClone(id === source.threadId ? source : destination),
    binary: async () => "keepgoing", records: async () => structuredClone(records), save: async r => { records = structuredClone(r); },
    intents: async () => structuredClone(intents), saveIntents: async i => { intents = structuredClone(i); },
    guard: async () => {}, changed: () => calls.push("changed"),
    command: async (_bin, r, op) => {
      calls.push(op); assert.equal(r.threadId, destination.threadId);
      const agent = { adapter: "bb", bbThread: thread, provider: "codex" };
      return { code: 0, data: { loop: record.loop, target: "primary", primaryAgent: agent, targetAgent: agent, control: { ...holds, awaitingUser: held, ...(pending ? { pendingTransfer: pending } : {}), ...(bound ? { bbTarget: destination.threadId, ownership: { threadId: destination.threadId, generation: 8 } } : {}), note: "PRIVATE-SECRET" } } };
    },
    transfer: async (_i, op, id) => {
      calls.push(op);
      if (op === "preview") return { code: 0, data: structuredClone(preview) };
      assert.equal(intents.length, 1, "intent must be durable before any mutation");
      if (op === "recover") assert.equal(id, pending?.id);
      finish(); return { code: 0, data: { outcome: "applied" } };
    },
  };
  const controller = createTransferController(d);
  const call = (action: "list" | "preview" | "apply" | "inspect" | "recover", ticket?: string) => controller.call({ action, threadId: destination.threadId, recordId: record.id, ticket });
  return { d, controller, call, calls, preview, finish, records: () => records, intents: () => intents, setPending: () => { pending = { version: 1, id: "move_" + "a".repeat(32), from: source.threadId, to: destination.threadId }; held = true; } };
}

test("explicit preview/apply revalidates, preserves held outcome, and rebinds only after status", async () => {
  const f = fixture();
  const list = await f.call("list"); assert.equal(list.choices?.[0].from, source.threadId);
  const p = await f.call("preview"); assert.equal(p.ok, true); assert.deepEqual(p.summary?.holds, holds);
  assert.ok(!JSON.stringify(p).includes("PRIVATE")); assert.equal(f.intents().length, 0); assert.ok(!f.calls.includes("apply"));
  const result = await f.call("apply", p.ticket);
  assert.equal(result.moved, true, result.message); assert.equal(f.records()[0].threadId, destination.threadId); assert.equal(f.intents().length, 0);
  assert.deepEqual(f.calls.filter(c => c !== "changed"), ["preview", "preview", "apply", "status"]);
  assert.ok(!JSON.stringify(result).includes("PRIVATE"));
  assert.equal((await f.call("apply", p.ticket)).ok, false); assert.equal(f.calls.filter(x => x === "apply").length, 1);
});

test("preview refusal and changed controls, goal, binary, metadata or record cannot authorize apply", async () => {
  for (const kind of ["old_cli", "controls", "goal", "binary", "environment", "record", "conflict", "foreign_ticket"]) {
    const f = fixture();
    if (kind === "old_cli") f.preview.applySupported = false;
    const p = await f.call("preview");
    if (kind === "controls") f.preview.controls.stop = false;
    if (kind === "goal") f.preview.goal.revision++;
    if (kind === "binary") f.d.binary = async () => "other-binary";
    if (kind === "environment") f.d.target = async id => ({ ...(id === source.threadId ? source : destination), environmentId: "env_other" });
    if (kind === "record") f.d.records = async () => [{ ...record, configPath: "/private/other.yaml" }];
    if (kind === "conflict") f.d.guard = async () => { throw new Error("PRIVATE-CONFLICT"); };
    const r = await f.call("apply", kind === "foreign_ticket" ? "unknown" : p.ticket);
    assert.equal(r.ok, false, kind); assert.ok(!f.calls.includes("apply"), kind); assert.equal(f.intents().length, 0); assert.ok(!JSON.stringify(r).includes("PRIVATE"));
  }
});

test("intent write failure sends no transfer; host error retains reference without retry", async () => {
  const f = fixture(); const p = await f.call("preview");
  f.d.saveIntents = async () => { throw new Error("PRIVATE-STORAGE"); };
  const r = await f.call("apply", p.ticket); assert.equal(r.ok, false); assert.ok(!f.calls.includes("apply"));
  const g = fixture(); const q = await g.call("preview"); const run = g.d.transfer;
  g.d.transfer = async (i, op, id) => { if (op === "apply") { g.calls.push("apply"); throw new Error("PRIVATE-TRANSPORT"); } return run(i, op, id); };
  const failed = await g.call("apply", q.ticket); assert.equal(failed.ok, false); assert.equal(g.intents().length, 1);
  assert.equal(g.calls.filter(c => c === "apply").length, 1); assert.equal(g.records()[0].threadId, source.threadId);
  assert.ok(!JSON.stringify(failed).includes("PRIVATE"));
  const inspected = await g.call("inspect"); assert.equal(inspected.canRecover, true); assert.equal(g.calls.filter(c => c === "apply").length, 1);
});

test("reload reconciles lost successful response and a failed reference save without reapplying", async () => {
  for (const stage of ["host_response", "save_record", "clear_intent"]) {
    const f = fixture(); const p = await f.call("preview"); const run = f.d.transfer, save = f.d.save, saveIntents = f.d.saveIntents;
    if (stage === "host_response") f.d.transfer = async (i, op, id) => { const r = await run(i, op, id); if (op === "apply") throw new Error("lost"); return r; };
    if (stage === "save_record") f.d.save = async () => { throw new Error("storage"); };
    if (stage === "clear_intent") f.d.saveIntents = async xs => { if (xs.length === 0) throw new Error("storage"); await saveIntents(xs); };
    assert.equal((await f.call("apply", p.ticket)).ok, false, stage); assert.equal(f.intents().length, 1);
    f.d.transfer = run; f.d.save = save; f.d.saveIntents = saveIntents;
    const reloaded = createTransferController(f.d);
    const r = await reloaded.call({ action: "inspect", threadId: destination.threadId, recordId: record.id });
    assert.equal(r.moved, true, r.message); assert.equal(f.records()[0].threadId, destination.threadId); assert.equal(f.intents().length, 0);
    assert.equal(f.calls.filter(c => c === "apply").length, 1);
  }
});

test("pending CLI move recovers explicitly by observed ID; no CLI record retries only on recovery click", async () => {
  for (const pending of [true, false]) {
    const f = fixture(); const p = await f.call("preview"); const run = f.d.transfer;
    f.d.transfer = async (i, op, id) => {
      if (op === "apply") { f.calls.push("apply"); if (pending) f.setPending(); return { code: 1, data: null }; }
      return run(i, op, id);
    };
    const r = await f.call("apply", p.ticket); assert.equal(r.canRecover, true);
    assert.equal(f.records()[0].threadId, source.threadId);
    f.d.transfer = run;
    const reloaded = createTransferController(f.d);
    const list = await reloaded.call({ action: "list", threadId: destination.threadId }); assert.equal(list.choices?.[0].pending, true);
    const result = await reloaded.call({ action: "recover", threadId: destination.threadId, recordId: record.id });
    assert.equal(result.moved, true, result.message); assert.equal(f.calls.filter(c => c === "recover").length, pending ? 1 : 0);
    assert.equal(f.calls.filter(c => c === "apply").length, pending ? 1 : 2);
  }
});

test("unsupported post-state cannot change registration; another destination cannot recover", async () => {
  const f = fixture(); const p = await f.call("preview");
  f.d.command = async () => ({ code: 0, data: { control: { pendingTransfer: { id: "PRIVATE-UNKNOWN" } } } });
  const r = await f.call("apply", p.ticket); assert.equal(r.ok, false); assert.equal(f.intents().length, 1); assert.equal(f.records()[0].threadId, source.threadId);
  assert.equal((await f.controller.call({ action: "recover", threadId: "thr_other", recordId: record.id })).ok, false);
  assert.ok(!f.calls.includes("recover")); assert.ok(!JSON.stringify(r).includes("PRIVATE"));
});
