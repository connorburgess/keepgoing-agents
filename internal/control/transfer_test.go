package control

import (
	"strings"
	"testing"
)

func TestPendingBBTransferProviderPlan(t *testing.T) {
	legacy := PendingBBTransfer{Version: 1, ID: "move_0123456789abcdef0123456789abcdef", From: "thr_example_source", To: "thr_example_destination", Before: strings.Repeat("a", 64), After: strings.Repeat("b", 64)}
	if !legacy.Valid() {
		t.Fatal("legacy recovery no longer supported")
	}
	cross := legacy
	cross.Version, cross.FromProvider, cross.ToProvider, cross.Group = 2, "claude-code", "codex", "example-pool"
	if !cross.Valid() {
		t.Fatal("explicit clear-label plan invalid")
	}
	cross.Model, cross.Reasoning = "example-model", "medium"
	if !cross.Valid() {
		t.Fatal("destination labels invalid")
	}
	for _, mutate := range []func(*PendingBBTransfer){
		func(p *PendingBBTransfer) { p.Version = 3 },
		func(p *PendingBBTransfer) { p.Version = 1 },
		func(p *PendingBBTransfer) { p.FromProvider = "" },
		func(p *PendingBBTransfer) { p.ToProvider = p.FromProvider },
		func(p *PendingBBTransfer) { p.Group = "" },
		func(p *PendingBBTransfer) { p.Reasoning = "unsupported value" },
	} {
		bad := cross
		mutate(&bad)
		if bad.Valid() {
			t.Fatal("unsupported provider plan accepted")
		}
	}
}
