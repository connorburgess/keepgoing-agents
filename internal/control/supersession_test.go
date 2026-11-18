package control

import (
	"encoding/json"
	"testing"
)

func TestSupersededLoopCannotResumeOrClaim(t *testing.T) {
	var f File
	if err := json.Unmarshal([]byte(`{"enabled":true,"supersededBy":"thr_example_successor"}`), &f); err != nil {
		t.Fatal(err)
	}
	if f.Gate().Enabled {
		t.Error("superseded loop still permits supervision")
	}
	if f.Summary() != "superseded" {
		t.Errorf("summary = %q", f.Summary())
	}
	if CheckTransferReady(f) == nil {
		t.Error("superseded loop permits resume/start")
	}
	if ClaimOwner(&f, "thr_example_successor", true) == nil {
		t.Error("takeover revives superseded loop")
	}
	f.Stop = true
	if !f.Gate().Stop {
		t.Error("supersession suppressed stop")
	}
}
