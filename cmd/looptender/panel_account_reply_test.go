package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Run the actual client functions with a fictional HTTP transport. The Go-only
// toolchain remains supported; hosts with Node additionally execute the UI
// response/outcome regression without a browser or network access.
func TestPanelAccountReplyMalformedOutcome(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: client response runtime regression not executed")
	}
	b, err := dockUI.ReadFile("dockui/dock.js")
	if err != nil {
		t.Fatal(err)
	}
	lf := strings.ReplaceAll(string(b), "\r\n", "\n")
	for name, js := range map[string]string{"LF": lf, "CRLF": strings.ReplaceAll(lf, "\n", "\r\n")} {
		t.Run(name, func(t *testing.T) { panelAccountReplyRuntime(t, node, js) })
	}
}

func panelAccountReplyRuntime(t *testing.T, node, js string) {
	t.Helper()
	// Git's Windows checkout can embed CRLF. Normalize only this test's
	// extraction input; execute the same client functions and assertions.
	js = strings.ReplaceAll(js, "\r\n", "\n")
	source := "const assert=require('node:assert/strict');\n"
	for _, head := range []string{"function accountQuotaReply(d, id, step, recovery) {", "function accountQuotaPost(id, step, form, recovery) {", "function accountQuotaRequest(c, confirm, recovery) {"} {
		source += jsFunction(t, js, head) + "\n}\n"
	}
	source += `
let reply,posts,loads,focused;
const state={server:{accountQuota:true,accountSelectionReview:true,form:'a'.repeat(64)}};
function changesOn(){return true;} function render(){} function focusOn(x){focused=x;}
function announce(){} function load(){loads++;}
global.fetch=async(url,options)=>{posts++;
 assert.equal(url,'api/v1/'+(state.accountQuotaChange.recovery?'account-selections/':'accounts/')+id+'/'+(state.accountQuotaChange.preview?'confirm':'prepare'));
 assert.equal(JSON.parse(options.body).action,state.accountQuotaChange.recovery?'review_selection':'subscription_quota');
 assert.equal(options.method,'POST');assert.ok(options.signal instanceof AbortSignal);
 return{ok:true,json:async()=>reply};};
const id='b'.repeat(32),c={id};
const preview={action:'review_selection',key:id,title:'Review Example?',confirm:'Use unchanged selection',form:'c'.repeat(64),expiresAt:'2030-01-01T00:00:00Z',lines:[[{t:'Fixed fictional paths.'}]],lost:'Reviewed reply lost: outcome unknown.',refused:'Refused before publication.'};
async function run(raw,confirm=true,recovery=true){
 reply=raw;posts=0;loads=0;focused='';
 state.accountQuotaChange=confirm?{id,recovery,preview}:null;
 accountQuotaRequest(c,confirm,recovery);
 for(let i=0;i<10;i++)await new Promise(resolve=>setImmediate(resolve));
 assert.equal(posts,1,'exactly one POST; no automatic replay');
 assert.equal(state.accountQuotaChange.busy,false);
 assert.equal(focused,'quota-result-'+id);
 assert.equal(loads,confirm?1:0,'only existing saved-state reload after confirm');
 return state.accountQuotaChange;
}
(async()=>{
 for(const raw of [{schemaVersion:1},{schemaVersion:1,result:{outcome:'future_success',text:'Unknown'}},{schemaVersion:1,result:{outcome:'done'}},{schemaVersion:1,preview},{schemaVersion:1,preview,result:{outcome:'done',text:'Done'}}]){
  const got=await run(raw);assert.equal(got.preview,null);assert.equal(got.result.text,preview.lost);
 }
 for(const change of [{action:'subscription_quota'},{key:'d'.repeat(32)},{form:'bad'},{expiresAt:'bad'},{lines:[]},{lost:''},{lines:[[{at:'2030-01-01T00:00:00Z'}]]}]){
  const got=await run({schemaVersion:1,preview:{...preview,...change}},false);assert.equal(got.preview,null);assert.match(got.result.text,/could not be prepared/);
 }
 let got=await run({schemaVersion:1,result:{outcome:'done',text:'Selection updated.'}});assert.equal(got.result.text,'Selection updated.');
 got=await run({schemaVersion:1,result:{outcome:'done',text:'Quota saved.'}},true,false);assert.equal(got.result.text,'Quota saved.');
 got=await run({schemaVersion:1,result:{outcome:'refused',text:'No write.'}},false);assert.equal(got.result.text,'No write.');
 const quota={...preview,action:'subscription_quota'};
 assert.equal(accountQuotaReply({schemaVersion:1,preview:quota},id,'prepare',false).preview,quota);
 assert.equal(accountQuotaReply({schemaVersion:1,preview},id,'prepare',true).preview,preview);
 console.log('Malformed confirm/unknown outcome/prepare validation and no replay PASS');
})().catch(e=>{console.error(e);process.exitCode=1;});
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=commonjs", "-")
	cmd.Stdin = bytes.NewBufferString(source)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("client regression: %v\n%s", err, out)
	}
	t.Log(string(out))
}
