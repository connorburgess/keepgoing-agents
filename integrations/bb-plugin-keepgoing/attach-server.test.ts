import { test } from "node:test";
import assert from "node:assert/strict";
import { createFakePluginHost, makeThreadResponse, makeHostResponse } from "@get-bb/plugin-sdk/testing";
import plugin from "./server.ts";
import { hostContract } from "./host-contract.ts";
import { attachResult } from "./attach-contract.ts";

function fixture() {
  const target={threadId:"thr_successor",provider:"codex",projectId:"proj_example",environmentId:"env_example",hostId:"host_example",project:"/projects/example"};
  const entry={id:"loop_0123456789abcdef0123456789abcdef",loop:"example-loop",configPath:"/private/example/loop.yaml",stateDir:"/private/example/state"};
  const agent={adapter:"bb",bbThread:target.threadId,provider:target.provider};
  const state={controlState:"enabled",daemonState:"responsive",failedChecks:3,lastCheck:{at:"2030-01-01T00:00:00Z",target:"primary",action:"none",outcome:"failed"}};
  let fail=false;
  const methods:string[]=[];
  const f=createFakePluginHost({pluginId:"attach-example",settings:{keepgoingPath:"keepgoing"},sdk:{
    threads:{get:async({threadId}:{threadId:string})=>makeThreadResponse({id:threadId,environmentId:target.environmentId,projectId:target.projectId,providerId:target.provider})},
    environments:{get:async()=>({id:target.environmentId,projectId:target.projectId,hostId:target.hostId,path:target.project,isGitRepo:true,status:"ready",hostLifecycle:"active",lifecycle:{phase:"active"}})},
    hosts:{get:async()=>makeHostResponse({id:target.hostId,status:"connected"})},
  },experimental_callHostRpc:async({method,input,hostId})=>{
    assert.equal(hostId,target.hostId);methods.push(method);
    if(method==="attachment"){
      const p=hostContract.attachment.input.parse(input);assert.deepEqual(p.target,target);
      if(fail)throw new Error("PRIVATE host output");
      return p.registryId?{snapshot:{entry,digest:"a".repeat(64),generation:8,state}}:{candidates:[{registryId:entry.id,loop:entry.loop}]};
    }
    const p=hostContract.command.input.parse(input);methods.push(p.operation);assert.equal(p.operation,"status");
    return {code:0,data:{loop:entry.loop,stateDir:entry.stateDir,target:"primary",primaryAgent:agent,targetAgent:agent,controlState:state.controlState,control:{},window:"always",state:{failedChecks:state.failedChecks,lastCheck:state.lastCheck},daemon:{state:"responsive"}}};
  }});
  return {...f,target,entry,methods,fail:()=>{fail=true;}};
}

test("SDK attachment routes to exact host, serializes concurrent clients, preserves unrelated records and recovers identity after reload",async()=>{
  const f=fixture();let active=f.harness;try{
    await plugin(f.bb);
    const unrelated={...f.target,threadId:"thr_other",id:"unrelated",configPath:"/private/other/loop.yaml",loop:"other-loop"};
    await f.bb.storage.kv.set("setups.v1",[unrelated]);
    const call=(action:string,ticket?:string)=>active.behavior.callRpc("attachment",{action,threadId:f.target.threadId,registryId:f.entry.id,...(ticket?{ticket}:{})}).then(v=>attachResult.parse(v));
    const [p1,p2]=await Promise.all([call("preview"),call("preview")]);
    const both=await Promise.all([call("attach",p1.ticket),call("attach",p2.ticket)]);assert.ok(both.some(r=>r.attached));
    const saved=await f.bb.storage.kv.get("setups.v1") as any[];assert.equal(saved.length,2);assert.deepEqual(saved[0],unrelated);
    assert.equal(saved[1].registryId,f.entry.id);const id=saved[1].id;
    const reloaded=await active.lifecycle.reload(plugin);active=reloaded.harness;
    const checked=await call("inspect");assert.equal(checked.attached,true);assert.equal(checked.record?.id,id);
    const displayed=await active.behavior.callRpc("forThread",{threadId:f.target.threadId}) as any;
    assert.equal(displayed.loops[0].imported,true);assert.equal(displayed.loops[0].details.failedChecks,3);
    assert.ok(!f.methods.some(x=>["init","transfer","start","resume"].includes(x)));
    f.fail();const failed=await active.behavior.callRpc("forThread",{threadId:f.target.threadId}) as any;
    assert.equal(failed.loops.length,0);assert.equal(failed.errors.length,1);assert.ok(!JSON.stringify(failed).includes("PRIVATE"));
  }finally{await active.lifecycle.dispose();}
});

test("browser cannot supply executable, path, provider or host; pending plugin moves block attachment",async()=>{
  const f=fixture();try{
    await plugin(f.bb);
    for(const [key,value]of Object.entries({bin:"bad",configPath:"/private/other",hostId:"host_other",provider:"claude-code"}))await assert.rejects(f.harness.behavior.callRpc("attachment",{action:"list",threadId:f.target.threadId,[key]:value}));
    await f.bb.storage.kv.set("moves.v1",[{id:"example-move",record:{...f.target,id:"old",threadId:"thr_predecessor",configPath:f.entry.configPath,loop:f.entry.loop},destination:f.target,bin:"keepgoing",createdAt:1}]);
    const r=attachResult.parse(await f.harness.behavior.callRpc("attachment",{action:"preview",threadId:f.target.threadId,registryId:f.entry.id}));
    assert.equal(r.ok,false);assert.equal(await f.bb.storage.kv.get("setups.v1"),undefined);
  }finally{await f.harness.lifecycle.dispose();}
});

test("manual configured-path duplicates are refused without editing settings or saved records",async()=>{
  const f=fixture();try{
    await plugin(f.bb);await f.harness.behavior.setSettings({configs:f.entry.configPath});
    const r=attachResult.parse(await f.harness.behavior.callRpc("attachment",{action:"preview",threadId:f.target.threadId,registryId:f.entry.id}));
    assert.equal(r.ok,false);assert.match(r.message,/already listed in Loop configs/);assert.equal(await f.bb.storage.kv.get("setups.v1"),undefined);
  }finally{await f.harness.lifecycle.dispose();}
});
