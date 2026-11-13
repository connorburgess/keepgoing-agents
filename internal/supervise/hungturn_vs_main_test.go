package supervise

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// Only the hung-turn watch reads the observed turn, so the same journey with
// no turn observed is what the stall rule alone (main) does.

// step is one check: when, whether the network works, the Git fingerprint,
// and what the session looks like.
type step struct {
	at     time.Duration
	online bool
	print  string
	obs    decide.Observation
}

// side is one run of a journey.
type side struct {
	w       *world
	p       *fakeAdapter
	results []Result
	probes  int
}

func (s side) holdAt(steps []step) (time.Duration, bool) {
	for i, r := range s.results {
		if r.Action == "await_user" {
			return steps[i].at, true
		}
	}
	return 0, false
}

func (s side) nudges() int {
	n := 0
	for _, m := range s.p.modes {
		if m == "queue" {
			n++
		}
	}
	return n
}

func runSide(t *testing.T, steps []step, withTurn bool, configure ...func(*config.Loop)) side {
	t.Helper()
	w, p, _ := hungWorld(t, configure...)
	s := side{w: w, p: p}
	for _, st := range steps {
		o := st.obs
		if o.Turn != nil && withTurn {
			turn := *o.Turn
			o.Turn = &turn
		} else {
			o.Turn = nil
		}
		p.obs = o
		opts := w.opts(t0.Add(st.at))
		print := st.print
		if print == "" {
			print = "same"
		}
		opts.Fingerprint = func([]string) (string, error) { return print, nil }
		online := st.online
		opts.Online = func(context.Context) bool { s.probes++; return online }
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatalf("+%s: %v", st.at, err)
		}
		s.results = append(s.results, r)
	}
	return s
}

// compare runs a journey with and without the watch, and checks the watch
// never holds later than the stall rule, never nudges more, and never asks
// for connectivity more often.
func compare(t *testing.T, steps []step, configure ...func(*config.Loop)) (branch, main side) {
	t.Helper()
	branch, main = runSide(t, steps, true, configure...), runSide(t, steps, false, configure...)
	bh, bok := branch.holdAt(steps)
	mh, mok := main.holdAt(steps)
	if mok && (!bok || bh > mh) {
		t.Errorf("held at %s (held %v), the stall rule at %s", bh, bok, mh)
	}
	if branch.nudges() > main.nudges() {
		t.Errorf("%d nudges, the stall rule %d", branch.nudges(), main.nudges())
	}
	if branch.probes > main.probes {
		t.Errorf("%d connectivity checks, the stall rule %d", branch.probes, main.probes)
	}
	if b, m := strings.Count(branch.w.reportTypes(), "network_wait"), strings.Count(main.w.reportTypes(), "network_wait"); b > m {
		t.Errorf("%d network_wait reports, the stall rule %d", b, m)
	}
	return branch, main
}

func every(from, to, interval time.Duration, online func(time.Duration) bool, obs func(time.Duration) decide.Observation) []step {
	var out []step
	for at := from; at <= to; at += interval {
		out = append(out, step{at: at, online: online(at), obs: obs(at)})
	}
	return out
}

func always(time.Duration) bool { return true }

func silentTurn(id string, last time.Duration) decide.Observation {
	return decide.Observation{State: decide.Active, Turn: &decide.TurnActivity{ID: id, LastActivityAt: t0.Add(last)}}
}

// The watch checks connectivity only when the stall rule would: never for
// a parent waiting on a child, a long tool or a message queued, and no more
// often for a silent turn through a long outage.
func TestHungTurnNeverChecksConnectivityMoreThanTheStallRule(t *testing.T) {
	offlineFor := func(from, to time.Duration) func(time.Duration) bool {
		return func(at time.Duration) bool { return at < from || at >= to }
	}
	for _, tc := range []struct {
		name   string
		online func(time.Duration) bool
		obs    func(o *decide.Observation)
		until  time.Duration
	}{
		{"a parent waiting on a child, 6h", always, func(o *decide.Observation) { o.ActiveChildren = 1 }, 6 * time.Hour},
		{"a parent waiting on a child through a 2h outage", offlineFor(time.Hour, 3*time.Hour), func(o *decide.Observation) { o.ActiveChildren = 1 }, 6 * time.Hour},
		{"a long tool through a 2h outage", offlineFor(time.Hour, 3*time.Hour), func(o *decide.Observation) { o.Turn.ToolsRunning = true }, 6 * time.Hour},
		{"a message queued", always, func(o *decide.Observation) { o.Queued = 1 }, 6 * time.Hour},
		{"a silent turn, 24h offline", offlineFor(time.Hour, 25*time.Hour), func(*decide.Observation) {}, 30 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := every(0, tc.until, 10*time.Minute, tc.online, func(time.Duration) decide.Observation {
				o := silentTurn("turn-example-1", -5*time.Minute)
				tc.obs(&o)
				return o
			})
			compare(t, steps)
		})
	}
}

// A turn that logged during a gap, or since the computer woke, was not
// hung through it: no watch is made, and the stall clock is left alone, so
// the stall rule nudges and holds exactly as it would have.
func TestHungTurnLeavesATurnThatKeptLoggingToTheStallRule(t *testing.T) {
	t.Run("the scheduler down for 2h, the turn logging", func(t *testing.T) {
		var steps []step
		for at := time.Duration(0); at <= 6*time.Hour; at += 10 * time.Minute {
			if at > time.Hour && at < 3*time.Hour {
				continue
			}
			steps = append(steps, step{at: at, online: true, obs: silentTurn("turn-example-1", at-time.Minute)})
		}
		branch, main := compare(t, steps)
		if fmt.Sprint(actions(branch)) != fmt.Sprint(actions(main)) {
			t.Fatalf("\nwatch %v\nmain  %v", actions(branch), actions(main))
		}
	})
	t.Run("25m of checks then a 40m gap, for 12h, the turn logging", func(t *testing.T) {
		var steps []step
		for cycle := time.Duration(0); cycle < 12*time.Hour; cycle += 65 * time.Minute {
			for at := cycle; at <= cycle+25*time.Minute; at += 5 * time.Minute {
				steps = append(steps, step{at: at, online: true, obs: silentTurn("turn-example-1", at-time.Minute)})
			}
		}
		set := func(l *config.Loop) { l.Daemon.Interval = config.Duration{Duration: 5 * time.Minute} }
		branch, main := compare(t, steps, set)
		if fmt.Sprint(actions(branch)) != fmt.Sprint(actions(main)) {
			t.Fatalf("\nwatch %v\nmain  %v", actions(branch), actions(main))
		}
		if main.nudges() == 0 {
			t.Fatal("the journey never nudges: it tests nothing")
		}
	})
}

func actions(s side) []string {
	var out []string
	for _, r := range s.results {
		out = append(out, r.Action)
	}
	return out
}

// The lid-close journey: checks every 5m from 0 to 20m, the turn's last log
// at 23m, the computer asleep from 24m to 2h, no connectivity check ever
// failing. The gap is the evidence, and silence that began before the wake
// counts. Earlier and later last logs, too.
func TestHungTurnAfterASleepWithNoFailedCheck(t *testing.T) {
	set := func(l *config.Loop) { l.Daemon.Interval = config.Duration{Duration: 5 * time.Minute} }
	for _, lastLog := range []time.Duration{17 * time.Minute, 23 * time.Minute} {
		t.Run(fmt.Sprintf("last log at %s", lastLog), func(t *testing.T) {
			var steps []step
			for at := time.Duration(0); at <= 20*time.Minute; at += 5 * time.Minute {
				steps = append(steps, step{at: at, online: true, obs: silentTurn("turn-example-1", min(at, lastLog)-time.Minute)})
			}
			for at := 2 * time.Hour; at <= 8*time.Hour; at += 5 * time.Minute {
				steps = append(steps, step{at: at, online: true, obs: silentTurn("turn-example-1", lastLog)})
			}
			branch, _ := compare(t, steps, set)
			if branch.nudges() != 0 || !strings.Contains(branch.w.reportTypes(), "hung_turn") {
				t.Fatalf("nudges %d, reports %s", branch.nudges(), branch.w.reportTypes())
			}
			if _, ok := branch.holdAt(steps); !ok {
				t.Fatal("never held")
			}
		})
	}
}

// Patience counts what the stall rule would still do: nudges already sent
// shorten it. A nudge queued behind turn A starts turn B, and B hangs in an
// outage; and a turn that logs once during the wait. Neither is held later
// than the stall rule would hold it.
func TestHungTurnPatienceCountsNudgesAlreadySent(t *testing.T) {
	t.Run("a nudge behind turn A starts turn B, which hangs", func(t *testing.T) {
		var steps []step
		for at := time.Duration(0); at <= 12*time.Hour; at += 10 * time.Minute {
			o := silentTurn("turn-example-A", -5*time.Minute)
			if at >= 50*time.Minute { // the nudge at 30m started turn B at 45m
				o = silentTurn("turn-example-B", 45*time.Minute)
			}
			steps = append(steps, step{at: at, online: at < time.Hour || at >= 3*time.Hour, obs: o})
		}
		branch, main := compare(t, steps)
		if main.nudges() == 0 || branch.nudges() == 0 {
			t.Fatalf("the nudge before turn B was not sent: watch %d, main %d", branch.nudges(), main.nudges())
		}
	})
	t.Run("a turn that logs once during the wait", func(t *testing.T) {
		var steps []step
		for at := time.Duration(0); at <= 12*time.Hour; at += 10 * time.Minute {
			last := -5 * time.Minute
			if at >= 4*time.Hour+50*time.Minute {
				last = 4*time.Hour + 45*time.Minute // one log, after the wait began
			}
			steps = append(steps, step{at: at, online: at < 15*time.Minute || at >= 4*time.Hour, obs: silentTurn("turn-example-1", last)})
		}
		branch, _ := compare(t, steps)
		if !strings.Contains(branch.w.reportTypes(), "hung_turn") {
			t.Fatalf("the wait never began: %s", branch.w.reportTypes())
		}
	})
}

// Outside the operating window the watch does nothing: no wait, no report.
func TestHungTurnDoesNothingOutsideTheWindow(t *testing.T) {
	closed := func(l *config.Loop) {
		l.Window = &config.Window{TZ: "UTC", Days: map[string][]string{"Sun": {"00:00", "00:01"}}}
	}
	w, p, run, back := hungJourney(t, 4*time.Hour, closed)
	for after := time.Duration(0); after <= 5*time.Hour; after += 10 * time.Minute {
		if r := run(back+after, true, "same"); r.Reason == decide.HungTurnReason || r.Action == "await_user" {
			t.Fatalf("+%s outside the window: %s (%s)", after, r.Action, r.Reason)
		}
	}
	if len(p.sent) != 0 || strings.Contains(w.reportTypes(), "hung_turn") {
		t.Fatalf("%d sends, reports %s", len(p.sent), w.reportTypes())
	}
}
