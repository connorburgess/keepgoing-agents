//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Status shows capped fallbacks and a stopped switch-back, and the operator's
// resume or retarget clears the counts so a repaired target is tried again.
func TestTakeoverBriefStatusAndOperatorReset(t *testing.T) {
	seed := func(t *testing.T) *fixture {
		t.Helper()
		f := newFixture(t)
		cfg, _ := os.ReadFile(f.cfg)
		write(t, f.cfg, string(cfg)+"  switch_back: {enabled: true}\n  errors: {max_reprompts: 2}\n", 0o600)
		if code, _, errs := f.run("enable"); code != exitOK {
			t.Fatal(errs)
		}
		at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
		if err := state.Save(filepath.Join(f.dir, "state"), state.File{
			ActiveTarget: "codex-main", LastHandoffAt: at, SwitchBackRejected: 2,
			HandoffBriefs: map[string]state.BriefRecord{"claude-other-account": {Failed: 2, LastAt: at}, "codex-main": {Failed: 1, LastAt: at}},
		}); err != nil {
			t.Fatal(err)
		}
		return f
	}
	f := seed(t)
	code, out, errs := f.run("status")
	if code != exitOK ||
		!strings.Contains(out, "handoff briefs: claude-other-account rejected 2 and will not be tried again this error episode; resume or retarget clears this\n") ||
		!strings.Contains(out, "switch-back: stopped after the primary rejected 2 briefs; resume, retarget or the next handoff restarts it\n") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	var status statusOut
	_, out, _ = f.run("status", "--json")
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	b := status.TakeoverBriefs
	if b.Cap != 2 || strings.Join(b.Capped, ",") != "claude-other-account" || b.SwitchBackRejected != 2 || !b.SwitchBackStopped {
		t.Fatalf("%+v", b)
	}

	for _, cmd := range [][]string{{"resume"}, {"retarget", "primary"}} {
		t.Run(cmd[0], func(t *testing.T) {
			f := seed(t)
			if code, _, errs := f.run(cmd...); code != exitOK {
				t.Fatal(errs)
			}
			st, err := state.Load(filepath.Join(f.dir, "state"))
			if err != nil || len(st.HandoffBriefs) != 0 || st.SwitchBackRejected != 0 {
				t.Fatalf("%s kept the counts: %+v %v", cmd[0], st, err)
			}
			_, out, _ := f.run("status")
			if strings.Contains(out, "handoff briefs:") || strings.Contains(out, "switch-back: stopped") {
				t.Fatalf("status after %s: %s", cmd[0], out)
			}
			var status statusOut
			_, out, _ = f.run("status", "--json")
			if err := json.Unmarshal([]byte(out), &status); err != nil || status.TakeoverBriefs.SwitchBackStopped || !strings.Contains(out, `"capped": []`) {
				t.Fatalf("%v %s", err, out)
			}
		})
	}
	// Pause, enable and other controls leave the counts alone.
	f = seed(t)
	for _, cmd := range []string{"pause", "enable"} {
		f.run(cmd)
	}
	if st, _ := state.Load(filepath.Join(f.dir, "state")); len(st.HandoffBriefs) != 2 || st.SwitchBackRejected != 2 {
		t.Fatalf("counts cleared by a control other than resume: %+v", st)
	}
}
