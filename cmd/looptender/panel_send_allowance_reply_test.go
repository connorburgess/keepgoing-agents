package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPanelSendAllowanceClientUnknownOutcome(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: actual client runtime not executed")
	}
	b, err := dockUI.ReadFile("dockui/dock.js")
	if err != nil {
		t.Fatal(err)
	}
	js := strings.ReplaceAll(string(b), "\r\n", "\n")
	source := "const assert=require('node:assert/strict');\n"
	for _, head := range []string{"function allowanceReply(d, key, step) {", "function allowancePost(key, step, form, amount) {", "function confirmChange(option) {"} {
		source += jsFunction(t, js, head) + "\n}\n"
	}
	source += `
let raw,posts,focus,loads;
const key='example',p={action:'set_send_allowance',key,title:'Allowance?',confirm:'Save',form:'a'.repeat(64),expiresAt:'2030-01-01T00:00:00Z',lines:[[{t:'Counted attempts: 40.'}]],lost:'Outcome unknown; read first.',refused:'Nothing changed.'};
const state={};function render(){}function focusOn(x){focus=x;}function announce(){}function load(){loads++;}
global.fetch=async(url,options)=>{posts++;assert.equal(url,'api/v1/send-allowance/example/confirm');assert.deepEqual(JSON.parse(options.body),{action:'set_send_allowance'});assert.ok(options.signal instanceof AbortSignal);return {ok:true,json:async()=>raw};};
(async()=>{
 for(const bad of [{schemaVersion:1},{schemaVersion:1,result:{outcome:'future',text:'Unknown'}},{schemaVersion:1,preview:p},{schemaVersion:1,result:{outcome:'done'}}]){
  raw=bad;posts=0;loads=0;focus='';state.change={key,action:p.action,preview:p};confirmChange(null);
  for(let n=0;n<10;n++)await new Promise(resolve=>setImmediate(resolve));
  assert.equal(posts,1);assert.equal(loads,1);assert.equal(focus,'change-result');assert.equal(state.change.result.outcome,'uncertain');assert.equal(state.change.result.text,p.lost);
 }
 for(const change of [{key:'other'},{action:'resume'},{lines:[]},{form:'bad'},{lost:''}])assert.throws(()=>allowanceReply({schemaVersion:1,preview:{...p,...change}},key,'prepare'));
 assert.equal(allowanceReply({schemaVersion:1,preview:p},key,'prepare').preview,p);
 console.log('Actual allowance client unknown-outcome/no-replay checks passed');
})().catch(e=>{console.error(e);process.exitCode=1;});
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=commonjs", "-")
	cmd.Stdin = bytes.NewBufferString(source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("client: %v\n%s", err, out)
	}
}

func TestPanelSendAllowanceTypingPreservationBoundary(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: actual client runtime not executed")
	}
	b, err := dockUI.ReadFile("dockui/dock.js")
	if err != nil {
		t.Fatal(err)
	}
	js := strings.ReplaceAll(string(b), "\r\n", "\n")
	source := "const assert=require('node:assert/strict');\n" + jsFunction(t, js, "function editingAllowanceInput(r, pane) {") + "\n}\n"
	// Exercise the real rendering decision through the point where the page
	// would be rebuilt; no DOM replacement is allowed while typing.
	render := jsFunction(t, js, "function render(navigated, force) {")
	at := strings.Index(render, "var top = navigated")
	if at < 0 {
		t.Fatal("render boundary missing")
	}
	source += render[:at] + "rebuilt++; return;\n}\n"
	source += `
let rebuilt=0,reads=0,exists=true;
const input={id:'allowance-input',matches:()=>true};
const document={activeElement:input};
const pane={contains:x=>x===input};
const state={data:{},server:{changes:'on',sendAllowanceEdit:true},change:{key:'example',action:'set_send_allowance',editing:true}};
function changesOn(){return state.server.changes==='on';}
function loopByKey(){return exists;}
function selecting(){return false;}
function route(){return {view:'loop',key:'example'};}
function $(id){assert.equal(id,'pane');return pane;}
function renderReadline(){reads++;}
render(false,false);assert.equal(rebuilt,0);assert.equal(reads,1);assert.equal(state.pending,true);
render(false,true);assert.equal(rebuilt,1); // explicit privacy redraw
render(true,false);assert.equal(rebuilt,2); // navigation
state.server.sendAllowanceEdit=false;render(false,false);assert.equal(rebuilt,3);
state.server.sendAllowanceEdit=true;state.server.changes='off';render(false,false);assert.equal(rebuilt,4);
state.server.changes='on';exists=false;render(false,false);assert.equal(rebuilt,5);
exists=true;document.activeElement=null;render(false,false);assert.equal(rebuilt,6);
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=commonjs", "-")
	cmd.Stdin = bytes.NewBufferString(source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render boundary: %v\n%s", err, out)
	}
}
