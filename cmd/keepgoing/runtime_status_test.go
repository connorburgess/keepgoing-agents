package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestRuntimeSummarySeparatesControlsAndHistory(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                 string
		ctl                  control.File
		change               func(*state.File)
		inWindow, configured bool
		want, source         string
	}{
		{"active history", control.File{Enabled: true}, nil, true, true, "recorded_active", "last_check"},
		{"pending interaction", control.File{Enabled: true}, func(s *state.File) { s.LastCheck.AwaitingInput = true }, true, true, "recorded_awaiting_input", "last_check"},
		{"unknown outcome", control.File{Enabled: true}, func(s *state.File) { s.LastCheck.Outcome = "new-outcome" }, true, true, "unknown", "last_check"},
		{"stop wins", control.File{Stop: true, Paused: true, Completed: true}, nil, true, true, "stop_requested", "control"},
		{"disabled", control.File{}, nil, true, true, "disabled", "control"},
		{"paused", control.File{Enabled: true, Paused: true}, nil, true, true, "paused", "control"},
		{"complete", control.File{Enabled: true, Completed: true}, nil, true, true, "completed", "control"},
		{"await user", control.File{Enabled: true, AwaitingUser: true}, nil, true, true, "awaiting_user", "control"},
		{"window", control.File{Enabled: true}, nil, false, true, "outside_window", "configuration"},
		{"missing target", control.File{Enabled: true}, nil, true, false, "unknown", "configuration"},
		{"old format", control.File{Enabled: true}, func(s *state.File) { s.LastCheck = nil }, true, true, "unknown", "unavailable"},
		{"stale", control.File{Enabled: true}, func(s *state.File) { s.CheckedAt = now.Add(-time.Hour) }, true, true, "unknown", "unavailable"},
		{"future", control.File{Enabled: true}, func(s *state.File) { s.CheckedAt = now.Add(time.Hour) }, true, true, "unknown", "unavailable"},
		{"dry run", control.File{Enabled: true}, func(s *state.File) { s.LastCheck.DryRun = true; s.LastCheck.Outcome = "dry_run" }, true, true, "dry_run", "last_check"},
		{"failed send", control.File{Enabled: true}, func(s *state.File) { s.LastCheck.Outcome = "failed" }, true, true, "check_failed", "last_check"},
		{"after retarget", control.File{Enabled: true}, func(s *state.File) { s.ActiveTarget = "fallback-a" }, true, true, "unknown", "unavailable"},
		{"after handoff", control.File{Enabled: true}, func(s *state.File) { s.ActiveTarget = "fallback-a"; s.LastCheck.Target = "fallback-a" }, true, true, "not_observed", "last_check"},
		{"resume after hold", control.File{Enabled: true}, func(s *state.File) {
			s.LastCheck.ObservedTarget = ""
			s.LastCheck.ObservedState = "not observed"
			s.LastCheck.Reason = "completed"
		}, true, true, "not_observed", "last_check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := state.File{CheckedAt: now, LastState: "active", LastCheck: &state.Check{At: now, Version: "v1.2.3", Target: "primary", ObservedTarget: "primary", ObservedState: "active", Action: "none", Reason: "active", Outcome: "succeeded"}}
			if tc.change != nil {
				tc.change(&st)
			}
			got := describeRuntime(tc.ctl, st, tc.inWindow, tc.configured, now, 30*time.Minute, "v1.2.3")
			if got.State != tc.want || got.Source != tc.source || !strings.HasPrefix(got.NextCheck, "unknown:") {
				t.Fatalf("%+v", got)
			}
			if tc.name == "resume after hold" && got.Reason == "completed" {
				t.Fatal("cleared completion hold presented as the current reason")
			}
		})
	}
}

func TestBuildComparisonIsHistoricalAndPrivate(t *testing.T) {
	for _, tc := range []struct{ recorded, current, want, status string }{
		{"v1", "v1", "match", "ok"}, {"old", "new", "different", "warning"}, {"", "v1", "unknown", "unavailable"}, {"dev", "dev", "unknown", "unavailable"}, {"v1", "dev", "unknown", "unavailable"},
	} {
		check := &state.Check{Version: tc.recorded}
		if got := compareCheckVersion(check, tc.current); got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
		r := doctorReport{Version: tc.current}
		doctorCheckVersion(&r, check, true)
		if doctorCheckByID(t, r, "supervisor.version").Status != tc.status {
			t.Fatalf("%+v", r)
		}
	}
	r := doctorReport{Version: "new"}
	doctorCheckVersion(&r, &state.Check{Version: doctorPrivate}, true)
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), doctorPrivate) {
		t.Fatal("doctor exposed raw build history")
	}
}

func TestStatusOldStateMissingTargetAndNoWrites(t *testing.T) {
	f := newDoctorFixture(t)
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	if err := control.Save(f.state, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(f.state, state.File{ActiveTarget: "removed-fallback", CheckedAt: now, LastState: "active"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(f.state, state.FileName))
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"status", "-c", f.config, "--json", "--now", now.Format(time.RFC3339)}, &out, &errOut)
	if code != 0 {
		t.Fatalf("%d %s", code, &errOut)
	}
	var s statusOut
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.TargetConfigured || s.TargetAgent.Adapter != "" || s.Runtime.State != "unknown" || s.TaskSource != "inline" {
		t.Fatalf("%+v", s)
	}
	after, _ := os.ReadFile(filepath.Join(f.state, state.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("status changed history")
	}
	if strings.Contains(out.String(), doctorPrivate) {
		t.Fatal("task text leaked")
	}
}

func TestStatusTextDescribesDryRunAndHolds(t *testing.T) {
	f := newDoctorFixture(t)
	now := time.Now().UTC()
	if err := control.Save(f.state, control.File{Enabled: true, Completed: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(f.state, state.File{CheckedAt: now, LastCheck: &state.Check{At: now, Version: "example-build", Target: "primary", ObservedTarget: "primary", ObservedState: "idle", Action: "wake", Reason: "idle", Outcome: "dry_run", DryRun: true}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(f.config)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if status(env{context.Background(), &out, &out}, cfg, now, false, defaultStaleAfter) != 0 {
		t.Fatal(out.String())
	}
	for _, fragment := range []string{"activity:  completed (source: control)", "outcome dry_run", "the decision was not executed", "next check: unknown:", "quota group codex", "thread:    thr_example"} {
		if !strings.Contains(out.String(), fragment) {
			t.Errorf("missing %q in %s", fragment, &out)
		}
	}
}
