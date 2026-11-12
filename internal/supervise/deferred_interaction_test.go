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
	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

const (
	acpTurn    = "turn-example-1"
	acpSession = "session-example/opaque"
)

// acpHoldAdapter is a stand-in, not a runner: it reads the real saved ACP hold
// records and otherwise reports idle, so any tick that fails to hold would send.
// Reporting idle after adoption is supervisor mechanics for these fixtures, not
// a statement about how a future ACP runner reconciles a cancelled turn.
type acpHoldAdapter struct {
	dir      runner.Dir
	extra    *decide.Observation
	adoptErr *error
	sent     *int
	stopped  *int
}

func (a acpHoldAdapter) Name() string { return "fake-acp" }
func (a acpHoldAdapter) Observe(context.Context) (decide.Observation, error) {
	obs := *a.extra
	h, ok, err := a.dir.AntigravityACPHold()
	if err != nil {
		return decide.Observation{}, errors.New("saved interaction evidence unavailable")
	}
	obs.DeferredInteraction = ok && h.Deferred()
	return obs, nil
}
func (a acpHoldAdapter) Send(context.Context, string, adapter.Mode) error { *a.sent++; return nil }
func (a acpHoldAdapter) Stop(context.Context) error                       { *a.stopped++; return nil }
func (a acpHoldAdapter) AdoptInteraction(context.Context) error {
	if *a.adoptErr != nil {
		return *a.adoptErr
	}
	return a.dir.AdoptAntigravityACPHold(acpTurn)
}

type acpHoldWorld struct {
	*world
	target   string
	extra    decide.Observation
	adoptErr error
	sent     int
	stopped  int
}

func newACPHoldWorld(t *testing.T) *acpHoldWorld {
	t.Helper()
	w := &acpHoldWorld{world: newWorld(t, true, false), extra: decide.Observation{State: decide.Idle}}
	w.target = filepath.Join(w.dir, "targets", "primary")
	return w
}

// tick reconstructs the adapter every time, as a restarted process would.
func (w *acpHoldWorld) tick(t *testing.T, at time.Time, dry bool) (Result, error) {
	t.Helper()
	opts := w.opts(at)
	opts.DryRun = dry
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
		return acpHoldAdapter{dir: runner.Dir(w.target), extra: &w.extra, adoptErr: &w.adoptErr, sent: &w.sent, stopped: &w.stopped}, nil
	}
	return Tick(context.Background(), w.cfg, opts)
}

func (w *acpHoldWorld) checkpoint(t *testing.T, c runner.AntigravityACPCheckpoint) {
	t.Helper()
	c.SessionID = acpSession
	if err := runner.Dir(w.target).AntigravityACPHoldCheckpoint(acpTurn)(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func (w *acpHoldWorld) permissionCancelled(t *testing.T) {
	t.Helper()
	w.checkpoint(t, runner.AntigravityACPCheckpoint{BeforePrompt: true})
	w.checkpoint(t, runner.AntigravityACPCheckpoint{NeedsUser: true})
}

func (w *acpHoldWorld) held(t *testing.T) control.File {
	t.Helper()
	ctl, err := control.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	return ctl
}

func (w *acpHoldWorld) adopted(t *testing.T) bool {
	t.Helper()
	h, ok, err := runner.Dir(w.target).AntigravityACPHold()
	if err != nil || !ok {
		t.Fatalf("record ok=%t err=%v", ok, err)
	}
	return h.Adopted
}

func (w *acpHoldWorld) resume(t *testing.T) {
	t.Helper()
	if _, err := control.Update(w.dir, func(f *control.File) { f.Paused, f.Stop, f.AwaitingUser = false, false, false }); err != nil {
		t.Fatal(err)
	}
}

func TestDeferredInteractionSavesHoldAcrossRestartUntilExplicitResume(t *testing.T) {
	w := newACPHoldWorld(t)
	w.permissionCancelled(t)

	// A dry run changes nothing and acknowledges nothing.
	res, err := w.tick(t, t0, true)
	if err != nil || res.Action != "await_user" || w.held(t).AwaitingUser || w.adopted(t) || len(w.reports) != 0 || w.sent != 0 {
		t.Fatalf("dry run: %+v %v", res, err)
	}

	// After observation the hold is saved before the progress fingerprint. (A
	// configured no-progress timeout fingerprints before observation instead.)
	w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
	opts := w.opts(t0.Add(time.Minute))
	opts.Fingerprint = func([]string) (string, error) { t.Fatal("fingerprint must not block the hold"); return "", nil }
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
		return acpHoldAdapter{dir: runner.Dir(w.target), extra: &w.extra, adoptErr: &w.adoptErr, sent: &w.sent, stopped: &w.stopped}, nil
	}
	res, err = Tick(context.Background(), w.cfg, opts)
	if err != nil || res.Action != "await_user" || res.Reason != decide.DeferredInteractionReason {
		t.Fatalf("%+v %v", res, err)
	}
	w.cfg.Loop.Progress.Repositories = nil
	ctl := w.held(t)
	if !ctl.AwaitingUser || ctl.Note != decide.DeferredInteractionReason || !ctl.Enabled || !w.adopted(t) || w.sent != 0 {
		t.Fatalf("control %+v adopted=%t sent=%d", ctl, w.adopted(t), w.sent)
	}
	if len(w.reports) != 1 || w.reports[0].Type != "await_user" || w.reports[0].Data["deferredInteraction"] != true || w.reports[0].Data["target"] != "primary" || len(w.reports[0].Data) != 2 {
		t.Fatalf("reports %+v", w.reports)
	}
	if text := w.reports[0].Text; strings.Contains(text, acpSession) || strings.Contains(text, acpTurn) {
		t.Fatalf("report carries private identity: %s", text)
	}

	// Automatic restarts never observe, send or report again.
	for i := 1; i <= 3; i++ {
		res, err := w.tick(t, t0.Add(time.Duration(i)*time.Hour), false)
		if err != nil || res.Action != "none" || res.Reason != "awaiting user" || res.State != "not observed" {
			t.Fatalf("restart %d: %+v %v", i, res, err)
		}
	}
	if w.sent != 0 || len(w.reports) != 1 {
		t.Fatalf("held loop acted: sent=%d reports=%s", w.sent, w.reportTypes())
	}

	// Only an explicit resume releases it, and the adopted evidence is not raised again.
	w.resume(t)
	res, err = w.tick(t, t0.Add(5*time.Hour), false)
	if err != nil || res.Action != "wake" || w.sent != 1 || w.held(t).AwaitingUser {
		t.Fatalf("after explicit resume: %+v %v sent=%d", res, err, w.sent)
	}
}

func TestDeferredInteractionFailedAdoptionHoldsAgainRatherThanLosingEvidence(t *testing.T) {
	w := newACPHoldWorld(t)
	w.permissionCancelled(t)
	// A failed adoption call stands in for a supervisor that died after saving
	// the hold; no process is killed here.
	w.adoptErr = errors.New("fictional adoption failure")
	if res, err := w.tick(t, t0, false); err == nil || res.Action != "await_user" {
		t.Fatalf("%+v %v", res, err)
	}
	if !w.held(t).AwaitingUser || w.adopted(t) || w.sent != 0 {
		t.Fatal("hold must be saved before adoption is attempted")
	}
	w.adoptErr = nil
	if res, err := w.tick(t, t0.Add(time.Hour), false); err != nil || res.Reason != "awaiting user" || w.adopted(t) {
		t.Fatalf("restart: %+v %v", res, err)
	}
	// The supervisor cannot know the first hold was seen, so one more explicit
	// resume is required. It never becomes a send.
	w.resume(t)
	if res, err := w.tick(t, t0.Add(2*time.Hour), false); err != nil || res.Action != "await_user" || !w.held(t).AwaitingUser || !w.adopted(t) || w.sent != 0 {
		t.Fatalf("unadopted evidence after resume: %+v %v", res, err)
	}
	w.resume(t)
	if res, err := w.tick(t, t0.Add(3*time.Hour), false); err != nil || res.Action != "wake" || w.sent != 1 {
		t.Fatalf("second resume: %+v %v", res, err)
	}
}

func TestDeferredInteractionYieldsToStopPauseAndOtherHolds(t *testing.T) {
	t.Run("stop_then_pause", func(t *testing.T) {
		w := newACPHoldWorld(t)
		w.permissionCancelled(t)
		w.extra.State = decide.Active
		if _, err := control.Update(w.dir, func(f *control.File) { f.Stop, f.Paused = true, true }); err != nil {
			t.Fatal(err)
		}
		res, err := w.tick(t, t0, false)
		if err != nil || res.Action != "stop" || w.stopped != 1 || w.held(t).AwaitingUser || w.adopted(t) || w.sent != 0 {
			t.Fatalf("stop: %+v %v", res, err)
		}
		if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = false }); err != nil {
			t.Fatal(err)
		}
		if res, err := w.tick(t, t0.Add(time.Hour), false); err != nil || res.Reason != "paused" || w.held(t).AwaitingUser || w.adopted(t) {
			t.Fatalf("pause: %+v %v", res, err)
		}
		// Evidence outlives both controls and still holds once they are lifted.
		w.resume(t)
		if res, err := w.tick(t, t0.Add(2*time.Hour), false); err != nil || res.Action != "await_user" || !w.held(t).AwaitingUser || !w.adopted(t) || w.sent != 0 || w.stopped != 1 {
			t.Fatalf("after controls: %+v %v", res, err)
		}
	})

	t.Run("child_input_and_gone_keep_their_own_hold", func(t *testing.T) {
		for name, extra := range map[string]decide.Observation{
			"child": {State: decide.Idle, AwaitingChildren: []string{"thr_child"}},
			"gone":  {State: decide.Idle, Gone: true},
		} {
			t.Run(name, func(t *testing.T) {
				w := newACPHoldWorld(t)
				w.permissionCancelled(t)
				w.extra = extra
				res, err := w.tick(t, t0, false)
				if err != nil || res.Action != "await_user" || res.Reason == decide.DeferredInteractionReason || !w.held(t).AwaitingUser {
					t.Fatalf("%+v %v", res, err)
				}
				if w.adopted(t) || w.reports[0].Data["deferredInteraction"] != nil {
					t.Fatal("another rule's hold acknowledged the permission evidence")
				}
				w.extra = decide.Observation{State: decide.Idle}
				w.resume(t)
				if res, err := w.tick(t, t0.Add(time.Hour), false); err != nil || res.Reason != decide.DeferredInteractionReason || !w.adopted(t) || w.sent != 0 {
					t.Fatalf("after resume: %+v %v", res, err)
				}
			})
		}
	})

	t.Run("unreadable_evidence_takes_no_action", func(t *testing.T) {
		w := newACPHoldWorld(t)
		w.permissionCancelled(t)
		if err := os.WriteFile(filepath.Join(w.target, "acp-needs-user.json"), []byte(`{"version":1`), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			res, err := w.tick(t, t0.Add(time.Duration(i)*time.Hour), false)
			if err == nil || res.Action != "none" || w.held(t).AwaitingUser || w.sent != 0 {
				t.Fatalf("%+v %v sent=%d", res, err, w.sent)
			}
		}
	})

	t.Run("unsaved_hold_is_not_adopted", func(t *testing.T) {
		w := newACPHoldWorld(t)
		w.permissionCancelled(t)
		// Control updates refuse a symlinked control file, so the hold cannot be saved.
		real := filepath.Join(t.TempDir(), "control.json")
		if err := os.Rename(filepath.Join(w.dir, control.FileName), real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(w.dir, control.FileName)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		res, err := w.tick(t, t0, false)
		if err == nil || res.Action != "await_user" || w.held(t).AwaitingUser {
			t.Fatalf("%+v %v", res, err)
		}
		if w.adopted(t) || w.sent != 0 {
			t.Fatal("evidence acknowledged without a saved hold")
		}
	})

	t.Run("reservation_alone_is_not_a_hold", func(t *testing.T) {
		w := newACPHoldWorld(t)
		w.checkpoint(t, runner.AntigravityACPCheckpoint{BeforePrompt: true})
		w.extra.State = decide.Unknown
		if res, err := w.tick(t, t0, false); err != nil || res.Action != "none" || w.held(t).AwaitingUser || w.sent != 0 {
			t.Fatalf("%+v %v", res, err)
		}
	})
}
