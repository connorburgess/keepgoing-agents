package supervise

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func progressOptions(w *world, now time.Time, fingerprint string) Options {
	w.cfg.Loop.Limits.NoProgressTimeout = config.Duration{Duration: time.Hour}
	w.cfg.Loop.Progress.Repositories = []string{"/example/repo"}
	opts := w.opts(now)
	opts.Fingerprint = func([]string) (string, error) { return fingerprint, nil }
	return opts
}
func progressTick(t *testing.T, w *world, now time.Time, fp string) Result {
	t.Helper()
	res, err := Tick(context.Background(), w.cfg, progressOptions(w, now, fp))
	if err != nil {
		t.Fatal(err)
	}
	return res
}
func TestNoProgressAcrossTurnsRecoveryAndTargets(t *testing.T) {
	for _, kind := range []string{"idle", "recovery", "handoff"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, true, kind == "handoff")
			if kind == "recovery" {
				w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
			}
			if kind == "handoff" {
				w.primary(limited)
				w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
			}
			first := progressTick(t, w, t0, "same")
			if kind == "handoff" && first.Action != "handoff" {
				t.Fatalf("%+v", first)
			}
			progressTick(t, w, t0.Add(30*time.Minute), "same")
			probes := len(w.probes)
			opts := progressOptions(w, t0.Add(time.Hour), "same")
			opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
				t.Fatal("expired check queried provider")
				return nil, nil
			}
			res, err := Tick(context.Background(), w.cfg, opts)
			if err != nil || res.Action != "await_user" || !strings.Contains(res.Reason, "no Git progress") {
				t.Fatalf("%+v %v", res, err)
			}
			if len(w.probes) != probes {
				t.Fatal("probed after no-progress limit")
			}
			progressTick(t, w, t0.Add(2*time.Hour), "changed") // Existing hold wins even over new Git changes.
			if strings.Count(w.reportTypes(), "await_user") != 1 {
				t.Fatal("repeated report")
			}
			control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false })
			if res := progressTick(t, w, t0.Add(2*time.Hour), "same"); res.Action != "await_user" {
				t.Fatal("resume refunded time")
			}
			control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false })
			if kind == "recovery" {
				w.adapters["primary"].obs = decide.Observation{State: decide.Idle}
			}
			if res := progressTick(t, w, t0.Add(2*time.Hour), "changed"); res.Action == "await_user" {
				t.Fatalf("verified progress failed: %+v", res)
			}
		})
	}
}
func TestNoProgressEvidencePrecedesProviderAndSurvivesLostHistory(t *testing.T) {
	w := newWorld(t, true, false)
	opts := progressOptions(w, t0, "same")
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
		p, err := state.LoadProgress(w.dir)
		if err != nil || p == nil || !p.ChangedAt.Equal(t0) {
			t.Fatal("baseline not durable before provider work")
		}
		return nil, errors.New("provider unavailable")
	}
	if _, err := Tick(context.Background(), w.cfg, opts); err == nil {
		t.Fatal("expected provider failure")
	}
	// A failed adapter build is now a recorded check. Lose the diagnostic
	// history anyway: the allowance evidence must not depend on it.
	st, _ := state.Load(w.dir)
	if st.LastCheck == nil || st.LastCheck.Outcome != "failed" || st.FailedChecks != 1 {
		t.Fatalf("failed check not recorded: %+v", st.LastCheck)
	}
	if err := os.Remove(filepath.Join(w.dir, state.FileName)); err != nil {
		t.Fatal(err)
	}
	if got := progressTick(t, w, t0.Add(time.Hour), "same"); got.Action != "await_user" {
		t.Fatalf("failure refunded: %+v", got)
	}
}
func TestNoProgressUnavailableAndDryRun(t *testing.T) {
	for _, kind := range []string{"fingerprint", "corrupt", "write", "clock", "dry-run"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, true, false)
			opts := progressOptions(w, t0, "same")
			switch kind {
			case "fingerprint":
				opts.Fingerprint = func([]string) (string, error) { return "", errors.New("private raw error") }
			case "corrupt":
				os.WriteFile(filepath.Join(w.dir, state.ProgressName), []byte("private broken contents"), 0600)
			case "write":
				opts.Fingerprint = func([]string) (string, error) {
					if err := os.Mkdir(filepath.Join(w.dir, state.ProgressName), 0700); err != nil {
						t.Fatal(err)
					}
					return "same", nil
				}
			case "clock":
				progressTick(t, w, t0.Add(time.Minute), "same")
			case "dry-run":
				opts.DryRun = true
			}
			if kind != "dry-run" {
				opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
					t.Fatal("provider queried without evidence")
					return nil, nil
				}
			}
			res, err := Tick(context.Background(), w.cfg, opts)
			if kind == "dry-run" {
				if err != nil || res.Action != "wake" {
					t.Fatalf("%+v %v", res, err)
				}
				if p, e := state.LoadProgress(w.dir); e != nil || p != nil {
					t.Fatal("dry run wrote baseline")
				}
			} else if err == nil || strings.Contains(err.Error(), "private") && kind == "fingerprint" {
				t.Fatalf("%+v %v", res, err)
			}
		})
	}
}
func TestNoProgressControlsAndPolling(t *testing.T) {
	for _, hold := range []string{"stop", "paused", "disabled", "completed", "awaiting", "expired-dry-run", "expired"} {
		now := time.Now().UTC().Add(-2 * time.Hour)
		w := newWorld(t, true, false)
		progressTick(t, w, now, "same")
		control.Update(w.dir, func(f *control.File) {
			switch hold {
			case "stop":
				f.Stop = true
			case "paused":
				f.Paused = true
			case "disabled":
				f.Enabled = false
			case "completed":
				f.Completed = true
			case "awaiting":
				f.AwaitingUser = true
			}
		})
		opts := progressOptions(w, now.Add(time.Hour), "same")
		opts.DryRun = hold == "expired-dry-run"
		if hold == "stop" {
			w.primary(decide.Observation{State: decide.Active})
		}
		if hold != "expired" && hold != "expired-dry-run" {
			opts.Fingerprint = func([]string) (string, error) { t.Fatal("hold queried Git"); return "", nil }
		}
		factory := func(config.Agent, string, string) (adapter.Adapter, error) {
			t.Fatal("poll queried expired/held provider")
			return nil, nil
		}
		if PollWake(context.Background(), w.cfg, factory) {
			t.Fatal("poll requested early wake")
		}
		res, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		if hold == "stop" && res.Action != "stop" {
			t.Fatal("stop lost")
		}
		if hold == "expired-dry-run" {
			ctl, _ := control.Load(w.dir)
			if ctl.AwaitingUser || res.Action != "await_user" {
				t.Fatal("dry run changed hold or lost preview")
			}
		}
	}
}

func TestNoProgressPreventsSwitchBackUntilVerifiedChange(t *testing.T) {
	w, p, _ := switchBackWorld(t)
	progressTick(t, w, t0.Add(time.Hour), "same")
	w.probeOK["primary"] = true
	p.obs = decide.Observation{State: decide.Idle}
	probes := len(w.probes)
	if got := progressTick(t, w, t0.Add(2*time.Hour), "same"); got.Action != "await_user" || len(w.probes) != probes {
		t.Fatalf("expired switch-back: %+v", got)
	}
	control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false })
	if got := progressTick(t, w, t0.Add(2*time.Hour), "changed"); got.Action != "switch_back" || got.Target != "primary" {
		t.Fatalf("verified change: %+v", got)
	}
}
func TestNoProgressPollEvidenceGates(t *testing.T) {
	for _, kind := range []string{"fresh", "missing", "corrupt", "rollback", "changed-sources"} {
		w := newWorld(t, true, false)
		now := time.Now().UTC()
		progressOptions(w, now, "same")
		if kind != "missing" {
			at := now.Add(-time.Minute)
			if kind == "rollback" {
				at = now.Add(time.Hour)
			}
			if _, err := state.ObserveProgress(w.dir, nil, state.ProgressSources(w.cfg.Loop.Progress.Repositories), "same", at, false); err != nil {
				t.Fatal(err)
			}
		}
		if kind == "corrupt" {
			if err := os.WriteFile(filepath.Join(w.dir, state.ProgressName), []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if kind == "changed-sources" {
			w.cfg.Loop.Progress.Repositories = []string{"/example/another"}
		}
		made := 0
		factory := func(config.Agent, string, string) (adapter.Adapter, error) {
			made++
			return &wakeAdapter{fakeAdapter: fakeAdapter{obs: decide.Observation{State: decide.Idle}}, signal: true}, nil
		}
		before, _ := os.ReadFile(filepath.Join(w.dir, state.ProgressName))
		got := PollWake(context.Background(), w.cfg, factory)
		if got != (kind == "fresh") || (made > 0) != (kind == "fresh") {
			t.Fatalf("%s: wake=%v factory=%d", kind, got, made)
		}
		after, _ := os.ReadFile(filepath.Join(w.dir, state.ProgressName))
		if string(before) != string(after) {
			t.Fatal("poll changed progress accounting")
		}
	}
}

// The no-progress flag's window starts at ProgressChangedAt: the first
// fingerprint is a baseline, a credit for time in which nothing could make
// progress moves the timeout's LastProgressAt but not it, and a verified
// change sets it.
func TestProgressChangedAtIsTheUncreditedChangeTime(t *testing.T) {
	w := newWorld(t, true, false)
	progressTick(t, w, t0, "same")
	st, err := state.Load(w.dir)
	if err != nil || !st.ProgressChangedAt.Equal(t0) || !st.ProgressBaseline {
		t.Fatalf("first fingerprint: %+v %v", st, err)
	}
	// A credit never passes the latest observation, so observe once more.
	progressTick(t, w, t0.Add(25*time.Minute), "same")
	p, err := state.LoadProgress(w.dir)
	if err != nil || p == nil {
		t.Fatalf("%v", err)
	}
	if _, err := state.CreditProgress(w.dir, *p, 20*time.Minute, false); err != nil {
		t.Fatal(err)
	}
	progressTick(t, w, t0.Add(30*time.Minute), "same")
	st, _ = state.Load(w.dir)
	if !st.LastProgressAt.After(t0) || !st.ProgressChangedAt.Equal(t0) || !st.ProgressBaseline {
		t.Fatalf("after a credit: last progress %v, changed %v baseline %t", st.LastProgressAt, st.ProgressChangedAt, st.ProgressBaseline)
	}
	progressTick(t, w, t0.Add(40*time.Minute), "changed")
	st, _ = state.Load(w.dir)
	if !st.ProgressChangedAt.Equal(t0.Add(40*time.Minute)) || st.ProgressBaseline {
		t.Fatalf("after a change: %v baseline %t", st.ProgressChangedAt, st.ProgressBaseline)
	}
	progressTick(t, w, t0.Add(50*time.Minute), "changed")
	if st, _ = state.Load(w.dir); !st.ProgressChangedAt.Equal(t0.Add(40 * time.Minute)) {
		t.Fatalf("an unchanged check moved it: %v", st.ProgressChangedAt)
	}
	// A state from before the field, with a fingerprint already recorded:
	// the next check starts a baseline rather than claiming a change.
	st.ProgressChangedAt, st.ProgressBaseline = time.Time{}, false
	if err := state.Save(w.dir, st); err != nil {
		t.Fatal(err)
	}
	progressTick(t, w, t0.Add(55*time.Minute), "changed")
	if st, _ = state.Load(w.dir); !st.ProgressChangedAt.Equal(t0.Add(55*time.Minute)) || !st.ProgressBaseline {
		t.Fatalf("an older state: %v baseline %t", st.ProgressChangedAt, st.ProgressBaseline)
	}
}
