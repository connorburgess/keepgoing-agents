import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { attachmentOnHost } from "./attach-host.ts";
import type { Execute } from "./setup-host.ts";

async function fixture() {
  const root=await mkdtemp(join(tmpdir(),"looptender-attach-"));
  const project=join(root,"project"), state=join(root,"private","state"), config=join(root,"private","loop.yaml");
  await mkdir(project); await mkdir(state,{recursive:true}); await writeFile(config,"fictional private config");
  const target={threadId:"thr_successor",provider:"codex",projectId:"proj_example",environmentId:"env_example",hostId:"host_example",project};
  const entry={id:"loop_0123456789abcdef0123456789abcdef",loop:"example-loop",configPath:config,stateDir:state};
  const agent={adapter:"bb",bbThread:target.threadId,provider:target.provider};
  const status:any={loop:entry.loop,stateDir:state,target:"primary",primaryAgent:{...agent},targetAgent:{...agent},controlState:"enabled",control:{ownership:{threadId:target.threadId,generation:8}},state:{failedChecks:2,lastCheck:{at:"2030-01-01T00:00:00Z",target:"primary",action:"none",outcome:"failed"}},daemon:{state:"responsive"}};
  let registry:any={schemaVersion:1,operation:"list",entries:[entry]};
  let client:any={schemaVersion:1,registrationId:entry.id,outcome:"available",issues:[],snapshot:{loop:entry.loop,target:{alias:"primary",adapter:"bb",bbThread:target.threadId}}};
  const calls:string[][]=[];
  const run:Execute=async (_bin,args,input,thread)=>{
    assert.equal(input,""); assert.equal(thread,target.threadId); calls.push(args);
    if(args[0]==="registry") return {code:0,data:structuredClone(registry)};
    if(args[0]==="client") return {code:0,data:structuredClone(client)};
    assert.deepEqual(args,["status","--json","-c",config]); return {code:0,data:structuredClone(status)};
  };
  return {root,target,entry,status,client,calls,run,setRegistry:(v:unknown)=>{registry=v;},setClient:(v:unknown)=>{client=v;},input:{bin:"keepgoing",target,registryId:entry.id}};
}

test("host attachment selects only registry references, verifies current owner and reports failed checks despite responsive daemon",async()=>{
  const f=await fixture(); try {
    const listing=await attachmentOnHost({...f.input,registryId:undefined},undefined,f.run);
    assert.deepEqual(listing.candidates,[{registryId:f.entry.id,loop:f.entry.loop}]); assert.equal(JSON.stringify(listing).includes(f.root),false);
    const result=await attachmentOnHost(f.input,undefined,f.run);
    assert.equal(result.snapshot?.generation,8); assert.equal(result.snapshot?.state.failedChecks,2); assert.equal(result.snapshot?.state.daemonState,"responsive");
    assert.ok(f.calls.every(c=>["registry","client","status"].includes(c[0]!))); assert.ok(f.calls.every(c=>!c.includes("--client")),"attachment never writes client-version tracking");
  }finally{await rm(f.root,{recursive:true,force:true});}
});

test("host refuses mismatched targets/providers/owners/state, pending transfers, routes, invalid registry bindings and config changes",async()=>{
  for(const kind of ["source-label","other-target","owner","pending","binding","state-dir","fallbacks","routes","unavailable-client","unknown-id","duplicate","config-change","inside-project","oversize","metadata-missing"]){
    const f=await fixture();try{
      let run=f.run;
      switch(kind){
        case "source-label": f.status.primaryAgent.provider="claude-code";break;
        case "other-target": f.status.targetAgent.bbThread="thr_predecessor";break;
        case "owner": f.status.control.ownership.threadId="thr_predecessor";break;
        case "pending": f.status.control.pendingTransfer={version:2};break;
        case "binding": f.status.control.bbTarget="thr_predecessor";break;
        case "state-dir": f.status.stateDir=f.target.project;break;
        case "fallbacks": f.status.redundancy={};break;
        case "routes": f.status.routes={source:"standard"};break;
        case "unavailable-client": f.client.outcome="unavailable";break;
        case "unknown-id": f.input.registryId="loop_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";break;
        case "duplicate": f.setRegistry({schemaVersion:1,operation:"list",entries:[f.entry,f.entry]});break;
        case "config-change": run=async(...a)=>{const r=await f.run(...a);if(a[1][0]==="status")await writeFile(f.entry.configPath,"changed fictional config");return r;};break;
        case "inside-project": f.entry.stateDir=f.target.project;break;
        case "oversize": await writeFile(f.entry.configPath,"x".repeat((1<<20)+1));break;
        case "metadata-missing": delete f.status.control.ownership;break;
      }
      await assert.rejects(attachmentOnHost(f.input,undefined,run),kind);
      assert.ok(!f.calls.some(c=>["start","resume","init","transfer"].includes(c[0]!)),kind);
    }finally{await rm(f.root,{recursive:true,force:true});}
  }
});

test("built CLI attachment reads a registered existing Codex loop without provider calls or changing private loop files", { skip: !process.env.KEEPGOING_TEST_BINARY }, async()=>{
  const {execFile}=await import("node:child_process");const {promisify}=await import("node:util");const {readFile,readdir}=await import("node:fs/promises");
  const exec=promisify(execFile);const root=await mkdtemp(join(tmpdir(),"looptender-attach-cli-"));
  try{
    const project=join(root,"example-project"),priv=join(root,"private"),state=join(priv,"state"),config=join(priv,"loop.yaml"),registry=join(root,"registry");
    await mkdir(project);await mkdir(state,{recursive:true});
    await exec("git",["init","-q",project]);
    await writeFile(config,`version: 1\nloop:\n  name: example-loop\n  adapter: bb\n  provider: codex\n  state_dir: ./state\n  bb:\n    thread: thr_successor\n  prompt: Fictional task\n  limits:\n    max_sends: 9\n`);
    await writeFile(join(state,"control.json"),JSON.stringify({enabled:true,paused:true,stop:true,awaitingUser:true,completed:true,note:"Fictional preserved note",ownership:{threadId:"thr_successor",generation:8}}));
    await writeFile(join(state,"state.json"),JSON.stringify({activeTarget:"primary",failedChecks:3,nudges:2,reprompts:1,handoffs:1,lastCheck:{at:"2030-01-01T00:00:00Z",target:"primary",action:"none",outcome:"failed"}}));
    await writeFile(join(state,"sends.json"),JSON.stringify({version:1,used:4}));
    const env={...process.env,LOOPTENDER_REGISTRY_DIR:registry,KEEPGOING_REGISTRY_DIR:registry,XDG_CONFIG_HOME:join(root,"config"),XDG_STATE_HOME:join(root,"user-state"),BB_THREAD_ID:"thr_successor"};
    const bin=process.env.KEEPGOING_TEST_BINARY!;
    const added=JSON.parse((await exec(bin,["registry","add","-c",config,"--json"],{env})).stdout);
    const before=await Promise.all((await readdir(state)).map(async name=>[name,await readFile(join(state,name),"utf8")]));const configBefore=await readFile(config,"utf8");
    const operations:string[]=[];
    const run:Execute=async(b,args,input,thread)=>{
      assert.equal(input,"");assert.equal(thread,"thr_successor");assert.ok(["registry","client","status"].includes(args[0]!));operations.push(args[0]!);
      const {stdout}=await exec(b,args,{env,timeout:20000,maxBuffer:1<<20});return {code:0,data:JSON.parse(stdout)};
    };
    const target={threadId:"thr_successor",provider:"codex",projectId:"proj_example",environmentId:"env_example",hostId:"host_example",project};
    const result=await attachmentOnHost({bin,target,registryId:added.entries[0].id},undefined,run);
    assert.equal(result.snapshot?.generation,8);assert.equal(result.snapshot?.state.controlState,"stop requested");assert.equal(result.snapshot?.state.failedChecks,3);
    assert.equal(await readFile(config,"utf8"),configBefore);assert.deepEqual(await Promise.all((await readdir(state)).map(async name=>[name,await readFile(join(state,name),"utf8")])),before);
    assert.deepEqual(operations,["registry","client","status","registry"]);
  }finally{await rm(root,{recursive:true,force:true});}
});
