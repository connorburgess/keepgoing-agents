//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/daemoninfo"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/state"
)

func TestDaemonShutdownPreservesControls(t *testing.T) {
	for _, hold := range []string{"enabled", "disabled", "paused", "stop", "awaiting", "completed"} {
		t.Run(hold, func(t *testing.T) {
			f := newFixture(t)
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctl := control.File{Enabled: hold != "disabled", Ownership: &control.Ownership{ThreadID: "thr_owner", Generation: 3}}
			ctl.Paused = hold == "paused"
			ctl.Stop = hold == "stop"
			ctl.AwaitingUser = hold == "awaiting"
			ctl.Completed = hold == "completed"
			if err := control.Save(cfg.Loop.StateDir, ctl); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
			t.Setenv("BB_THREAD_ID", "thr_owner")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan int, 1)
			var out, errs safeBuffer
			go func() { done <- daemon(env{ctx: ctx, stdout: &out, stderr: &errs}, cfg, f.cfg, true) }()
			waitFor(t, func() bool {
				s, e := daemoninfo.Query(context.Background(), cfg.Loop.StateDir)
				return e == nil && s.Shutdown && s.Phase == "waiting"
			}, "shutdown endpoint")
			code, _, msg := f.run("daemon-shutdown")
			if code != 0 {
				t.Fatalf("shutdown: %d %s", code, msg)
			}
			select {
			case code := <-done:
				if code != 0 {
					t.Fatal(code, errs.String())
				}
			case <-time.After(time.Second):
				t.Fatal("daemon did not join")
			}
			after, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
			if !bytes.Equal(before, after) {
				t.Fatal("controls changed")
			}
		})
	}
}

func TestDaemonShutdownBindingRefusals(t *testing.T) {
	for _, change := range []string{"caller", "owner", "target", "goal", "config", "busy", "retired"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctl := control.File{Enabled: true, Paused: true, Ownership: &control.Ownership{ThreadID: "thr_owner", Generation: 1}}
			if err := control.Save(cfg.Loop.StateDir, ctl); err != nil {
				t.Fatal(err)
			}
			lease := leaseOf(ctl)
			binding, err := shutdownBinding(cfg, f.cfg, "thr_owner", lease)
			if err != nil {
				t.Fatal(err)
			}
			s, err := daemoninfo.Start(cfg.Loop.StateDir, "fixture", false)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cleanup := enableDaemonShutdown(s, cfg, f.cfg, lease, cancel)
			defer cleanup()
			snap, err := daemoninfo.Query(context.Background(), cfg.Loop.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			caller := "thr_owner"
			switch change {
			case "caller":
				caller = "thr_other"
			case "owner":
				ctl.Ownership.Generation++
				control.Save(cfg.Loop.StateDir, ctl)
			case "target":
				st := state.File{}
				st.ActiveTarget = "codex-main"
				if err := state.Save(cfg.Loop.StateDir, st); err != nil {
					t.Fatal(err)
				}
			case "goal":
				os.WriteFile(filepath.Join(cfg.Loop.StateDir, control.GoalName), []byte(`{"version":900}`), 0600)
			case "config":
				b, _ := os.ReadFile(f.cfg)
				os.WriteFile(f.cfg, append(b, []byte("\n# changed\n")...), 0600)
			case "busy":
				l, e := lock.Acquire(cfg.Loop.StateDir)
				if e != nil {
					t.Fatal(e)
				}
				defer l.Release()
			case "retired":
				ctl.SupersededBy = "fictional-successor"
				control.Save(cfg.Loop.StateDir, ctl)
			}
			before, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
			if daemoninfo.Shutdown(context.Background(), cfg.Loop.StateDir, snap, binding, caller) == nil {
				t.Fatal("accepted", change)
			}
			if ctx.Err() != nil {
				t.Fatal("refusal cancelled daemon")
			}
			after, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed controls")
			}
		})
	}
}

func TestDaemonShutdownRetainsCheckLockUntilJoin(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctl := control.File{Paused: true}
	if err := control.Save(cfg.Loop.StateDir, ctl); err != nil {
		t.Fatal(err)
	}
	binding, err := shutdownBinding(cfg, f.cfg, "", leaseOf(ctl))
	if err != nil {
		t.Fatal(err)
	}
	s, err := daemoninfo.Start(cfg.Loop.StateDir, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := enableDaemonShutdown(s, cfg, f.cfg, leaseOf(ctl), cancel)
	defer cleanup()
	snap, err := daemoninfo.Query(context.Background(), cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := daemoninfo.Shutdown(context.Background(), cfg.Loop.StateDir, snap, binding, ""); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("not cancelled")
	}
	if busy, err := lock.Held(cfg.Loop.StateDir, lock.Tick); err != nil || !busy {
		t.Fatal("acceptance released check lock", busy, err)
	}
	if err := daemoninfo.Shutdown(context.Background(), cfg.Loop.StateDir, snap, binding, ""); err == nil {
		t.Fatal("replayed request accepted")
	}
	cleanup()
	if busy, err := lock.Held(cfg.Loop.StateDir, lock.Tick); err != nil || busy {
		t.Fatal("join retained check lock", busy, err)
	}
}

func TestDaemonShutdownUnverifiedExitIsBounded(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Save(cfg.Loop.StateDir, control.File{Paused: true}); err != nil {
		t.Fatal(err)
	}
	s, err := daemoninfo.Start(cfg.Loop.StateDir, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// An acknowledgement alone must not certify that a daemon joined.
	s.EnableShutdown(func(daemoninfo.ShutdownRequest) error { return nil })
	started := time.Now()
	code, _, msg := f.run("daemon-shutdown")
	if code == 0 || !strings.Contains(msg, "exit unverified") || time.Since(started) > 7*time.Second {
		t.Fatalf("unbounded or falsely completed shutdown: %d %s", code, msg)
	}
}
