//go:build unix

package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Include cursors, health, history, and budgets as well as control/state;
// merely acquiring an advisory lock is not a runtime-state mutation.
func snapshotRuntime(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(path, ".lock") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProviderOperationsRejectSnapshotChangedUnderTickLock(t *testing.T) {
	for _, source := range []string{"config", "fallbacks"} {
		for _, remove := range []bool{false, true} {
			for _, op := range []string{"tick", "poll", "retarget", "probes"} {
				t.Run(source+"/"+op+map[bool]string{false: "/edit", true: "/remove"}[remove], func(t *testing.T) {
					f := newFixture(t)
					if code, _, msg := f.run("enable"); code != exitOK {
						t.Fatal(msg)
					}
					old := loadFixture(t, f.cfg)
					if err := state.Save(old.Loop.StateDir, state.File{Reprompts: 2}); err != nil {
						t.Fatal(err)
					}
					var out bytes.Buffer
					invoke := func(cfg *config.Config) int {
						e := env{context.Background(), &out, &out}
						switch op {
						case "tick":
							return tickOnce(e, cfg, time.Now(), false, false)
						case "poll":
							cfg.Loop.Daemon.ControlPoll.Duration = 2 * time.Second
							if pollWake(context.Background(), cfg) {
								t.Fatal("unexpected early wake")
							}
							return exitSkipped
						case "retarget":
							return retarget(e, cfg, "claude-other-account", "fictional target selection", false)
						default:
							return fallbacksCheck(e, cfg, time.Now(), "", false, true, false)
						}
					}
					// Deterministic interleaving: read old snapshot, writer wins tick.lock,
					// reader contends, writer replaces source and releases, reader retries.
					held, err := lock.Acquire(old.Loop.StateDir)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { held.Release() })
					if invoke(old) != exitSkipped {
						t.Fatal("operation ignored held lock")
					}
					path := f.cfg
					if source == "fallbacks" {
						path = old.Loop.Fallbacks
					}
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					changed := strings.ReplaceAll(string(raw), "thr_primary", "thr_updated")
					changed = strings.ReplaceAll(changed, "thr_alt", "thr_replacement")
					if remove {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					} else {
						write(t, path, changed, 0600)
					}
					if err := held.Release(); err != nil {
						t.Fatal(err)
					}
					before := snapshotRuntime(t, old.Loop.StateDir)
					out.Reset()
					if invoke(old) != exitSkipped {
						t.Fatalf("stale operation accepted: %s", &out)
					}
					if op != "poll" && !strings.Contains(out.String(), "changed or is unreadable") {
						t.Fatalf("missing rejection: %s", &out)
					}
					if f.read("calls") != "" || f.read("probes") != "" || f.hooks.types() != "" {
						t.Fatal("rejected operation performed provider work or reported")
					}
					if !reflect.DeepEqual(before, snapshotRuntime(t, old.Loop.StateDir)) {
						t.Fatal("rejected operation changed runtime records")
					}
					if remove {
						write(t, path, changed, 0600)
					}
					fresh := loadFixture(t, f.cfg)
					out.Reset()
					code := invoke(fresh)
					if op != "poll" && code != exitOK {
						t.Fatalf("fresh operation failed: %d %s", code, &out)
					}
					if op == "probes" {
						if f.read("probes") == "" {
							t.Fatal("fresh snapshot did not probe")
						}
					} else if f.read("calls") == "" {
						t.Fatal("fresh snapshot did not reach provider")
					}
					if op == "tick" && !strings.Contains(f.read("calls"), "thread tell ") {
						t.Fatal("fresh tick did not send")
					}
					if op == "poll" && !strings.Contains(f.read("calls"), "thread log ") {
						t.Fatal("fresh poll did not read notifications")
					}
					if source == "config" && (op == "tick" || op == "poll") && strings.Contains(f.read("calls"), "thr_primary") {
						t.Fatal("fresh operation queried old target")
					}
					if source == "fallbacks" && op == "retarget" && !strings.Contains(f.read("calls"), "thread update thr_replacement") {
						t.Fatal("fresh retarget did not apply new target model")
					}
				})
			}
		}
	}
}

func TestSnapshotRejectionPreservesHoldsAndDefersStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "pause", true: "stop-over-pause"}[stop], func(t *testing.T) {
			f := newFixture(t)
			cfg := loadFixture(t, f.cfg)
			ctl := control.File{Enabled: true, Paused: true, Stop: stop, Completed: true, AwaitingUser: true, Note: "fictional hold"}
			if err := control.Save(cfg.Loop.StateDir, ctl); err != nil {
				t.Fatal(err)
			}
			ctl, err := control.Load(cfg.Loop.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0600)
			raw, err := os.ReadFile(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			write(t, f.cfg, string(raw)+"# changed\n", 0600)
			before := snapshotRuntime(t, cfg.Loop.StateDir)
			var out bytes.Buffer
			e := env{context.Background(), &out, &out}
			if tickOnce(e, cfg, time.Now(), false, false) != exitSkipped || pollWake(context.Background(), cfg) {
				t.Fatal("stale config accepted")
			}
			if !reflect.DeepEqual(before, snapshotRuntime(t, cfg.Loop.StateDir)) || f.read("calls") != "" || f.read("probes") != "" {
				t.Fatal("stale work touched provider or holds")
			}
			out.Reset()
			if tickOnce(e, loadFixture(t, f.cfg), time.Now(), false, false) != exitOK {
				t.Fatal(out.String())
			}
			after, err := control.Load(cfg.Loop.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ctl, after) {
				t.Fatalf("holds changed: %+v", after)
			}
			if stop {
				if !strings.Contains(f.read("calls"), "thread stop thr_primary") {
					t.Fatal("verified stop did not take precedence")
				}
			} else if f.read("calls") != "" {
				t.Fatal("paused loop queried provider")
			}
			if f.read("probes") != "" || strings.Contains(f.read("calls"), "thread tell") {
				t.Fatal("held loop prompted or probed")
			}
		})
	}
}
