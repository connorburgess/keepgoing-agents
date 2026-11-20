import { test } from "node:test";
import assert from "node:assert/strict";
import { createAttachController, type AttachDependencies } from "./attach-controller.ts";
import type { AttachSnapshot } from "./attach-contract.ts";
import type { SetupRecord } from "./setup-contract.ts";

function fixture() {
  const target = { threadId: "thr_successor", projectId: "proj_example", environmentId: "env_example", hostId: "host_example", project: "/projects/example", provider: "codex" };
  const snapshot: AttachSnapshot = { entry: { id: "loop_0123456789abcdef0123456789abcdef", loop: "example-loop", configPath: "/private/example/loop.yaml", stateDir: "/private/example/state" }, digest: "a".repeat(64), generation: 8, state: { controlState: "enabled", daemonState: "responsive", failedChecks: 2, lastCheck: null } };
  let records: SetupRecord[] = [];
  let saves = 0, inspections = 0;
  const d: AttachDependencies = {
    target: async () => structuredClone(target), binary: async () => "keepgoing",
    list: async () => [{ registryId: snapshot.entry.id, loop: snapshot.entry.loop }],
    inspect: async () => { inspections++; return structuredClone(snapshot); },
    records: async () => structuredClone(records), save: async r => { saves++; records = structuredClone(r); },
    guard: async () => {}, changed: () => {},
  };
  let controller = createAttachController(d);
  const call = (action: "list" | "preview" | "attach" | "inspect", ticket?: string) => controller.call({ action, threadId: target.threadId, registryId: snapshot.entry.id, ...(ticket ? { ticket } : {}) });
  return { d, target, snapshot, call, reload: () => { controller = createAttachController(d); }, records: () => records, setRecords: (r: SetupRecord[]) => { records=r; }, saves: () => saves, inspections: () => inspections };
}

test("explicit preview/attach imports reference only; response inspection and repeated attachment do not save", async () => {
  const f=fixture();
  const unrelated = { ...f.target, threadId:"thr_other", id:"other-record", loop:"other-loop", configPath:"/private/other/loop.yaml" };
  f.setRecords([unrelated]);
  const before=structuredClone(f.snapshot);
  const listed=await f.call("list"); assert.equal(listed.candidates?.length,1); assert.equal(f.saves(),0);
  assert.equal((await f.call("attach")).ok,false);
  const preview=await f.call("preview"); assert.equal(preview.ok,true); assert.equal(f.saves(),0);
  assert.match(preview.message,/Checks are failing/); assert.match(preview.message,/response only/);
  const attached=await f.call("attach",preview.ticket); assert.equal(attached.attached,true,attached.message);
  assert.equal(f.saves(),1); assert.deepEqual(f.records()[0],unrelated); assert.deepEqual(f.snapshot,before);
  f.reload();
  const checked=await f.call("inspect"); assert.equal(checked.attached,true); assert.equal(f.saves(),1); assert.equal(checked.record?.id,attached.record?.id);
  const again=await f.call("preview"); const repeat=await f.call("attach",again.ticket); assert.equal(repeat.attached,true); assert.equal(f.saves(),1);
});

test("external Claude-to-Codex transfer reconciles the same loop identity without needing a transfer command",async () => {
  const f=fixture();
  f.setRecords([{ ...f.target, threadId:"thr_predecessor", provider:"claude-code", id:"original-record", loop:f.snapshot.entry.loop, configPath:f.snapshot.entry.configPath, startJson:true, startJsonBin:"old-binary" }]);
  const p=await f.call("preview"); const r=await f.call("attach",p.ticket);
  assert.equal(r.attached,true,r.message); assert.equal(f.records().length,1); assert.equal(r.record?.id,"original-record");
  assert.equal(r.record?.provider,"codex"); assert.equal(r.record?.threadId,"thr_successor"); assert.equal(r.record?.startJson,undefined);
});

test("unknown save response recovers through read-only status even after reload",async () => {
  const f=fixture(); const save=f.d.save;
  f.d.save=async r => { await save(r); throw new Error("PRIVATE lost response"); };
  const p=await f.call("preview"); const r=await f.call("attach",p.ticket);
  assert.equal(r.ok,false); assert.match(r.message,/Check attachment status/); assert.ok(!r.message.includes("PRIVATE"));
  f.reload(); const checked=await f.call("inspect"); assert.equal(checked.attached,true); assert.equal(f.saves(),1);
});

test("changed target/config/owner/record or late guard refuses registration",async () => {
  for (const kind of ["provider","host","project","environment","path","digest","owner","record","late-config","guard"] as const) {
    const f=fixture(); const p=await f.call("preview");
    switch(kind) {
      case "provider": f.target.provider="claude-code"; break;
      case "host": f.target.hostId="host_other"; break;
      case "project": f.target.projectId="proj_other"; break;
      case "environment": f.target.environmentId="env_other"; break;
      case "path": f.target.project="/projects/other"; break;
      case "digest": f.snapshot.digest="b".repeat(64); break;
      case "owner": f.snapshot.generation++; break;
      case "record": f.setRecords([{...f.target,id:"conflict",loop:"other",configPath:"/private/other/loop.yaml"}]); break;
      case "late-config": { const inspect=f.d.inspect; let n=0; f.d.inspect=async (...args) => { const s=await inspect(...args); if(++n===2) s.digest="c".repeat(64); return s; }; break; }
      case "guard": f.d.guard=async()=>{throw new Error("PRIVATE pending transfer");}; break;
    }
    const r=await f.call("attach",p.ticket); assert.equal(r.ok,false,kind); assert.equal(f.saves(),0,kind); assert.ok(!r.message.includes("PRIVATE"));
  }
});

test("intervening holds are read freshly and never overwritten; absent checks are not operational success",async () => {
  const f=fixture(); const p=await f.call("preview");
  f.snapshot.state={controlState:"stopped",daemonState:"responsive",failedChecks:0,lastCheck:null};
  const r=await f.call("attach",p.ticket); assert.equal(r.attached,true,r.message); assert.equal(r.state?.controlState,"stopped"); assert.match(r.message,/No check is recorded/);
  assert.deepEqual(f.snapshot.state,r.state);
});

test("the registration write merges the latest unrelated setups after asynchronous verification",async()=>{
  const f=fixture(); const p=await f.call("preview");let n=0;
  const unrelated={...f.target,threadId:"thr_other",id:"other-record",loop:"other-loop",configPath:"/private/other/loop.yaml"};
  f.d.guard=async()=>{if(++n===2)f.setRecords([unrelated]);};
  const r=await f.call("attach",p.ticket);assert.equal(r.attached,true,r.message);assert.deepEqual(f.records()[0],unrelated);assert.equal(f.records().length,2);
});
