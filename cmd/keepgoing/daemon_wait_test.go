package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

// Coarse or preserved filesystem timestamps can hide a same-size control edit.
// Freeze the metadata explicitly rather than depending on the host's clock.
func TestDaemonWaitDetectsControlContentWithUnchangedMetadata(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		for _, flag := range []string{"paused", "stop"} {
			for _, timing := range []string{"before wait", "during poll"} {
				name := flag + "/" + timing
				if replacement {
					name += "/atomic replacement"
				}
				t.Run(name, func(t *testing.T) {
					cfg := &config.Config{Loop: config.Loop{StateDir: t.TempDir()}}
					cfg.Loop.Daemon.Interval.Duration = time.Hour
					cfg.Loop.Daemon.ControlPoll.Duration = time.Millisecond
					path := filepath.Join(cfg.Loop.StateDir, control.FileName)
					// Put the changed gate after more than a copy buffer of note data.
					before := `{"note":"` + strings.Repeat("fixture", 20000) + `","enabled":true,"paused":false,"stop":false}`
					after := strings.Replace(before, `"`+flag+`":false`, `"`+flag+`":true `, 1)
					stamp := time.Unix(1700000000, 0)
					write := func(contents string) os.FileInfo {
						t.Helper()
						destination := path
						if replacement {
							destination += ".new"
						}
						if err := os.WriteFile(destination, []byte(contents), 0o600); err != nil {
							t.Fatal(err)
						}
						if replacement {
							if err := os.Rename(destination, path); err != nil {
								t.Fatal(err)
							}
						}
						if err := os.Chtimes(path, stamp, stamp); err != nil {
							t.Fatal(err)
						}
						info, err := os.Stat(path)
						if err != nil {
							t.Fatal(err)
						}
						return info
					}
					old := write(before)
					sig := controlSignature(cfg.Loop.StateDir)
					change := func() {
						next := write(after)
						if next.Size() != old.Size() || !next.ModTime().Equal(old.ModTime()) {
							t.Fatal("fixture did not preserve metadata")
						}
						ctl, err := control.Load(cfg.Loop.StateDir)
						if err != nil || (flag == "paused" && !ctl.Paused) || (flag == "stop" && !ctl.Stop) {
							t.Fatalf("fixture hold mismatch: paused=%t stop=%t error=%v", ctl.Paused, ctl.Stop, err)
						}
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					if timing == "before wait" {
						change()
					}
					calls := 0
					poll := func(context.Context) bool {
						calls++
						if timing == "during poll" && calls == 1 {
							change()
						} else {
							cancel()
						}
						return false
					}
					if !waitForNextWithPoll(ctx, cfg, sig, nil, poll) || ctx.Err() != nil {
						t.Fatalf("saved %s was not detected before another poll (calls=%d)", flag, calls)
					}
					want := 0
					if timing == "during poll" {
						want = 1
					}
					if calls != want {
						t.Fatalf("polls=%d; want %d", calls, want)
					}
				})
			}
		}
	}
}

func TestDaemonWaitDoesNotWakeForUnchangedControl(t *testing.T) {
	for _, present := range []bool{false, true} {
		cfg := &config.Config{Loop: config.Loop{StateDir: t.TempDir()}}
		cfg.Loop.Daemon.Interval.Duration = time.Hour
		cfg.Loop.Daemon.ControlPoll.Duration = time.Millisecond
		if present {
			if err := control.Save(cfg.Loop.StateDir, control.File{Paused: true}); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		calls := 0
		poll := func(context.Context) bool {
			calls++
			if calls == 2 {
				cancel()
			}
			return false
		}
		next := waitForNextWithPoll(ctx, cfg, controlSignature(cfg.Loop.StateDir), nil, poll)
		cancel()
		if next || calls != 2 {
			t.Fatalf("unchanged control (present=%t) triggered early check: next=%t calls=%d", present, next, calls)
		}
	}
}

// A poll can consume a one-shot wake notice, so none may run once the daemon
// has been told to exit. Hold each poll past several control ticks, so that on
// return both the cancelled context and a pending tick are ready and the wait
// must choose correctly. Left to select's random choice, one round in two
// polled again; fifty rounds make a chance pass negligible.
func TestDaemonWaitNeverPollsAfterCancellation(t *testing.T) {
	cfg := &config.Config{Loop: config.Loop{StateDir: t.TempDir()}}
	cfg.Loop.Daemon.Interval.Duration = time.Hour
	cfg.Loop.Daemon.ControlPoll.Duration = time.Millisecond
	sig := controlSignature(cfg.Loop.StateDir)
	for round := 0; round < 50; round++ {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		poll := func(context.Context) bool {
			calls++
			cancel()
			time.Sleep(10 * cfg.Loop.Daemon.ControlPoll.Duration)
			return false
		}
		if waitForNextWithPoll(ctx, cfg, sig, nil, poll) || calls != 1 {
			t.Fatalf("round %d: next after cancellation or extra poll (polls=%d)", round, calls)
		}
	}
}

func TestDaemonWaitHandlesPollResultsBeforeOrdinaryInterval(t *testing.T) {
	for _, mode := range []string{"notification", "later notification", "control change", "cancelled notification"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{Loop: config.Loop{StateDir: t.TempDir()}}
			cfg.Loop.Daemon.Interval.Duration = time.Hour
			cfg.Loop.Daemon.ControlPoll.Duration = time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			calls := 0
			poll := func(context.Context) bool {
				calls++
				switch mode {
				case "later notification":
					return calls == 2
				case "control change":
					if _, err := control.Update(cfg.Loop.StateDir, func(f *control.File) { f.Stop = true }); err != nil {
						t.Fatal(err)
					}
					return false
				case "cancelled notification":
					cancel()
				}
				return true
			}
			got := waitForNextWithPoll(ctx, cfg, controlSignature(cfg.Loop.StateDir), nil, poll)
			want, wantCalls := mode != "cancelled notification", 1
			if mode == "later notification" {
				wantCalls = 2
			}
			if got != want || calls != wantCalls {
				t.Fatalf("next=%t polls=%d; want next=%t polls=%d", got, calls, want, wantCalls)
			}
			if mode != "cancelled notification" && ctx.Err() != nil {
				t.Fatalf("wait reached test deadline: %v", ctx.Err())
			}
		})
	}
}
