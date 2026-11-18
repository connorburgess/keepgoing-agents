import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import type { AttachResult } from "./attach-contract.ts";
const dom=new JSDOM("<!doctype html><html><body></body></html>",{url:"https://example.com"});
Object.assign(globalThis,{window:dom.window,document:dom.window.document,HTMLElement:dom.window.HTMLElement,IS_REACT_ACT_ENVIRONMENT:true});
Object.defineProperty(globalThis,"navigator",{value:dom.window.navigator,configurable:true});
const {render,fireEvent,cleanup}=await import("@testing-library/react");
const {AttachPanel}=await import("./attach-panel.tsx");
const id="loop_0123456789abcdef0123456789abcdef";

test("attachment requires list, selection, preview and explicit click; lost response offers status without replay or startup",async()=>{
  const calls:string[]=[];let attached=false,changed=0;
  const call=async(input:{action:string;registryId?:string;ticket?:string}):Promise<AttachResult>=>{
    calls.push(input.action);
    if(input.action==="list")return {ok:true,message:"Choose",candidates:[{registryId:id,loop:"example-loop"}]};
    assert.equal(input.registryId,id);
    if(input.action==="preview")return {ok:true,message:"Preview existing loop",ticket:"example-ticket"};
    if(input.action==="attach"){assert.equal(input.ticket,"example-ticket");attached=true;throw new Error("PRIVATE lost response");}
    return {ok:true,attached,message:attached?"Existing attachment verified; checks failing despite responsive daemon":"Not attached"};
  };
  const ui=render(<AttachPanel threadId="thr_successor" call={call} onChanged={()=>changed++}/>);
  try{
    assert.equal(calls.length,0);fireEvent.click(ui.getByRole("button",{name:"Find registered loops"}));
    await ui.findByLabelText("Registered loop");
    fireEvent.change(ui.getByLabelText("Registered loop"),{target:{value:id}});
    assert.equal((ui.getByRole("button",{name:"Preview attachment"})as HTMLButtonElement).disabled,false);
    fireEvent.click(ui.getByRole("button",{name:"Preview attachment"}));await ui.findByText("Preview existing loop");
    assert.equal(calls.includes("attach"),false);
    fireEvent.click(ui.getByRole("button",{name:"Attach existing loop"}));await ui.findByRole("alert");
    assert.equal(ui.queryByRole("button",{name:"Attach existing loop"}),null);assert.ok(!ui.container.textContent?.includes("PRIVATE"));
    fireEvent.click(ui.getByRole("button",{name:"Check attachment status"}));await ui.findByText(/Existing attachment verified/);
    assert.equal(changed,1);assert.equal(calls.filter(c=>c==="attach").length,1);assert.ok(!calls.some(c=>["create","start","resume"].includes(c)));
  }finally{cleanup();}
});

test("empty registry gives a recovery instruction without suggesting automatic setup",async()=>{
  const ui=render(<AttachPanel threadId="thr_successor" call={async()=>({ok:true,candidates:[],message:"Read registry"})} onChanged={()=>{}}/>);
  try{fireEvent.click(ui.getByRole("button",{name:"Find registered loops"}));await ui.findByText(/Register the existing loop with the CLI first/);assert.equal(ui.queryByRole("button",{name:"Attach existing loop"}),null);}finally{cleanup();}
});
