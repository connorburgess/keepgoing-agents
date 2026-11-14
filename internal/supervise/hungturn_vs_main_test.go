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

func runSide(t *testing.T, steps []step, withTurn, fallbacks bool, configure ...func(*config.Loop)) side {
	t.Helper()
	w, p, _ := hungWorldWith(t, fallbacks, configure...)
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

// compare runs a journey with and without the watch and checks they act
// alike at every check: the same action, except that a nudge without the
// watch is a held-back nudge with it; the same hold; the same connectivity
// checks, provider probes and network_wait reports.
func compare(t *testing.T, steps []step, configure ...func(*config.Loop)) (branch, main side) {
	t.Helper()
	return compareWith(t, false, steps, configure...)
}

func compareWith(t *testing.T, fallbacks bool, steps []step, configure ...func(*config.Loop)) (branch, main side) {
	t.Helper()
	branch, main = runSide(t, steps, true, fallbacks, configure...), runSide(t, steps, false, fallbacks, configure...)
	for i := range steps {
		b, m := branch.results[i], main.results[i]
		same := b.Action == m.Action && b.Reason != decide.HungTurnNudgeReason
		if m.Action == "nudge" && b.Action == "none" {
			same = b.Reason == decide.HungTurnNudgeReason // held back
		}
		if !same {
			t.Errorf("+%s: watch %s (%s), stall rule %s (%s)", steps[i].at, b.Action, b.Reason, m.Action, m.Reason)
		}
	}
	bh, bok := branch.holdAt(steps)
	mh, mok := main.holdAt(steps)
	if bok != mok || bh != mh {
		t.Errorf("held at %s (%v), the stall rule at %s (%v)", bh, bok, mh, mok)
	}
	if branch.probes != main.probes {
		t.Errorf("%d connectivity checks, the stall rule %d", branch.probes, main.probes)
	}
	if b, m := strings.Join(branch.w.probes, ","), strings.Join(main.w.probes, ","); b != m {
		t.Errorf("provider probes %q, the stall rule %q", b, m)
	}
	if b, m := strings.Count(branch.w.reportTypes(), "network_wait"), strings.Count(main.w.reportTypes(), "network_wait"); b != m {
		t.Errorf("%d network_wait reports, the stall rule %d", b, m)
	}
	if len(branch.p.sent) > len(main.p.sent) {
		t.Errorf("%d sends, the stall rule %d", len(branch.p.sent), len(main.p.sent))
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

func (s side) heldNudges() int {
	n := 0
	for _, r := range s.results {
		if r.Reason == decide.HungTurnNudgeReason {
			n++
		}
	}
	return n
}

// journey builds checks from `from` to `to` every `interval`, the network
// working except where offline says, the session as obs says.
func journey(from, to, interval time.Duration, offline func(time.Duration) bool, obs func(time.Duration) decide.Observation) []step {
	var out []step
	for at := from; at <= to; at += interval {
		out = append(out, step{at: at, online: !offline(at), obs: obs(at)})
	}
	return out
}

func silentSince(last time.Duration) func(time.Duration) decide.Observation {
	return func(time.Duration) decide.Observation { return silentTurn("turn-example-1", last) }
}

func between(spans ...[2]time.Duration) func(time.Duration) bool {
	return func(at time.Duration) bool {
		for _, s := range spans {
			if at >= s[0] && at < s[1] {
				return true
			}
		}
		return false
	}
}

// The journeys the third review ran, with the watch on and off. In each the
// watch acts exactly as the stall rule does, except that it holds back the
// nudges: the same hold, at the same check, the same connectivity checks,
// provider probes and network_wait reports.
func TestHungTurnActsAsTheStallRuleExceptForTheNudges(t *testing.T) {
	h, m := time.Hour, time.Minute
	every5 := func(l *config.Loop) { l.Daemon.Interval = config.Duration{Duration: 5 * time.Minute} }
	for _, tc := range []struct {
		name      string
		steps     []step
		fallbacks bool
		configure []func(*config.Loop)
	}{
		{name: "A: an outage, the network back, then a second outage",
			steps: journey(0, 16*h, 10*m, between([2]time.Duration{15 * m, 4*h + 15*m}, [2]time.Duration{5*h + 5*m, 9*h + 20*m}), silentSince(-5*m))},
		{name: "A2: an outage, then one failed check at 7h50m",
			steps: journey(0, 14*h, 10*m, between([2]time.Duration{15 * m, 4*h + 15*m}, [2]time.Duration{7*h + 50*m, 7*h + 51*m}), silentSince(-5*m))},
		{name: "B: a lid close, then a second sleep",
			steps: func() []step {
				var s []step
				for _, span := range [][2]time.Duration{{0, 20 * m}, {2 * h, 4 * h}, {6 * h, 12 * h}} {
					s = append(s, journey(span[0], span[1], 5*m, between(), silentSince(23*m))...)
				}
				return s
			}(), configure: []func(*config.Loop){every5}},
		{name: "D: checks every 25m, an outage, then one failed check",
			steps: journey(0, 14*h, 25*m, between([2]time.Duration{h, 3 * h}, [2]time.Duration{6*h + 40*m, 6*h + 41*m}), silentSince(-5*m))},
		{name: "D3: the same, with fallbacks to probe",
			steps: journey(0, 14*h, 25*m, between([2]time.Duration{h, 3 * h}, [2]time.Duration{6*h + 40*m, 6*h + 41*m}), silentSince(-5*m)), fallbacks: true},
		{name: "F: checks every 9m59s, an outage, then one failed check",
			steps: journey(0, 12*h, 9*m+59*time.Second, between([2]time.Duration{15 * m, 2 * h}, [2]time.Duration{4*h + 59*m, 5*h + 9*m}), silentSince(-5*m))},
		{name: "G1: a tool opens 2h after the network is back",
			steps: journey(0, 12*h, 10*m, between([2]time.Duration{15 * m, 2 * h}), func(at time.Duration) decide.Observation {
				o := silentTurn("turn-example-1", -5*m)
				o.Turn.ToolsRunning = at >= 4*h
				return o
			})},
		{name: "G2: a message is queued 2h after the network is back",
			steps: journey(0, 12*h, 10*m, between([2]time.Duration{15 * m, 2 * h}), func(at time.Duration) decide.Observation {
				o := silentTurn("turn-example-1", -5*m)
				if at >= 4*h {
					o.Queued = 1
				}
				return o
			})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			branch, main := compareWith(t, tc.fallbacks, tc.steps, tc.configure...)
			if branch.heldNudges() == 0 {
				t.Fatalf("the watch never held a nudge back: it tests nothing (the stall rule nudged %d times)", main.nudges())
			}
			if tc.fallbacks && len(main.w.probes) == 0 {
				t.Fatal("no provider probe in the journey: it tests nothing")
			}
		})
	}
}

// A watched turn that logs again is handed back to the stall rule
// completely: one logging every 29 minutes after an outage is nudged as the
// stall rule nudges it, not held back.
func TestHungTurnThatLogsAgainIsHandedBack(t *testing.T) {
	h, m := time.Hour, time.Minute
	steps := journey(0, 10*h, 10*m, between([2]time.Duration{15 * m, 2 * h}), func(at time.Duration) decide.Observation {
		last := -5 * m
		if at >= 2*h+30*m {
			last = at - at%(29*m) // a log every 29 minutes
		}
		return silentTurn("turn-example-1", last)
	})
	branch, main := compare(t, steps)
	if main.nudges() == 0 || branch.nudges() != main.nudges()-branch.heldNudges() {
		t.Fatalf("nudges: watch %d sent and %d held, the stall rule %d", branch.nudges(), branch.heldNudges(), main.nudges())
	}
	for i, r := range branch.results {
		if steps[i].at > 2*h+30*m && (r.Reason == decide.HungTurnNudgeReason || r.Reason == decide.HungTurnReason) {
			t.Fatalf("+%s: still watched after it logged again: %s", steps[i].at, r.Reason)
		}
	}
}

// A turn counts as silent only once it has logged nothing for stall_after:
// one that logged until 1h45m while only the scheduler was down has no
// nudge held back before 2h15m.
func TestHungTurnIsSilentOnlyAfterStallAfterWithoutALog(t *testing.T) {
	h, m := time.Hour, time.Minute
	var steps []step
	for _, span := range [][2]time.Duration{{0, 20 * m}, {2 * h, 6 * h}} {
		steps = append(steps, journey(span[0], span[1], 5*m, between(), func(at time.Duration) decide.Observation {
			return silentTurn("turn-example-1", min(at, h+45*m))
		})...)
	}
	branch, _ := compare(t, steps, func(l *config.Loop) { l.Daemon.Interval = config.Duration{Duration: 5 * time.Minute} })
	for i, r := range branch.results {
		if r.Reason == decide.HungTurnNudgeReason && steps[i].at < 2*h+15*m {
			t.Fatalf("+%s: a nudge held back %s after the turn's last log", steps[i].at, steps[i].at-(h+45*m))
		}
	}
}
