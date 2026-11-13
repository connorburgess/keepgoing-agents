//go:build unix

package main

import (
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
		{"started by a BB automation", &state.Routine{Source: state.RoutineBBAutomation, Routine: "auto_example1"}, "the computer may have slept, or the BB automation that runs this loop (auto_example1) may have disabled itself; check it in BB's Automations"},
		{"no scheduler identity", nil, "the computer may have slept, or the scheduler that runs `keepgoing tick` for this loop may have stopped"},
		{"another scheduler", &state.Routine{Source: "example-scheduler", Routine: "nightly"}, "the computer may have slept, or the scheduler that runs `keepgoing tick` for this loop may have stopped"},
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
			backdateControl(t, cfg.Loop.StateDir, last.Add(-time.Hour))
			if err := state.Save(cfg.Loop.StateDir, state.File{CheckedAt: last, ScheduleGaps: []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}, StartedBy: tc.startedBy}); err != nil {
				t.Fatal(err)
			}
			_, out, _ := f.run("status")
			if !strings.Contains(out, "schedule:  no check has run for 3h10m (checks normally run every 10m): ") || !strings.Contains(out, tc.want) {
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
	old := state.File{CheckedAt: time.Now().Add(-48 * time.Hour).UTC(), ScheduleGaps: []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}}
	if err := state.Save(cfg.Loop.StateDir, old); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := f.run("status"); strings.Contains(out, "schedule:") {
		t.Fatalf("a disabled loop was called overdue:\n%s", out)
	}
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	if err := state.Save(cfg.Loop.StateDir, state.File{CheckedAt: time.Now().Add(-5 * time.Minute).UTC(), ScheduleGaps: []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := f.run("status"); strings.Contains(out, "schedule:") {
		t.Fatalf("a recent check was called overdue:\n%s", out)
	}
}

// backdateControl sets control.json's updatedAt, which control.Save always
// stamps with the current time, as if the last change were made at at.
func backdateControl(t *testing.T, dir string, at time.Time) {
	t.Helper()
	path := filepath.Join(dir, control.FileName)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	doc["updatedAt"] = at.UTC().Format(time.RFC3339Nano)
	if b, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A paused loop is not judged; status --json says so and gives no usual
// interval or threshold. A loop with fewer than three gaps is not judged
// against a usual interval: doctor says how many it has seen, and it is
// called stopped only after the day-long fallback.
func TestScheduleIsNotClaimedWithoutGrounds(t *testing.T) {
	f := newFixture(t)
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	if code, _, errs := f.run("pause"); code != 0 {
		t.Fatal(errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	last := time.Now().Add(-72 * time.Hour).UTC()
	backdateControl(t, cfg.Loop.StateDir, last.Add(-time.Hour))
	if err := state.Save(cfg.Loop.StateDir, state.File{CheckedAt: last, ScheduleGaps: []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	_, out, _ := f.run("status", "--json")
	var st struct {
		Schedule map[string]any `json:"schedule"`
	}
	if json.Unmarshal([]byte(out), &st) != nil || st.Schedule["overdue"] != false || st.Schedule["judging"] != false || st.Schedule["notJudged"] != "held" || st.Schedule["usual"] != nil || st.Schedule["threshold"] != nil {
		t.Fatalf("a paused loop: %s", out)
	}
	if code, _, errs := f.run("resume"); code != 0 {
		t.Fatal(errs)
	}
	backdateControl(t, cfg.Loop.StateDir, last.Add(-time.Hour))
	for _, tc := range []struct {
		since time.Duration
		want  string
	}{
		{2 * time.Hour, `Not judged against its usual interval yet: 2 of 3 gaps between checks seen. Until then it is called stopped only after 24h without a check; the last one was 2h ago.`},
		{30 * time.Hour, `no check has run for 30h (its usual interval is not known yet, so it is called stopped after 24h)`},
	} {
		if err := state.Save(cfg.Loop.StateDir, state.File{CheckedAt: time.Now().Add(-tc.since).UTC(), ScheduleGaps: []time.Duration{10 * time.Minute, 10 * time.Minute}}); err != nil {
			t.Fatal(err)
		}
		if _, out, _ := f.run("doctor", "--json", "--offline"); !strings.Contains(out, tc.want) {
			t.Fatalf("doctor after %s: %s", tc.since, out)
		}
	}
}
