package supervise

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func TestDeadlinePersistsBeforeProviderWork(t *testing.T) {
	w := newWorld(t, true, true)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w.cfg.Loop.Limits.Deadline = now.Format(time.RFC3339)
	// A deadline must not depend on healthy providers, fingerprints or registry.
	if err := os.WriteFile(filepath.Join(w.dir, "health.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := w.opts(now)
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
		t.Fatal("provider constructed")
		return nil, nil
	}
	opts.Fingerprint = func([]string) (string, error) { t.Fatal("fingerprint queried"); return "", nil }
	w.cfg.Loop.Progress.Repositories = []string{"unused"}
	for i := 0; i < 3; i++ {
		result, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (result.Action != "await_user" || !strings.Contains(result.Reason, "deadline")) {
			t.Fatalf("first check: %+v", result)
		}
		if i > 0 && result.Reason != "awaiting user" {
			t.Fatalf("restart: %+v", result)
		}
	}
	if len(w.probes) != 0 || w.reportTypes() != "await_user" {
		t.Fatalf("probes=%v reports=%s", w.probes, w.reportTypes())
	}
	// A clock rollback or removing the deadline does not silently reopen a hold.
	w.cfg.Loop.Limits.Deadline = ""
	opts.Now = now.Add(-time.Hour)
	if result, err := Tick(context.Background(), w.cfg, opts); err != nil || result.Reason != "awaiting user" {
		t.Fatalf("hold lost: %+v %v", result, err)
	}
	// Clearing the hold with an expired deadline results in another hold, never work.
	w.cfg.Loop.Limits.Deadline = now.Format(time.RFC3339)
	opts.Now = now
	if _, err := control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false }); err != nil {
		t.Fatal(err)
	}
	if result, err := Tick(context.Background(), w.cfg, opts); err != nil || result.Action != "await_user" {
		t.Fatalf("resume bypass: %+v %v", result, err)
	}
}

func TestDeadlineDryRunAndExistingHolds(t *testing.T) {
	for _, hold := range []string{"dry-run", "disabled", "paused", "completed", "awaiting", "stop"} {
		t.Run(hold, func(t *testing.T) {
			w := newWorld(t, true, false)
			now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			w.cfg.Loop.Limits.Deadline = now.Format(time.RFC3339)
			_, err := control.Update(w.dir, func(f *control.File) {
				switch hold {
				case "disabled":
					f.Enabled = false
				case "paused":
					f.Paused = true
				case "completed":
					f.Completed = true
				case "awaiting":
					f.AwaitingUser = true
				case "stop":
					f.Stop = true
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(w.dir, control.FileName))
			opts := w.opts(now)
			opts.DryRun = hold == "dry-run"
			if hold == "stop" {
				w.adapters["primary"] = &fakeAdapter{obs: decide.Observation{State: decide.Active}}
			} else {
				opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
					t.Fatal("provider queried")
					return nil, nil
				}
			}
			result, err := Tick(context.Background(), w.cfg, opts)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(filepath.Join(w.dir, control.FileName))
			if string(before) != string(after) {
				t.Fatal("changed controls")
			}
			if hold == "stop" && (result.Action != "stop" || w.adapters["primary"].stopped != 1) {
				t.Fatalf("stop lost: %+v", result)
			}
			if hold == "dry-run" && result.Action != "await_user" {
				t.Fatalf("dry run: %+v", result)
			}
		})
	}
}

func TestDeadlineAllowsWorkBeforeCutoffAndPollsWithoutProvider(t *testing.T) {
	w := newWorld(t, true, false)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w.cfg.Loop.Limits.Deadline = now.Format(time.RFC3339)
	result, err := Tick(context.Background(), w.cfg, w.opts(now.Add(-time.Nanosecond)))
	if err != nil || result.Action != "wake" || len(w.adapters["primary"].sent) != 1 {
		t.Fatalf("before cutoff: %+v %v", result, err)
	}
	w.cfg.Loop.Limits.Deadline = "2000-01-01T00:00:00Z"
	factory := func(config.Agent, string, string) (adapter.Adapter, error) {
		t.Fatal("expired poll queried provider")
		return nil, nil
	}
	if PollWake(context.Background(), w.cfg, factory) {
		t.Fatal("expired poll requested a wake")
	}
	if _, err := Tick(context.Background(), w.cfg, w.opts(now)); err != nil {
		t.Fatal(err)
	}
	if PollWake(context.Background(), w.cfg, factory) {
		t.Fatal("held poll should not request check")
	}
}
