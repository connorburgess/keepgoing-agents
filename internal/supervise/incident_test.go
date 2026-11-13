package supervise

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func TestIncidentTickJourney(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Agent.Adapter = "bb"
	p := w.primary(decide.Observation{State: decide.Active, ErrorClass: decide.ErrNetwork, Retrying: true})
	tickAt := func(n int) {
		t.Helper()
		opts := w.opts(t0.Add(time.Duration(n) * time.Minute))
		opts.Version = "abcdef1"
		if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
			t.Fatal(err)
		}
	}
	tickAt(0)
	p.obs = decide.Observation{State: decide.Active, RetryUnverified: true}
	tickAt(1)
	p.obs = decide.Observation{State: decide.Active}
	tickAt(2)
	p.obs = decide.Observation{State: decide.Active, AwaitingChildren: []string{"thr_private_marker"}}
	tickAt(3)
	p.obs = decide.Observation{State: decide.Idle}
	tickAt(4)
	j, err := incident.Load(w.dir)
	if err != nil || len(j.Records) != 5 {
		t.Fatal(j, err)
	}
	if r := j.Records[0]; r.Observation.Retry != "retrying" || r.Observation.ErrorClass != "network" || r.Cause != "retrying" || !r.Decisions.Evaluations[0].Input.Retrying {
		t.Fatal(r)
	}
	if r := j.Records[1]; r.Observation.Retry != "unverified" || !r.Decisions.Evaluations[0].Input.RetryUnverified {
		t.Fatal(r)
	}
	if r := j.Records[2]; r.LastActiveRecoveryAt == nil || r.Observation.State != "active" {
		t.Fatal(r)
	}
	if r := j.Records[3]; r.Action != "await_user" || r.ControlBefore.AwaitingUser || !r.ControlAfter.AwaitingUser || r.Observation.AwaitingChildren != 1 {
		t.Fatal(r)
	}
	if r := j.Records[4]; r.Observation.Availability != "not_observed" || r.Cause != "awaiting_user" {
		t.Fatal(r)
	}
	b, _ := os.ReadFile(filepath.Join(w.dir, incident.FileName))
	if strings.Contains(string(b), "thr_private_marker") {
		t.Fatal("child identifier leaked")
	}
}

func TestIncidentWriteFailurePreservesHoldAndStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Active, AwaitingChildren: []string{"thr_child"}})
		if stop {
			if _, err := control.Update(w.dir, func(c *control.File) { c.Stop = true }); err != nil {
				t.Fatal(err)
			}
		}
		l, err := lock.AcquireNamed(w.dir, "incident")
		if err != nil {
			t.Fatal(err)
		}
		r, err := Tick(context.Background(), w.cfg, w.opts(t0))
		l.Release()
		// The check's work was saved in state.json; only its history is
		// missing, which does not fail the check.
		if err != nil || r.History != "unavailable" || !r.Recorded || len(r.Warnings) == 0 {
			t.Fatal(r, err)
		}
		c, _ := control.Load(w.dir)
		if stop {
			if !c.Stop || p.stopped != 1 {
				t.Fatal("journal failure blocked stop")
			}
		} else if !c.AwaitingUser {
			t.Fatal("journal failure undid hold")
		}
	}
}

func TestIncidentFailedObservationContainsNoRawError(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Agent.Adapter = "process"
	p := w.primary(decide.Observation{})
	p.obsErr = errors.New("SECRET_PROVIDER_ERROR")
	_, err := Tick(context.Background(), w.cfg, w.opts(t0))
	if err == nil {
		t.Fatal("expected observe failure")
	}
	j, err := incident.Load(w.dir)
	if err != nil || len(j.Records) != 1 {
		t.Fatal(j, err)
	}
	r := j.Records[0]
	if r.Observation.Availability != "unavailable" || r.Outcome != "failed" || r.Decisions.Availability != "captured" || len(r.Decisions.Evaluations) != 0 {
		t.Fatal(r)
	}
	b, _ := os.ReadFile(filepath.Join(w.dir, incident.FileName))
	if strings.Contains(string(b), "SECRET_PROVIDER_ERROR") {
		t.Fatal("raw error leaked")
	}
}

func TestIncidentRealAdapterFixtures(t *testing.T) {
	for _, kind := range []string{"process", "claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "process" && runtime.GOOS == "windows" {
				t.Skip("POSIX status-command fixture")
			}
			w := newWorld(t, true, false)
			agent := config.Agent{Adapter: kind}
			switch kind {
			case "process":
				agent.Process = &config.Process{StatusCommand: []string{"sh", "-c", `printf '%s' '{"state":"error","errorClass":"network","text":"SECRET_BODY"}'`}, ResumeCommand: []string{"false"}}
			case "claude":
				agent.Claude = &config.Claude{CLI: "missing-fixture-provider", Workdir: t.TempDir()}
			case "codex":
				agent.Codex = &config.Codex{CLI: "missing-fixture-provider", Workdir: t.TempDir()}
			}
			w.cfg.Loop.Agent = agent
			if kind != "process" {
				dir := filepath.Join(w.dir, "targets", "primary")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				stdout := `{"type":"result","is_error":true,"subtype":"connection_error","result":"SECRET_BODY"}`
				if kind == "codex" {
					stdout = `{"type":"turn.failed","error":{"code":"connection_error","message":"SECRET_BODY"}}`
				}
				for name, body := range map[string]string{"run.json": `{"pid":0,"argv0":"fixture"}`, "exit": "1", "stdout": stdout} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			opts := w.opts(t0)
			opts.NewAdapter = nil
			opts.DryRun = true
			if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
				t.Fatal(err)
			}
			j, err := incident.Load(w.dir)
			if err != nil || len(j.Records) != 1 {
				t.Fatal(j, err)
			}
			r := j.Records[0]
			if r.Adapter != kind || r.Observation.ErrorClass != "network" || r.Observation.Retry != "unavailable" || !r.DryRun || len(r.Decisions.Evaluations) != 1 || r.Decisions.Evaluations[0].Input.ErrorClass != "network" {
				t.Fatal(r)
			}
			wantSource, wantCode := "process_json", "network"
			if kind == "claude" {
				wantSource, wantCode = "claude_result", "connection_error"
			}
			if kind == "codex" {
				wantSource, wantCode = "codex_events", "connection_error"
			}
			if r.Observation.Source != wantSource || r.Observation.ProviderCode != wantCode || r.Observation.ExitCode == nil {
				t.Fatal("source evidence lost in journal", r)
			}
			b, _ := os.ReadFile(filepath.Join(w.dir, incident.FileName))
			if strings.Contains(string(b), "SECRET_BODY") {
				t.Fatal("provider body leaked")
			}
		})
	}
}

func lastIncident(t *testing.T, w *world) incident.Record {
	t.Helper()
	j, err := incident.Load(w.dir)
	if err != nil || len(j.Records) == 0 {
		t.Fatal(j, err)
	}
	return j.Records[len(j.Records)-1]
}

func TestIncidentDecisionInputsExplainNudgeAndHold(t *testing.T) {
	w := newWorld(t, true, false)
	w.primary(decide.Observation{State: decide.Active})
	w.cfg.Loop.Progress.MaxNudges = 1
	w.cfg.Loop.Progress.Repositories = []string{"/example/repo"}
	opts := w.opts(t0)
	opts.Fingerprint = func([]string) (string, error) { return "same", nil }
	for i := 0; i < 4; i++ {
		opts.Now = t0.Add(time.Duration(i) * time.Hour)
		if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
			t.Fatal(err)
		}
	}
	j, err := incident.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	nudge, hold, held := j.Records[1], j.Records[2], j.Records[3]
	e := nudge.Decisions.Evaluations[0]
	if e.Action != "nudge" || e.Stage != "normal" || e.Input.Nudges != 0 || nudge.Nudges != 1 || e.Input.MaxNudges != 1 || e.Input.UnchangedFor != time.Hour || e.Input.StallAfter != 30*time.Minute || e.Input.MinWakeInterval != time.Hour || !e.Input.InWindow {
		t.Fatal("lost pre-action inputs", nudge)
	}
	e = hold.Decisions.Evaluations[0]
	if e.Action != "await_user" || e.Input.Nudges != e.Input.MaxNudges || e.Input.SinceWake != time.Hour || hold.ControlBefore.AwaitingUser || !hold.ControlAfter.AwaitingUser {
		t.Fatal(hold)
	}
	if held.Decisions.Availability != "captured" || len(held.Decisions.Evaluations) != 0 {
		t.Fatal("invented held evaluation", held)
	}
}

func TestIncidentEarlyDecisionInputs(t *testing.T) {
	for _, stage := range []string{"deadline", "progress_limit", "send_limit", "child_input", "stop"} {
		t.Run(stage, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(decide.Observation{State: decide.Active})
			switch stage {
			case "deadline":
				w.cfg.Loop.Limits.Deadline = t0.Format(time.RFC3339)
			case "progress_limit":
				progressTick(t, w, t0.Add(-time.Hour), "same")
			case "send_limit":
				w.cfg.Loop.Limits.MaxSends = 1
				p.obs.State = decide.Idle
				runTick(t, w, t0.Add(-time.Hour))
			case "child_input":
				p.obs.AwaitingChildren = []string{"thr_secret_fixture"}
			case "stop":
				if _, err := control.Update(w.dir, func(c *control.File) { c.Stop = true }); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "progress_limit" {
				progressTick(t, w, t0, "same")
			} else {
				runTick(t, w, t0)
			}
			r := lastIncident(t, w)
			if len(r.Decisions.Evaluations) != 1 {
				t.Fatal(r)
			}
			e := r.Decisions.Evaluations[0]
			if e.Stage != stage || e.Input.InWindow || e.Input.StallAfter != 0 {
				t.Fatal("early evaluation claimed full policy", e)
			}
			switch stage {
			case "deadline":
				if !e.Input.DeadlineReached {
					t.Fatal(e)
				}
			case "progress_limit":
				if !e.Input.NoProgressReached {
					t.Fatal(e)
				}
			case "send_limit":
				if !e.Input.SendLimitReached {
					t.Fatal(e)
				}
			case "child_input":
				if e.Input.AwaitingChildren != 1 {
					t.Fatal(e)
				}
			case "stop":
				if !e.Input.Control.Stop || e.Action != "stop" {
					t.Fatal(e)
				}
				runTick(t, w, t0.Add(time.Minute))
				r = lastIncident(t, w)
				if r.Action != "none" || r.Decisions.Evaluations[0].Action != "stop" || p.stopped != 1 {
					t.Fatal("lost stop dedup distinction", r)
				}
			}
		})
	}
}

func TestIncidentSwitchBackReevaluation(t *testing.T) {
	w, _, _ := switchBackWorld(t)
	runTick(t, w, t0.Add(2*time.Hour+time.Minute))
	r := lastIncident(t, w)
	e := r.Decisions.Evaluations
	if len(e) != 2 || e[0].Stage != "normal" || e[0].Action != "switch_back" || !e[0].Input.SwitchBackReady || e[1].Stage != "switch_back_failed" || e[1].Action != "wake" || e[1].Input.SwitchBackReady || r.Action != "wake" {
		t.Fatal(r)
	}
}
