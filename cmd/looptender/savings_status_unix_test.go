//go:build unix

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Through the real command: status shows the figures from the checks that
// ran, and reading them calls nothing.
func TestSavingsThroughStatus(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.context"), `{"usage":{"estimated":true,"modelContextWindow":200000,"usedTokens":48000}}`, 0o600)
	if v := f.tick(); v["action"] != "wake" {
		t.Fatalf("wake: %v", v)
	}
	f.run("pause")
	f.tick()
	calls := f.read("calls")
	_, text, _ := f.run("status")
	_, out, _ := f.run("status", "--json")
	if f.read("calls") != calls {
		t.Fatal("status called bb")
	}
	if !strings.Contains(text, "savings:   last 7 UTC days, measured from looptender's records") || !strings.Contains(text, "  sends: 1 send: 1 wake\n") {
		t.Fatalf("status:\n%s", text)
	}
	var v struct {
		Savings savingsStatus `json:"savings"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Savings.Version != 1 || *v.Savings.Sends.Total != 1 ||
		v.Savings.ChecksSentNothing.ByReason[0].Reason != "paused" || *v.Savings.WithheldWakes.Total != 0 ||
		v.Savings.ContextNow.Availability != "recorded" || *v.Savings.ContextNow.UsedTokens != 48000 ||
		v.Savings.Estimate.Availability != "recorded" || *v.Savings.Estimate.HighTokens != 0 {
		t.Fatalf("status --json savings: %+v %v", v.Savings, err)
	}
}
