package supervise

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// stalledWorld is a loop whose primary stays active and never changes Git:
// stall_after 30m, min_wake_interval 1h, three nudges, then a hold.
func stalledWorld(t *testing.T) (*world, *fakeAdapter, func(after time.Duration, online bool) Result, *int) {
	t.Helper()
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	w.cfg.Loop.Progress.MaxNudges = 3
	w.cfg.Loop.Daemon.Interval = config.Duration{Duration: config.DefaultDaemonInterval}
	p := w.primary(decide.Observation{State: decide.Active})
	checks := 0
	run := func(after time.Duration, online bool) Result {
		t.Helper()
		opts := w.opts(t0.Add(after))
		opts.Fingerprint = func([]string) (string, error) { return "same", nil }
		opts.Online = func(context.Context) bool { checks++; return online }
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	return w, p, run, &checks
}

// A scheduler that is simply slow is not an outage. A session that is
// genuinely stalled is nudged and then held at the same points whether checks
// run every ten minutes, every thirty-one or every hour: each at the first
// check on or after the time the rule gives, and no_progress_timeout holds at
// the first check on or after its configured time.
func TestStalledSessionIsNudgedAndHeldOnScheduleAtAnyCadence(t *testing.T) {
	for _, every := range []time.Duration{10 * time.Minute, 31 * time.Minute, time.Hour} {
		t.Run(every.String(), func(t *testing.T) {
			_, p, run, _ := stalledWorld(t)
			// The first check on or after d.
			at := func(d time.Duration) time.Duration { return (d + every - 1) / every * every }
			nudge1 := at(30 * time.Minute)
			nudge2 := at(nudge1 + time.Hour)
			nudge3 := at(nudge2 + time.Hour)
			hold := at(nudge3 + time.Hour)
			want := map[time.Duration]string{nudge1: "nudge", nudge2: "nudge", nudge3: "nudge", hold: "await_user"}
			for after := time.Duration(0); after <= hold; after += every {
				r := run(after, true)
				expected := want[after]
				if expected == "" {
					expected = "none"
				}
				if r.Action != expected {
					t.Fatalf("+%s: %s (%s), want %s", after, r.Action, r.Reason, expected)
				}
			}
			if len(p.sent) != 3 {
				t.Fatalf("nudges sent: %d", len(p.sent))
			}
		})
		t.Run(every.String()+" no_progress_timeout", func(t *testing.T) {
			w, _, run, _ := stalledWorld(t)
			w.cfg.Loop.Limits.NoProgressTimeout = config.Duration{Duration: 2 * time.Hour}
			due := (2*time.Hour + every - 1) / every * every
			for after := time.Duration(0); after <= due; after += every {
				r := run(after, true)
				held := r.Action == "await_user" && strings.HasPrefix(r.Reason, "no Git progress within")
				if held != (after == due) {
					t.Fatalf("+%s: %s (%s); the limit is due at +%s", after, r.Action, r.Reason, due)
				}
			}
		})
	}
}

// Where only a gap is known, with no evidence of an outage, the stall clock
// is paused for the time no check ran. It is not restarted: the active time
// before the gap still counts.
func TestGapWithNoCheckPausesTheStallClock(t *testing.T) {
	_, p, run, _ := stalledWorld(t)
	for m := 0; m <= 20; m += 10 {
		run(time.Duration(m)*time.Minute, true) // twenty minutes active, checked every ten
	}
	// The host sleeps for nine hours. Of the gap, one usual interval counts:
	// thirty minutes of active time, which is a stall.
	if r := run(20*time.Minute+9*time.Hour, true); r.Action != "nudge" || !strings.HasPrefix(r.Reason, "no progress for 30m") {
		t.Fatalf("after the gap: %+v", r)
	}
	// A gap right after the session was first seen: the ten minutes before it
	// and one usual interval of it count, so the nudge comes ten minutes on.
	_, p, run, _ = stalledWorld(t)
	run(0, true)
	run(10*time.Minute, true)
	back := 10*time.Minute + 72*time.Hour
	if r := run(back, true); r.Action != "none" || len(p.sent) != 0 {
		t.Fatalf("nudged for time in which nothing ran: %+v", r)
	}
	if r := run(back+10*time.Minute, true); r.Action != "nudge" || !strings.HasPrefix(r.Reason, "no progress for 30m") {
		t.Fatalf("+10m: %+v", r)
	}
}

// The owner's scenario with a turn that never fails: the host sleeps in the
// middle of a request, so the session stays active. Offline it is not nudged
// and not held, the outage is recorded, the stall clock is paused, and the
// first nudge comes stall_after after the network is back.
func TestHungActiveTurnIsNotNudgedOrHeldWhileOffline(t *testing.T) {
	for _, length := range []time.Duration{20 * time.Minute, 4 * time.Hour, 72 * time.Hour, 21 * 24 * time.Hour} {
		t.Run(length.String(), func(t *testing.T) {
			w, p, run, checks := stalledWorld(t)
			run(0, true)
			run(10*time.Minute, true)
			drop := 20 * time.Minute
			known := false
			for after := drop; after < drop+length; after += 10 * time.Minute {
				r := run(after, false)
				if r.Action != "none" || len(p.sent) != 0 {
					t.Fatalf("+%s offline: %+v", after, r)
				}
				if r.Network == "down" {
					known = true
				} else if known {
					t.Fatalf("+%s: the outage was forgotten while it lasted: %+v", after, r)
				}
				if after-drop > 2*time.Hour && after-drop < length-2*time.Hour {
					after = drop + length - 2*time.Hour // the host sleeps through the middle
				}
			}
			if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
				t.Fatal("held while offline")
			}
			if !known || *checks == 0 {
				t.Fatalf("the outage was never noticed: %d connectivity checks", *checks)
			}
			back := drop + length
			var got []string
			for after := back; after <= back+40*time.Minute; after += 10 * time.Minute {
				r := run(after, true)
				got = append(got, r.Action)
			}
			// The clock starts when the network is seen back.
			want := "none,none,none,nudge,none"
			if s := strings.Join(got, ","); s != want || len(p.sent) != 1 {
				t.Fatalf("after the outage: %s, want %s (sends %d)", s, want, len(p.sent))
			}
			if st, _ := state.Load(w.dir); !st.NetworkDownSince.IsZero() {
				t.Fatalf("status still shows the outage: %+v", st.NetworkDownSince)
			}
		})
	}
}

// Without Git tracking the stall rule is the silent-retry hold. It is gated
// in the same way, and its clock does not count the outage.
func TestSilentRetryHoldWaitsForTheNetwork(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.MaxNudges = 3
	p := w.primary(decide.Observation{State: decide.Active, RetryExpired: true})
	run := func(after time.Duration, online bool) Result {
		t.Helper()
		opts := w.opts(t0.Add(after))
		opts.Online = func(context.Context) bool { return online }
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	patience := decide.SilentRetryPatience(30*time.Minute, time.Hour, 3)
	for after := time.Duration(0); after <= 12*time.Hour; after += 10 * time.Minute {
		if r := run(after, false); r.Action != "none" || len(p.sent) != 0 {
			t.Fatalf("+%s offline: %+v", after, r)
		}
	}
	back := 12*time.Hour + 10*time.Minute
	for after := back; after < back+patience; after += 10 * time.Minute {
		if r := run(after, true); r.Action != "none" {
			t.Fatalf("+%s online: held for the outage: %+v", after-back, r)
		}
	}
	if r := run(back+patience, true); r.Action != "await_user" || !strings.Contains(r.Reason, "provider retry went silent") {
		t.Fatalf("the hold no longer applies: %+v", r)
	}
}
