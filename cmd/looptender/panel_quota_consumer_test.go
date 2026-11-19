package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPanelAccountQuotaClientCapabilityAndProvenance(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: client contract execution not performed")
	}
	js := strings.ReplaceAll(dockUIText(t, "dock.js"), "\r\n", "\n")
	source := "const assert=require('node:assert/strict');const state={server:{accountQuota:true}};\n"
	for _, head := range []string{"function accountQuotaSupported(c) {", "function accountQuotaSummary(c) {"} {
		source += jsFunction(t, js, head) + "\n}\n"
	}
	source += `
const claude={provider:'claude-code'},codex={provider:'codex'};
assert.equal(accountQuotaSupported(claude),false);assert.equal(accountQuotaSupported(codex),true);
state.server.accountQuotaProviders=['codex'];assert.equal(accountQuotaSupported(claude),false);
state.server.accountQuotaProviders=['codex','claude-code'];assert.equal(accountQuotaSupported(claude),true);
state.server.accountQuota=false;assert.equal(accountQuotaSupported(claude),false);
const c={state:'reported',source:'claude:get_usage',retrievedAt:'2030-03-04T12:00:00Z',freshness:'provider_unknown',rows:[]};
claude.quota={summary:c};assert.equal(accountQuotaSummary(claude),c);
for(const bad of [{freshness:'fresh'},{source:'codex account/rateLimits/read'},{retrievedAt:null},{retrievedAt:'not-a-date'},{observedAt:'2030-03-04T11:00:00Z'},{state:'unavailable'}]){
 claude.quota={summary:{...c,...bad}};assert.equal(accountQuotaSummary(claude),null);
}
codex.quota={summary:c};assert.equal(accountQuotaSummary(codex),null);
const x={state:'reported',source:'codex account/rateLimits/read',observedAt:'2030-03-04T12:00:00Z',freshness:'stale',rows:[]};
codex.quota={summary:x};assert.equal(accountQuotaSummary(codex),x);
codex.quota={summary:{...x,retrievedAt:c.retrievedAt}};assert.equal(accountQuotaSummary(codex),null);
console.log('Legacy/explicit provider capability and source/time/freshness contract PASS');
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=commonjs", "-")
	cmd.Stdin = bytes.NewBufferString(source)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("client contract: %v\n%s", err, out)
	}
	t.Log(string(out))
}
