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

func sendsUsed(t *testing.T, w *world, want int64) {
	t.Helper()
	used, err := state.LoadSends(w.dir)
	if err != nil || used != want {
		t.Fatalf("used=%d want=%d err=%v", used, want, err)
	}
}

func TestSendLimitAcrossTurnsAndResume(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Limits.MaxSends = 2
	for i := 0; i < 2; i++ { // Repeated idle observations, no in-memory counter survives Tick.
		if got := runTick(t, w, t0); got.Action != "wake" {
			t.Fatalf("%+v", got)
		}
	}
	sendsUsed(t, w, 2)
	for i := 0; i < 3; i++ {
		runTick(t, w, t0.Add(time.Hour))
	}
	if len(w.adapters["primary"].sent) != 2 || strings.Count(w.reportTypes(), "await_user") != 1 {
		t.Fatal("cap or report dedup failed")
	}
	control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false })
	if got := runTick(t, w, t0); got.Action != "await_user" {
		t.Fatalf("resume refunded: %+v", got)
	}
	w.cfg.Loop.Limits.MaxSends = 3
	if got := runTick(t, w, t0); got.Reason != "awaiting user" {
		t.Fatalf("edit reopened: %+v", got)
	}
	control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false })
	if got := runTick(t, w, t0); got.Action != "wake" {
		t.Fatalf("increase+resume: %+v", got)
	}
	sendsUsed(t, w, 3)
}

func TestSendAttemptKindsAndFailures(t *testing.T) {
	for _, kind := range []string{"wake", "nudge", "reprompt", "failed", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.Limits.MaxSends = 1
			p := w.primary(decide.Observation{State: decide.Idle})
			opts := w.opts(t0.Add(time.Hour))
			switch kind {
			case "nudge":
				p.obs.State = decide.Active
				w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
				opts.Fingerprint = func([]string) (string, error) { return "same", nil }
				if err := state.Save(w.dir, state.File{Fingerprint: "same", LastProgressAt: t0}); err != nil {
					t.Fatal(err)
				}
			case "reprompt":
				p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
			case "failed":
				p.sendErr = errors.New("uncertain delivery")
			case "unsupported":
				p.sendErr = adapter.ErrUnsupported{Op: "send"}
			}
			got, err := Tick(context.Background(), w.cfg, opts)
			if kind == "failed" && err == nil {
				t.Fatal("expected send failure")
			}
			if kind != "failed" && err != nil {
				t.Fatal(err)
			}
			if kind != "failed" && kind != "unsupported" && got.Action != kind {
				t.Fatalf("%+v", got)
			}
			sendsUsed(t, w, 1)
			if got := runTick(t, w, t0); got.Action != "await_user" || len(p.sent) != 1 {
				t.Fatalf("attempt refunded: %+v", got)
			}
		})
	}
}

func TestHandoffSendLimitWithinOneTick(t *testing.T) {
	for _, max := range []int64{1, 2, 3} {
		w := newWorld(t, true, true)
		w.cfg.Loop.Limits.MaxSends = max
		p := w.primary(limited)
		w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
		for _, id := range []string{"claude-other", "codex-main"} {
			w.adapters[id] = &fakeAdapter{sendErr: errors.New("uncertain")}
		}
		got := runTick(t, w, t0)
		if max < 3 && got.Action != "await_user" {
			t.Fatalf("max=%d %+v", max, got)
		}
		sendsUsed(t, w, max)
		total := len(p.sent) + len(w.adapters["claude-other"].sent) + len(w.adapters["codex-main"].sent)
		if int64(total) != max {
			t.Fatalf("sent=%d max=%d", total, max)
		}
		if max == 1 && (w.adapters["codex-main"].activated != 0 || strings.Contains(strings.Join(w.probes, ","), "codex-main:exact")) {
			t.Fatal("next candidate touched after exhaustion")
		}
	}
}

func TestSwitchBackSendLimit(t *testing.T) {
	for _, failed := range []bool{false, true} {
		w, p, fb := switchBackWorld(t) // Setup handoff happened before accounting was enabled.
		prior := len(fb.sent)
		w.cfg.Loop.Limits.MaxSends = 1
		w.probeOK["primary"] = true
		if failed {
			p.sendErr = errors.New("uncertain")
		}
		got := runTick(t, w, t0.Add(3*time.Hour))
		want := "switch_back"
		if failed {
			want = "await_user"
		}
		if got.Action != want || len(p.sent) != 1 || len(fb.sent) != prior {
			t.Fatalf("%+v", got)
		}
		sendsUsed(t, w, 1)
	}
}

func TestSendAllowancePreflightAndStopPrecedence(t *testing.T) {
	for _, bad := range []bool{false, true} {
		for _, hold := range []string{"none", "paused", "disabled", "completed", "awaiting", "stop", "dry-run"} {
			w := newWorld(t, true, true)
			w.cfg.Loop.Limits.MaxSends = 1
			if err := state.ReserveSend(w.dir, 1); err != nil {
				t.Fatal(err)
			}
			if bad {
				if err := os.WriteFile(filepath.Join(w.dir, state.SendsName), []byte("private invalid contents"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			control.Update(w.dir, func(f *control.File) {
				switch hold {
				case "paused":
					f.Paused = true
				case "disabled":
					f.Enabled = false
				case "completed":
					f.Completed = true
				case "awaiting":
					f.AwaitingUser = true
				case "stop":
					f.Stop = true
				}
			})
			opts := w.opts(t0)
			opts.DryRun = hold == "dry-run"
			if hold == "stop" {
				w.primary(decide.Observation{State: decide.Active})
			} else {
				opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
					t.Fatal("provider constructed")
					return nil, nil
				}
			}
			if PollWake(context.Background(), w.cfg, opts.NewAdapter) {
				t.Fatal("exhausted/held poll requested wake")
			}
			got, err := Tick(context.Background(), w.cfg, opts)
			wantErr := bad && (hold == "none" || hold == "dry-run")
			if (err != nil) != wantErr {
				t.Fatalf("bad=%t hold=%s result=%+v err=%v", bad, hold, got, err)
			}
			if len(w.probes) > 0 {
				t.Fatal("probes despite budget gate")
			}
			if hold == "stop" && got.Action != "stop" {
				t.Fatal("stop lost")
			}
			if hold == "dry-run" {
				ctl, _ := control.Load(w.dir)
				if ctl.AwaitingUser {
					t.Fatal("dry run persisted hold")
				}
			}
		}
	}
}

// A crash after delivery but before diagnostic state is recorded must not refund
// the attempt. Panic models that boundary without starting any real provider.
type crashSend struct {
	fakeAdapter
	t   *testing.T
	dir string
}

func (a *crashSend) Send(context.Context, string, adapter.Mode) error {
	used, err := state.LoadSends(a.dir)
	if err != nil || used != 1 {
		a.t.Fatalf("reservation not visible before send: %d %v", used, err)
	}
	panic("simulated process exit")
}
func TestSendReservationSurvivesMissingCheckRecord(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Limits.MaxSends = 1
	opts := w.opts(t0)
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
		return &crashSend{fakeAdapter: fakeAdapter{obs: decide.Observation{State: decide.Idle}}, t: t, dir: w.dir}, nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("did not reach send")
			}
		}()
		Tick(context.Background(), w.cfg, opts)
	}()
	st, err := state.Load(w.dir)
	if err != nil || st.LastCheck != nil {
		t.Fatal("unexpected completed check")
	}
	if got := runTick(t, w, t0); got.Action != "await_user" {
		t.Fatalf("restart refunded: %+v", got)
	}
}

func TestSendLimitDryRunDoesNotReserve(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Limits.MaxSends = 1
	opts := w.opts(t0)
	opts.DryRun = true
	for i := 0; i < 3; i++ {
		got, err := Tick(context.Background(), w.cfg, opts)
		if err != nil || got.Action != "wake" {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(w.dir, state.SendsName)); !os.IsNotExist(err) {
		t.Fatal("dry run wrote ledger")
	}
}

func TestSendReservationFailureCannotDeliverOrTryAnotherTarget(t *testing.T) {
	for _, handoff := range []bool{false, true} {
		w := newWorld(t, true, handoff)
		w.cfg.Loop.Limits.MaxSends = 2
		if handoff {
			w.primary(limited)
			w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
		}
		opts := w.opts(t0)
		factory := opts.NewAdapter
		opts.NewAdapter = func(a config.Agent, dir, target string) (adapter.Adapter, error) {
			made, err := factory(a, dir, target)
			// Preflight succeeded, but the reservation destination is no longer writable.
			if !handoff || target == "claude-other" {
				if err := os.Mkdir(filepath.Join(w.dir, state.SendsName), 0700); err != nil {
					t.Fatal(err)
				}
			}
			return made, err
		}
		got, err := Tick(context.Background(), w.cfg, opts)
		if !errors.Is(err, state.ErrSendLedger) || got.Action != "none" {
			t.Fatalf("%+v %v", got, err)
		}
		for _, a := range w.adapters {
			if len(a.sent) != 0 {
				t.Fatal("sent without reservation")
			}
		}
		if handoff && strings.Contains(strings.Join(w.probes, ","), "codex-main:exact") {
			t.Fatal("continued recovery after accounting failure")
		}
	}
}
