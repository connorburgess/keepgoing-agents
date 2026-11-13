//go:build unix

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// status and doctor say the loop's scheduler may have stopped once no check
// has run for longer than the overdue rule allows, from the record alone.
// They name a BB automation only when the last check said it was started by
// one; otherwise the generic sentence.
func TestStatusAndDoctorSayTheScheduleMayHaveStopped(t *testing.T) {
	for _, tc := range []struct {
		name      string
		startedBy *state.Routine
		want      string
	}{
		{"started by a BB automation", &state.Routine{Source: state.RoutineBBAutomation, Routine: "auto_example1"}, "the BB automation that runs this loop (auto_example1) may have disabled itself: check it in BB's Automations"},
		{"no scheduler identity", nil, "check the scheduler that runs `keepgoing tick` for this loop"},
		{"another scheduler", &state.Routine{Source: "example-scheduler", Routine: "nightly"}, "check the scheduler that runs `keepgoing tick` for this loop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if code, _, errs := f.run("enable"); code != 0 {
				t.Fatal(errs)
			}
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			last := time.Now().Add(-3*time.Hour - 10*time.Minute).UTC()
			if err := state.Save(cfg.Loop.StateDir, state.File{CheckedAt: last, RecentCheckGaps: []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}, StartedBy: tc.startedBy}); err != nil {
				t.Fatal(err)
			}
			_, out, _ := f.run("status")
			if !strings.Contains(out, "schedule:  no check has run for 3h10m; checks normally run every 10m, so the loop's scheduler may have stopped") || !strings.Contains(out, tc.want) {
				t.Fatalf("status:\n%s", out)
			}
			_, out, _ = f.run("status", "--json")
			var st struct {
				Schedule scheduleStatus `json:"schedule"`
			}
			if json.Unmarshal([]byte(out), &st) != nil || !st.Schedule.Overdue || !strings.Contains(st.Schedule.Message, tc.want) {
				t.Fatalf("status --json: %s", out)
			}
			_, out, _ = f.run("doctor", "--json", "--offline")
			if !strings.Contains(out, `"supervisor.schedule"`) || !strings.Contains(out, tc.want) {
				t.Fatalf("doctor: %s", out)
			}
		})
	}
}

// A disabled loop, a loop that has never run a check, and one checked
// recently say nothing about the schedule.
func TestStatusSaysNothingWhenTheScheduleIsNotOverdue(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := state.File{CheckedAt: time.Now().Add(-48 * time.Hour).UTC(), RecentCheckGaps: []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}}
	if err := state.Save(cfg.Loop.StateDir, old); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := f.run("status"); strings.Contains(out, "schedule:") {
		t.Fatalf("a disabled loop was called overdue:\n%s", out)
	}
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	if err := state.Save(cfg.Loop.StateDir, state.File{CheckedAt: time.Now().Add(-5 * time.Minute).UTC(), RecentCheckGaps: []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := f.run("status"); strings.Contains(out, "schedule:") {
		t.Fatalf("a recent check was called overdue:\n%s", out)
	}
}
