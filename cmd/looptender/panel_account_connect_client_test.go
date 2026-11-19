package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPanelAccountConnectClientBindingAndUnknownOutcome(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	raw, err := dockUI.ReadFile("dockui/dock.js")
	if err != nil {
		t.Fatal(err)
	}
	js := strings.ReplaceAll(string(raw), "\r\n", "\n")
	source := "const assert=require('node:assert/strict');\n"
	for _, head := range []string{"function accountConnectSupported(c) {", "function accountConnectReply(d, c, step, attempt) {", "function accountConnectPost(c, step, form, attempt) {", "function accountConnectRequest(c, step) {", "function accountConnectOpen(c) {"} {
		source += jsFunction(t, js, head) + "\n}\n"
	}
	source += `
const id='a'.repeat(32), other='b'.repeat(32), attempt='c'.repeat(32);
const c={id,provider:'codex'}, accountConnections=Object.create(null);
const state={server:{form:'d'.repeat(64),accountConnectProviders:['codex','claude-code']},accountOpen:{}};
function list(v){return Array.isArray(v)?v:[];} function changesOn(){return true;}
function render(){} function focusOn(){} function announce(){} function load(){loads++;}
const document={hidden:false};function route(){return{view:'accounts'};}
let posts=0,loads=0,reply,expectedAction="connect_account",lastBody;
global.fetch=async(url,opts)=>{posts++;assert.equal(opts.method,'POST');assert.ok(opts.signal instanceof AbortSignal);lastBody=JSON.parse(opts.body);assert.equal(lastBody.action,expectedAction);return{ok:true,text:async()=>JSON.stringify(reply)};};
const preview={attemptId:attempt,action:'connect_account',key:id,title:'Connect Example?',confirm:'Connect Codex',form:'e'.repeat(64),expiresAt:'2035-01-01T00:00:00Z',lines:[[{t:'Separate context; other accounts unchanged.'}]],lost:'Unknown',refused:'Refused'};
const value={id:attempt,accountId:id,provider:'codex',state:'waiting',expiresAt:'2035-01-01T00:00:00Z',authorizationUrl:'https://auth.openai.com/oauth/authorize?state=fictional'};
(async()=>{
 assert.equal(accountConnectReply({schemaVersion:1,preview},c,'prepare').preview,preview);
 assert.equal(accountConnectReply({schemaVersion:1,connect:value},c,'status',attempt).connect,value);
 for(const bad of [{accountId:other},{provider:'claude-code'},{id:other},{state:'future'},{expiresAt:'no'},
 {authorizationUrl:'javascript:alert(1)'},{authorizationUrl:'https://auth.openai.com.evil.example/oauth/authorize'},
 {authorizationUrl:'https://name:password@auth.openai.com/oauth/authorize'},{authorizationUrl:'https://auth.openai.com:123/oauth/authorize'},
 {authorizationUrl:'https://auth.openai.com/redirect'},{authorizationUrl:'https://auth.openai.com/oauth/authorize#secret'}]){
  assert.throws(()=>accountConnectReply({schemaVersion:1,connect:{...value,...bad}},c,'status',attempt));
 }
 for(const bad of [{schemaVersion:1},{schemaVersion:1,preview,connect:value},{schemaVersion:1,result:{outcome:'done',text:'unknown'}}]) assert.throws(()=>accountConnectReply(bad,c,'start'));
 accountConnections[id]={preview};reply={schemaVersion:1};accountConnectRequest(c,'start');
 for(let i=0;i<10;i++)await new Promise(resolve=>setImmediate(resolve));
 assert.equal(posts,1);assert.equal(loads,0);assert.equal(accountConnections[id].busy,false);
 assert.match(accountConnections[id].result.text,/may still be running/);assert.equal(accountConnections[id].preview,null);assert.equal(accountConnections[id].connect.id,attempt,'lost start retains reviewed attempt ID');
 accountConnections[id]={connect:value};posts=0;accountConnectOpen(c);assert.equal(posts,0,'opening an active attempt never starts another');
 reply={schemaVersion:1,connect:{...value,state:'connected',authorizationUrl:undefined}};accountConnectRequest(c,'status');
 for(let i=0;i<10;i++)await new Promise(resolve=>setImmediate(resolve));
 assert.equal(posts,1);assert.equal(loads,1,'connected reloads saved catalog, never calls quota');
 accountConnections[id]={newLabel:'Second Example'};expectedAction='connect_another_account';reply={schemaVersion:1,preview};
 accountConnectRequest(c,'prepare');for(let i=0;i<10;i++)await new Promise(resolve=>setImmediate(resolve));
 assert.equal(lastBody.label,'Second Example');assert.equal(accountConnections[id].newLabel,'Second Example');
 assert.equal(accountConnections[id].preview.attemptId,attempt);
 expectedAction='connect_account';let finish;
 global.fetch=async()=>new Promise(resolve=>{posts++;finish=()=>resolve({ok:true,text:async()=>JSON.stringify({schemaVersion:1,connect:{...value,state:'connected',authorizationUrl:undefined}})});});
 const beforeLoads=loads;accountConnectRequest(c,'start');state.server.form='f'.repeat(64);finish();
 for(let i=0;i<10;i++)await new Promise(resolve=>setImmediate(resolve));
 assert.equal(accountConnections[id],undefined,'session replacement discards old connection reply');assert.equal(loads,beforeLoads);
 console.log('Account/attempt/provider binding, URL rejection, lost start ID, no replay, additive label and session replacement PASS');
})().catch(e=>{console.error(e);process.exitCode=1;});
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=commonjs", "-")
	cmd.Stdin = bytes.NewBufferString(source)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("client: %v\n%s", err, out)
	}
	t.Log(string(out))
}
