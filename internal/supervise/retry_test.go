package supervise

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestActiveRetryPreservesNudgesAndRecoveryGrace(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.MaxNudges = 3
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	p := w.primary(decide.Observation{State: decide.Active, Retrying: true})
	if err := state.Save(w.dir, state.File{Fingerprint: "same", LastProgressAt: t0, LastState: "active", Nudges: 2}); err != nil {
		t.Fatal(err)
	}
	run := func(at time.Time, dry bool) Result {
		opts := w.opts(at)
		opts.DryRun = dry
		opts.Fingerprint = func([]string) (string, error) { return "same", nil }
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for i := 1; i <= 6; i++ {
		r := run(t0.Add(time.Duration(i)*time.Hour), false)
		st, _ := state.Load(w.dir)
		ctl, _ := control.Load(w.dir)
		if r.Action != "none" || r.Reason != "provider is retrying the turn" || st.Nudges != 2 || ctl.AwaitingUser || len(p.sent) != 0 || !st.LastProgressAt.Equal(t0) {
			t.Fatalf("%+v %+v", r, st)
		}
	}
	// Failed refresh does not announce recovery or clear the pending episode.
	p.obs = decide.Observation{State: decide.Active, RetryUnverified: true}
	if r := run(t0.Add(7*time.Hour), false); r.Reason != "provider retry state unverified" {
		t.Fatal(r)
	}
	p.obs = decide.Observation{State: decide.Active}
	if r := run(t0.Add(8*time.Hour), true); r.Action != "none" {
		t.Fatal(r)
	}
	st, _ := state.Load(w.dir)
	if !st.StallRetry.Waiting {
		t.Fatal("dry run consumed recovery")
	}
	if r := run(t0.Add(9*time.Hour), false); r.Action != "none" {
		t.Fatal("no recovery grace", r)
	}
	if r := run(t0.Add(9*time.Hour+29*time.Minute), false); r.Action != "none" {
		t.Fatal("grace ended early", r)
	}
	if r := run(t0.Add(9*time.Hour+30*time.Minute), false); r.Action != "nudge" {
		t.Fatal("later stall was hidden", r)
	}
	st, _ = state.Load(w.dir)
	if st.Nudges != 3 || !st.LastProgressAt.Equal(t0) {
		t.Fatal("recovery forged Git progress or refunded nudges")
	}
	if r := run(t0.Add(11*time.Hour), false); r.Action != "await_user" {
		t.Fatal("real stall no longer escalates", r)
	}
	// Recovery must not silently clear the explicit hold.
	p.obs = decide.Observation{State: decide.Idle}
	if r := run(t0.Add(12*time.Hour), false); r.Reason != "awaiting user" {
		t.Fatal(r)
	}
}

func TestRetryHardBoundsAndHoldsRemainAuthoritative(t *testing.T) {
	for _, kind := range []string{"deadline", "sends", "progress", "stop", "pause", "completed"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(decide.Observation{State: decide.Active, Retrying: true})
			opts := w.opts(t0.Add(2 * time.Hour))
			opts.Fingerprint = func([]string) (string, error) { return "same", nil }
			want := "await_user"
			switch kind {
			case "deadline":
				w.cfg.Loop.Limits.Deadline = t0.Format(time.RFC3339)
			case "sends":
				w.cfg.Loop.Limits.MaxSends = 1
				if err := state.ReserveSend(w.dir, 1); err != nil {
					t.Fatal(err)
				}
			case "progress":
				w.cfg.Loop.Limits.NoProgressTimeout = config.Duration{Duration: time.Hour}
				w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
				_, err := state.ObserveProgress(w.dir, nil, state.ProgressSources(w.cfg.Loop.Progress.Repositories), "same", t0, false)
				if err != nil {
					t.Fatal(err)
				}
			case "stop":
				control.Update(w.dir, func(c *control.File) { c.Stop = true })
				want = "stop"
			case "pause":
				control.Update(w.dir, func(c *control.File) { c.Paused = true })
				want = "none"
			case "completed":
				control.Update(w.dir, func(c *control.File) { c.Completed = true })
				want = "none"
			}
			r, err := Tick(context.Background(), w.cfg, opts)
			if err != nil || r.Action != want || len(p.sent) != 0 {
				t.Fatalf("%+v %v", r, err)
			}
			if kind == "stop" && p.stopped != 1 {
				t.Fatal("retry suppressed stop")
			}
		})
	}
}

func TestRetryGraceTargetIsolationAndRecord(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	w.primary(decide.Observation{State: decide.Active})
	st := state.File{Fingerprint: "same", LastProgressAt: t0, StallRetry: &state.StallRetry{Target: "old-target", Waiting: true}}
	if err := state.Save(w.dir, st); err != nil {
		t.Fatal(err)
	}
	opts := w.opts(t0.Add(time.Hour))
	opts.Fingerprint = func([]string) (string, error) { return "same", nil }
	r, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || r.Action != "nudge" {
		t.Fatal("another target granted grace", r, err)
	}
	w.adapters[state.Primary].obs.Retrying = true
	opts.Now = t0.Add(2 * time.Hour)
	r, err = Tick(context.Background(), w.cfg, opts)
	if err != nil || r.Action != "none" {
		t.Fatal(r, err)
	}
	b, err := os.ReadFile(filepath.Join(w.dir, state.LogName))
	if err != nil || !strings.Contains(string(b), "provider is retrying the turn") {
		t.Fatal("reason missing from own history")
	}
}
