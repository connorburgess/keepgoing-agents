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
	if r := j.Records[0]; r.Observation.Retry != "retrying" || r.Observation.ErrorClass != "network" || r.Cause != "retrying" {
		t.Fatal(r)
	}
	if r := j.Records[1]; r.Observation.Retry != "unverified" {
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
		if !errors.Is(err, incident.ErrUnavailable) || len(r.Warnings) == 0 {
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
	if r.Observation.Availability != "unavailable" || r.Outcome != "failed" {
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
			if r.Adapter != kind || r.Observation.ErrorClass != "network" || r.Observation.Retry != "unavailable" || !r.DryRun {
				t.Fatal(r)
			}
			b, _ := os.ReadFile(filepath.Join(w.dir, incident.FileName))
			if strings.Contains(string(b), "SECRET_BODY") {
				t.Fatal("provider body leaked")
			}
		})
	}
}
