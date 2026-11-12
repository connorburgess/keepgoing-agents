package supervise

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Before attempts were counted, each of these sessions was sent to on every
// check (20 of 20 re-prompts with max_reprompts 2) and never reached a hold.
func TestFailedAndUnsupportedSendsSpendCapsAndSpacing(t *testing.T) {
	sendErrs := map[string]error{
		"failed":      errors.New("uncertain delivery"),
		"unsupported": adapter.ErrUnsupported{Op: "send"},
	}
	cases := []struct {
		name      string
		obs       decide.Observation
		every     time.Duration
		checks    int
		wantSends []time.Duration
		wantHold  time.Duration
		wantNote  string
	}{
		{"reprompt", decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, time.Minute, 20,
			[]time.Duration{0, 10 * time.Minute}, 11 * time.Minute, "error persisted after re-prompts"},
		// The first check only records the Git baseline; the turn stalls after 30m.
		{"nudge", decide.Observation{State: decide.Active}, 10 * time.Minute, 20,
			[]time.Duration{30 * time.Minute, 90 * time.Minute}, 150 * time.Minute, "no progress after 2 nudges"},
	}
	for kind, sendErr := range sendErrs {
		for _, tc := range cases {
			t.Run(tc.name+" "+kind, func(t *testing.T) {
				w := newWorld(t, true, false)
				w.cfg.Loop.Progress.Repositories = []string{"/example/repo"}
				w.cfg.Loop.Progress.MaxNudges = 2 // newWorld sets max_reprompts 2, backoff 10m, min_wake_interval 1h
				p := w.primary(tc.obs)
				p.sendErr = sendErr
				check := func(at time.Duration) Result {
					opts := w.opts(t0.Add(at))
					opts.Fingerprint = func([]string) (string, error) { return "unchanged", nil }
					res, err := Tick(context.Background(), w.cfg, opts)
					if kind == "unsupported" && err != nil {
						t.Fatalf("at %v: %v", at, err)
					}
					return res
				}
				var sent []time.Duration
				hold := time.Duration(-1)
				for i := 0; i < tc.checks; i++ {
					at := time.Duration(i) * tc.every
					before := len(p.sent)
					res := check(at)
					if len(p.sent) > before {
						sent = append(sent, at)
					}
					if res.Action == "await_user" && hold < 0 {
						hold = at
					}
				}
				if len(sent) != len(tc.wantSends) || hold != tc.wantHold {
					t.Fatalf("sends at %v hold at %v, want %v and %v", sent, hold, tc.wantSends, tc.wantHold)
				}
				for i := range sent {
					if sent[i] != tc.wantSends[i] {
						t.Fatalf("sends at %v, want %v", sent, tc.wantSends)
					}
				}
				ctl, err := control.Load(w.dir)
				if err != nil || !ctl.AwaitingUser || !strings.Contains(ctl.Note, tc.wantNote) {
					t.Fatalf("hold=%+v err=%v", ctl, err)
				}
				if n := strings.Count(w.reportTypes(), "await_user"); n != 1 {
					t.Fatalf("await_user reports=%d (%s)", n, w.reportTypes())
				}
				// Stop still reaches the session after the hold.
				if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
					t.Fatal(err)
				}
				p.obs = decide.Observation{State: decide.Active}
				if res := check(24 * time.Hour); res.Action != "stop" || p.stopped != 1 || len(p.sent) != len(tc.wantSends) {
					t.Fatalf("stop: %+v stopped=%d sends=%d", res, p.stopped, len(p.sent))
				}
			})
		}
	}
}

// A failed attempt is persisted as spent. Pause and stop then win before any cap
// is reached and leave the count alone, and a session whose turn then ends
// without an error starts a fresh count. The pause and stop outcomes themselves are unchanged
// behavior; the persisted count is what the earlier code did not record.
func TestHoldsAndRecoveryBetweenFailedAttempts(t *testing.T) {
	for _, hold := range []string{"pause", "stop", "recovers"} {
		t.Run(hold, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
			p.sendErr = errors.New("uncertain delivery")
			tick := func(at time.Duration) Result {
				res, _ := Tick(context.Background(), w.cfg, w.opts(t0.Add(at)))
				return res
			}
			if res := tick(0); res.Action != "reprompt" || len(p.sent) != 1 {
				t.Fatalf("%+v", res)
			}
			spent := func(want int, wantAt time.Time) {
				t.Helper()
				st, err := state.Load(w.dir)
				if err != nil || st.Reprompts != want || !st.LastRepromptAt.Equal(wantAt) {
					t.Fatalf("reprompts=%d last=%v err=%v, want %d at %v", st.Reprompts, st.LastRepromptAt, err, want, wantAt)
				}
			}
			spent(1, t0)
			if hold == "recovers" {
				// Seeing the session active is not yet a recovery: the re-prompt
				// itself makes it active. A turn that ends without an error is.
				p.obs, p.sendErr = decide.Observation{State: decide.Active}, nil
				tick(5 * time.Minute)
				spent(1, t0)
				p.obs = decide.Observation{State: decide.Idle}
				if res := tick(6 * time.Minute); res.Action != "wake" || len(p.sent) != 2 {
					t.Fatalf("%+v", res)
				}
				spent(0, time.Time{})
				p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
				for i, at := range []time.Duration{7 * time.Minute, 17 * time.Minute} {
					if res := tick(at); res.Action != "reprompt" || len(p.sent) != i+3 {
						t.Fatalf("after recovery at %v: %+v sends=%d", at, res, len(p.sent))
					}
				}
				return
			}
			if _, err := control.Update(w.dir, func(f *control.File) { f.Paused, f.Stop = hold == "pause", hold == "stop" }); err != nil {
				t.Fatal(err)
			}
			for _, at := range []time.Duration{10 * time.Minute, 20 * time.Minute, time.Hour} {
				if res := tick(at); res.Action != "none" || len(p.sent) != 1 {
					t.Fatalf("%s at %v: %+v sends=%d", hold, at, res, len(p.sent))
				}
			}
			p.obs = decide.Observation{State: decide.Active}
			res := tick(2 * time.Hour)
			if want := map[string]string{"pause": "none", "stop": "stop"}[hold]; res.Action != want || len(p.sent) != 1 {
				t.Fatalf("active under %s: %+v", hold, res)
			}
			if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
				t.Fatal("hold replaced by an escalation")
			}
			spent(1, t0)
		})
	}
}

// A wake whose adapter call fails may still have started a turn, so its time is
// recorded and the next stall nudge is spaced from it.
func TestFailedWakeRecordsLastSendAndSpacesTheNextNudge(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/example/repo"}
	p := w.primary(decide.Observation{State: decide.Idle})
	p.sendErr = errors.New("uncertain delivery")
	tick := func(at time.Duration) (Result, error) {
		opts := w.opts(t0.Add(at))
		opts.Fingerprint = func([]string) (string, error) { return "unchanged", nil }
		return Tick(context.Background(), w.cfg, opts)
	}
	if res, err := tick(0); err == nil || res.Action != "wake" || len(p.sent) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	st, err := state.Load(w.dir)
	if err != nil || !st.LastWakeAt.Equal(t0) || st.Nudges != 0 || st.Reprompts != 0 {
		t.Fatalf("lastWakeAt=%v nudges=%d reprompts=%d err=%v", st.LastWakeAt, st.Nudges, st.Reprompts, err)
	}
	if st.LastCheck == nil || st.LastCheck.Outcome != "failed" || st.LastPromptSend == nil || st.LastPromptSend.Outcome != "adapter_error" {
		t.Fatalf("check=%+v send=%+v", st.LastCheck, st.LastPromptSend)
	}
	// The turn did start. It is stalled after 30m, but min_wake_interval is 1h.
	p.obs, p.sendErr = decide.Observation{State: decide.Active}, nil
	if res, err := tick(40 * time.Minute); err != nil || res.Action != "none" || len(p.sent) != 1 {
		t.Fatalf("nudged inside the wake interval: %+v %v", res, err)
	}
	if res, err := tick(61 * time.Minute); err != nil || res.Action != "nudge" || len(p.sent) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}

// An unsupported send is also recorded as an attempt, but the check itself
// succeeds: only the prompt-send record and the warning show it.
func TestUnsupportedSendIsRecordedOnASucceededCheck(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	p.sendErr = adapter.ErrUnsupported{Op: "send"}
	res := runTick(t, w, t0)
	st, err := state.Load(w.dir)
	if err != nil || res.Action != "reprompt" || len(res.Warnings) != 1 || st.Reprompts != 1 || !st.LastWakeAt.Equal(t0) {
		t.Fatalf("%+v reprompts=%d lastWakeAt=%v err=%v", res, st.Reprompts, st.LastWakeAt, err)
	}
	if st.LastCheck.Outcome != "succeeded" || st.LastPromptSend.Outcome != "adapter_error" {
		t.Fatalf("check=%+v send=%+v", st.LastCheck, st.LastPromptSend)
	}
}
