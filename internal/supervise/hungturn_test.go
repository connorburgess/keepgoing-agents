package supervise

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// hungWorld is an active turn, tracked by Git, that last logged something
// five minutes before t0. stall_after 30m, min_wake_interval 1h, three
// nudges, checks every ten minutes.
// Each configure function may change the loop before the first check.
func hungWorld(t *testing.T, configure ...func(*config.Loop)) (*world, *fakeAdapter, func(at time.Duration, online bool, print string) Result) {
	t.Helper()
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	w.cfg.Loop.Progress.MaxNudges = 3
	w.cfg.Loop.Daemon.Interval = config.Duration{Duration: 10 * time.Minute}
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	for _, c := range configure {
		c(&w.cfg.Loop)
	}
	p := w.primary(decide.Observation{State: decide.Active, Turn: &decide.TurnActivity{ID: "turn-example-1", LastActivityAt: t0.Add(-5 * time.Minute)}})
	run := func(at time.Duration, online bool, print string) Result {
		t.Helper()
		opts := w.opts(t0.Add(at))
		opts.Fingerprint = func([]string) (string, error) { return print, nil }
		opts.Online = func(context.Context) bool { return online }
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatalf("+%s: %v", at, err)
		}
		return r
	}
	return w, p, run
}

// hungJourney runs the outage: online checks, then offline ones (every ten
// minutes for an hour, then every seven hours), then online checks every ten
// minutes from back. It fails if anything is sent or held while offline.
func hungJourney(t *testing.T, length time.Duration, configure ...func(*config.Loop)) (w *world, p *fakeAdapter, run func(time.Duration, bool, string) Result, back time.Duration) {
	w, p, run = hungWorld(t, configure...)
	run(0, true, "same")
	run(10*time.Minute, true, "same")
	drop := 15 * time.Minute
	for off := drop + 5*time.Minute; off < drop+length; {
		r := run(off, false, "same")
		if len(p.sent) != 0 || r.Action != "none" {
			t.Fatalf("+%s offline: %s (%s)", off, r.Action, r.Reason)
		}
		if off-drop < time.Hour {
			off += 10 * time.Minute
		} else {
			off += 7 * time.Hour
		}
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("held while offline")
	}
	return w, p, run, drop + length + 5*time.Minute
}

// waitFrom runs the checks from back to back+40m, every ten minutes, and
// expects the hung-turn wait to have begun by the last.
func waitFrom(t *testing.T, run func(time.Duration, bool, string) Result, back time.Duration) {
	t.Helper()
	var r Result
	for after := time.Duration(0); after <= 40*time.Minute; after += 10 * time.Minute {
		r = run(back+after, true, "same")
	}
	if r.Reason != decide.HungTurnReason {
		t.Fatalf("the wait has not begun: %s (%s)", r.Action, r.Reason)
	}
}

// The owner's scenario with a turn whose host slept mid-request: it stays
// active and silent through the outage. Once the network is back, the check
// waits stall_after as for any turn, then waits on it instead of nudging and
// reports that once, and asks a person when the stall rule would have held
// (stall_after 30m, then three nudges an hour apart: 3h30m after the network
// came back) with a reason that says what happened. At every length, no nudge
// is sent.
func TestHungTurnIsWaitedOnThenHeldAtAnyOutageLength(t *testing.T) {
	for _, length := range []time.Duration{20 * time.Minute, 4 * time.Hour, 72 * time.Hour, 21 * 24 * time.Hour} {
		t.Run(length.String(), func(t *testing.T) {
			w, p, run, back := hungJourney(t, length)
			var got []string
			hold := 3*time.Hour + 30*time.Minute
			for after := time.Duration(0); after <= hold; after += 10 * time.Minute {
				r := run(back+after, true, "same")
				got = append(got, fmt.Sprintf("%s:%s", after, r.Reason))
			}
			want := map[time.Duration]string{}
			for after := time.Duration(0); after <= hold; after += 10 * time.Minute {
				switch {
				case after < 30*time.Minute:
					want[after] = "active"
				case after < hold:
					want[after] = decide.HungTurnReason
				default:
					want[after] = decide.HungTurnHoldReason
				}
			}
			for i, after := 0, time.Duration(0); after <= hold; i, after = i+1, after+10*time.Minute {
				if got[i] != fmt.Sprintf("%s:%s", after, want[after]) {
					t.Fatalf("after the network came back: %s, want %s", got[i], want[after])
				}
			}
			if len(p.sent) != 0 {
				t.Fatalf("%d sends", len(p.sent))
			}
			if strings.Count(w.reportTypes(), "hung_turn") != 1 || strings.Count(w.reportTypes(), "await_user") != 1 {
				t.Fatalf("reports %s", w.reportTypes())
			}
			if ctl, _ := control.Load(w.dir); !ctl.AwaitingUser || ctl.Note != decide.HungTurnHoldReason {
				t.Fatalf("%+v", ctl)
			}
		})
	}
}

// The wait ends by itself. A turn that resumes and does work is handled by
// the ordinary rules; one that fails is recovered as a failed turn. Neither
// is nudged, and neither is held.
func TestHungTurnWaitEndsWhenTheTurnResumesOrFails(t *testing.T) {
	for _, length := range []time.Duration{20 * time.Minute, 4 * time.Hour, 72 * time.Hour, 21 * 24 * time.Hour} {
		t.Run(length.String()+"/resumes", func(t *testing.T) {
			_, p, run, back := hungJourney(t, length)
			waitFrom(t, run, back)
			// It logs again and commits, then its turn ends.
			p.obs.Turn = &decide.TurnActivity{ID: "turn-example-1", LastActivityAt: t0.Add(back + 45*time.Minute)}
			if r := run(back+50*time.Minute, true, "changed"); r.Action != "none" || r.Reason != "active" {
				t.Fatalf("resumed: %s (%s)", r.Action, r.Reason)
			}
			p.obs = decide.Observation{State: decide.Idle}
			if r := run(back+60*time.Minute, true, "changed"); r.Action != "wake" {
				t.Fatalf("idle: %s (%s)", r.Action, r.Reason)
			}
			for i, m := range p.modes {
				if m == "queue" {
					t.Fatalf("send %d was a nudge", i)
				}
			}
		})
		t.Run(length.String()+"/fails", func(t *testing.T) {
			w, p, run, back := hungJourney(t, length)
			waitFrom(t, run, back)
			p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
			if r := run(back+50*time.Minute, true, "same"); r.Action != "reprompt" || len(p.sent) != 1 || p.modes[0] == "queue" {
				t.Fatalf("failed: %s (%s) sends %d", r.Action, r.Reason, len(p.sent))
			}
			if st, _ := state.Load(w.dir); st.HungTurn != nil {
				t.Fatalf("the watch outlived the turn: %+v", st.HungTurn)
			}
			if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
				t.Fatal("held")
			}
		})
	}
}

// Every sign must hold. With each one failing in turn, the check does not
// wait on a hung turn and sends no hung_turn report: the ordinary rules
// apply, which here nudge a turn that has been silent with no Git change.
func TestHungTurnNeedsEverySign(t *testing.T) {
	for _, tc := range []struct {
		name   string
		online bool // the network was never down
		change func(o *decide.Observation)
		print  string
		reason string
	}{
		{name: "all hold", reason: decide.HungTurnReason},
		// With no outage the turn is nudged as usual when it has been silent
		// for stall_after, at +40m, and is inside min_wake_interval at +90m.
		{name: "no outage", online: true, reason: "active"},
		{name: "another turn", change: func(o *decide.Observation) {
			o.Turn = &decide.TurnActivity{ID: "turn-example-2", LastActivityAt: t0.Add(-5 * time.Minute)}
		}, reason: "no progress for"},
		// A turn that came back is not nudged until it has been silent for
		// stall_after again.
		{name: "activity after the outage", change: func(o *decide.Observation) { o.Turn.LastActivityAt = t0.Add(80 * time.Minute) }, reason: decide.HungTurnGraceReason},
		{name: "no turn seen", change: func(o *decide.Observation) { o.Turn = nil }, reason: "no progress for"},
		// The log could not be read: nothing is decided, and the watch is
		// kept (TestHungTurnWatchSurvivesAFailedLogRead).
		{name: "activity unknown", change: func(o *decide.Observation) { o.Turn, o.TurnUnknown, o.RetryUnverified = nil, true, true }, reason: "provider retry state unverified"},
		{name: "background agents", change: func(o *decide.Observation) { o.Background = 1 }, reason: "no progress for"},
		{name: "a tool is running", change: func(o *decide.Observation) { o.Turn.ToolsRunning = true }, reason: "no progress for"},
		{name: "something queued", change: func(o *decide.Observation) { o.Queued = 1 }, reason: "no progress for"},
		{name: "children running", change: func(o *decide.Observation) { o.ActiveChildren = 1 }, reason: "child work running"},
		{name: "a person is asked", change: func(o *decide.Observation) { o.AwaitingInput = true }, reason: "waiting on a person"},
		{name: "a provider retry", change: func(o *decide.Observation) { o.Retrying = true }, reason: "provider is retrying the turn"},
		{name: "a Git change", print: "changed", reason: "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, p, run := hungWorld(t)
			run(0, true, "same")
			run(10*time.Minute, true, "same")
			run(20*time.Minute, true, "same")
			run(40*time.Minute, tc.online, "same") // the turn is silent and the network down
			run(50*time.Minute, true, "same")
			run(60*time.Minute, true, "same")
			if tc.change != nil {
				o := p.obs
				if o.Turn != nil {
					turn := *o.Turn
					o.Turn = &turn
				}
				tc.change(&o)
				p.obs = o
			}
			print := "same"
			if tc.print != "" {
				print = tc.print
			}
			// The network has been back for 40 minutes, the turn active and
			// unchanged for an hour and a half.
			r := run(90*time.Minute, true, print)
			if !strings.HasPrefix(r.Reason, tc.reason) {
				t.Fatalf("%s (%s), want %q", r.Action, r.Reason, tc.reason)
			}
			if (tc.reason == decide.HungTurnReason) != strings.Contains(w.reportTypes(), "hung_turn") {
				t.Fatalf("reports %s", w.reportTypes())
			}
			if tc.online && (len(p.modes) != 1 || p.modes[0] != "queue") {
				t.Fatalf("not nudged as usual: %v", p.modes)
			}
		})
	}
}

// Stop and pause win over the wait, and nothing is decided while the
// network is down again.
func TestHungTurnWaitYieldsToControlsAndTheNetworkGate(t *testing.T) {
	for _, hold := range []string{"pause", "stop"} {
		w, p, run, back := hungJourney(t, 4*time.Hour)
		waitFrom(t, run, back)
		control.Update(w.dir, func(f *control.File) {
			if hold == "pause" {
				f.Paused = true
			} else {
				f.Stop = true
			}
		})
		r := run(back+50*time.Minute, true, "same")
		if hold == "pause" && (r.Action != "none" || r.Reason != "paused") || hold == "stop" && (r.Action != "stop" || p.stopped != 1) {
			t.Fatalf("%s: %s (%s)", hold, r.Action, r.Reason)
		}
	}
	_, p, run, back := hungJourney(t, 4*time.Hour)
	waitFrom(t, run, back)
	// The network drops again before the patience is spent.
	if r := run(back+3*time.Hour, false, "same"); r.Action != "none" || r.Reason == decide.HungTurnHoldReason || len(p.sent) != 0 {
		t.Fatalf("%s (%s)", r.Action, r.Reason)
	}
}
