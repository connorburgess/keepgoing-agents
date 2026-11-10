//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestStartPreservesHoldsAndClaimsOwner(t *testing.T) {
	for _, hold := range []string{"none", "paused", "stop", "awaiting", "completed"} {
		t.Run(hold, func(t *testing.T) {
			f := newFixture(t)
			t.Setenv("BB_THREAD_ID", "thr_start_owner")
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			before := control.File{Note: "fictional hold", Paused: hold == "paused", Stop: hold == "stop", AwaitingUser: hold == "awaiting", Completed: hold == "completed"}
			if err := control.Save(cfg.Loop.StateDir, before); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var errs bytes.Buffer
			ready := false
			code := startCommand(env{ctx, io.Discard, &errs}, []string{"--foreground", "-c", f.cfg}, func(got control.File) error {
				ready = true
				if !got.Enabled || got.Ownership == nil || got.Ownership.ThreadID != "thr_start_owner" || got.Ownership.Generation != 1 {
					t.Fatalf("not enabled/claimed: %+v", got)
				}
				got.Enabled, got.Ownership = false, nil
				got.UpdatedAt = before.UpdatedAt
				if !reflect.DeepEqual(got, before) {
					t.Fatalf("holds changed: %+v", got)
				}
				if _, err := daemoninfo.Query(ctx, cfg.Loop.StateDir); err != nil {
					t.Fatal(err)
				}
				if l, err := lock.AcquireNamed(cfg.Loop.StateDir, lock.Daemon); !errors.Is(err, lock.ErrHeld) {
					if l != nil {
						l.Release()
					}
					t.Fatalf("daemon lock: %v", err)
				}
				cancel()
				return nil
			})
			if code != 0 || !ready || f.read("calls") != "" || f.read("probes") != "" {
				t.Fatalf("start: %d ready=%v errors=%s", code, ready, &errs)
			}
			if _, err := os.Stat(filepath.Join(cfg.Loop.StateDir, daemoninfo.FileName)); !os.IsNotExist(err) {
				t.Fatalf("endpoint survived: %v", err)
			}
		})
	}
}

func TestStartRefusesBeforeEnabling(t *testing.T) {
	for _, kind := range []string{"daemon", "tick", "control", "owner", "task", "cli", "workdir", "account", "process", "state", "target", "health", "diagnostics", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			dir := cfg.Loop.StateDir
			ctl := control.File{Paused: true, Completed: true, Note: "fictional hold", Ownership: &control.Ownership{ThreadID: "thr_existing", Generation: 1}}
			if err := control.Save(dir, ctl); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := exitFailed
			switch kind {
			case "daemon", "tick", "control":
				l, err := lock.AcquireNamed(dir, kind)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Release()
				want = exitSkipped
			case "owner":
				t.Setenv("BB_THREAD_ID", "thr_conflicting")
			case "task":
				cfg.Loop.PromptFile = f.dir // non-regular file, never read
			case "cli":
				cfg.Loop.BB.CLI = filepath.Join(f.dir, "absent")
			case "workdir", "account":
				cfg.Loop.Agent = config.Agent{Adapter: "codex", Codex: &config.Codex{CLI: filepath.Join(f.dir, "bin", "codex"), Workdir: f.dir}}
				if kind == "workdir" {
					cfg.Loop.Codex.Workdir = filepath.Join(f.dir, "missing")
				} else {
					cfg.Loop.Codex.Env = map[string]string{"CODEX_HOME": filepath.Join(f.dir, "missing")}
				}
			case "process":
				cfg.Loop.Agent = config.Agent{Adapter: "process", Process: &config.Process{StatusCommand: []string{filepath.Join(f.dir, "bin", "bb")}, ResumeCommand: []string{filepath.Join(f.dir, "missing")}}}
			case "state":
				write(t, filepath.Join(dir, state.FileName), "broken", 0o600)
			case "target":
				if err := state.Save(dir, state.File{ActiveTarget: "removed"}); err != nil {
					t.Fatal(err)
				}
			case "health":
				write(t, filepath.Join(dir, "health.json"), "broken", 0o600)
			case "diagnostics":
				if err := os.Mkdir(filepath.Join(dir, daemoninfo.FileName), 0o700); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			before, _ := os.ReadFile(filepath.Join(dir, control.FileName))
			var errs bytes.Buffer
			code := startForeground(env{ctx, io.Discard, &errs}, cfg, f.cfg, func(control.File) error { t.Error("unexpected readiness"); return nil })
			after, _ := os.ReadFile(filepath.Join(dir, control.FileName))
			if code != want || !bytes.Equal(before, after) || f.read("calls") != "" || f.read("probes") != "" {
				t.Fatalf("code=%d want=%d mutated=%v errs=%s", code, want, !bytes.Equal(before, after), &errs)
			}
		})
	}
}

func TestStartFlagsAndBackgroundLaunchFailure(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"--foreground", "--background"}, {"--take-over"}, {"--dry-run"}, {"unexpected"}, {"--now", "2026-01-01T00:00:00Z"}} {
		code, _, _ := f.run(append([]string{"start"}, args...)...)
		if code != exitUsage {
			t.Fatalf("args %v code %d", args, code)
		}
	}
	var errs bytes.Buffer
	code := startBackground(env{context.Background(), io.Discard, &errs}, filepath.Join(f.dir, "missing"), f.cfg)
	if code != exitFailed || !strings.Contains(errs.String(), "cannot launch") {
		t.Fatalf("code %d: %s", code, &errs)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "state", control.FileName)); !os.IsNotExist(err) {
		t.Fatalf("launch failure wrote control: %v", err)
	}
}
