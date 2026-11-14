package scenario

import (
	"fmt"
	"testing"
	"time"
)

// outage is one run of the owner's outage scenario.
type outage struct {
	name string
	// length is how long the network is down, from the drop mid-turn to its
	// return behind a captive portal.
	length time.Duration
	// checks are the moments, after the drop, at which the host was awake
	// and a check ran. The host sleeps the rest of the time.
	checks []time.Duration
	// laneRecovery turns bb.children.retry_errored on. It is off by default.
	laneRecovery bool
	// known lists the assertions that fail and what each one waits on. Every
	// other assertion is enforced.
	known map[string][]Waiting
}

// Expectations of the outage scenario. Each is an assertion name used below.
const (
	expectNoSendOffline   = "nothing is sent to the agent while the network is down"
	expectNoHoldOffline   = "no hold is raised while the network is down"
	expectNetworkShown    = "each check while the network is down says so"
	expectNoReportOffline = "nothing is reported while the network is down"
	expectRecoveredOnce   = "the first check online recovers the failed turn once, by retry, without a new prompt"
	expectWaitsAtPortal   = "behind a captive portal the loop waits: nothing sent, no hold"
	expectNotHeldOnline   = "the loop is not held when the network returns"
	expectNoPerson        = "the loop recovers without a person"
	expectNoNudge         = "a turn working again after the outage is not nudged for the time the network was down"
	expectChildRecovered  = "the child's failed turn is retried once when the network returns"
	expectTwoSends        = "the agent receives two things in the whole journey: the wake and one retry"
	expectOnlyComplete    = "the only report in the whole journey is the completion"
)

var outages = []outage{
	{
		name:   "20m",
		length: 20 * time.Minute,
		// One check just after the drop, then the host sleeps until the
		// network is back.
		checks: []time.Duration{3 * time.Minute},
	},
	{
		name:   "4h",
		length: 4 * time.Hour,
		checks: []time.Duration{3 * time.Minute, 50 * time.Minute, 51 * time.Minute, 2*time.Hour + 40*time.Minute, 3*time.Hour + 59*time.Minute},
	},
	{
		name:   "3d",
		length: 72 * time.Hour,
		checks: []time.Duration{3 * time.Minute, 9 * time.Hour, 9*time.Hour + time.Minute, 30 * time.Hour, 30*time.Hour + 2*time.Minute, 71 * time.Hour},
	},
	{
		name:   "3w",
		length: 21 * 24 * time.Hour,
		checks: []time.Duration{3 * time.Minute, 48 * time.Hour, 48*time.Hour + time.Minute, 9 * 24 * time.Hour, 15 * 24 * time.Hour, 15*24*time.Hour + 5*time.Minute, 20*24*time.Hour + 23*time.Hour},
	},
}

// With lane recovery on (bb.children.retry_errored) every assertion is
// enforced at every length. With it off, as it is by default, these still
// fail: the network check waits the outage out, through the captive portal,
// without a send, a hold or a report, and the loop recovers without a person
// by retrying the failed turn once, but the child's failed turn is not
// retried, so the child is still in error and the retry is followed by the
// count-only child-error notice: a third message.
var outageKnown = map[string][]Waiting{
	expectRecoveredOnce:  Waits(WaitChildRecovery),
	expectChildRecovered: Waits(WaitChildRecovery),
	expectTwoSends:       Waits(WaitChildRecovery),
}

// TestScenarioOutage is the owner's outage: the network drops in the middle
// of a turn while a child is working, BB retries and then gives up, the host
// sleeps so checks run irregularly or not at all, the network comes back
// behind a captive portal, the portal clears, and the work finishes.
func TestScenarioOutage(t *testing.T) {
	t.Parallel()
	for _, o := range outages {
		// Opted in to lane recovery: everything is enforced.
		on := o
		on.laneRecovery = true
		t.Run(o.name, func(t *testing.T) {
			t.Parallel()
			runOutage(t, on)
		})
		// The default: no lane is ever retried.
		off := o
		off.known = outageKnown
		t.Run(o.name+"/lane-recovery-off", func(t *testing.T) {
			t.Parallel()
			runOutage(t, off)
		})
	}
}

func runOutage(t *testing.T, o outage) {
	w := NewWorld(t, scenarioStart)
	w.Init("await_user", "complete", "stop")
	if o.laneRecovery {
		w.EditConfig(func(loop map[string]any) {
			bb, _ := loop["bb"].(map[string]any)
			bb["children"] = map[string]any{"retry_errored": true}
		})
	}
	primary := w.Thread(primaryThread)
	child := primary.Child(childThread)
	expect := func(s *Step, name string, err error) {
		t.Helper()
		if waiting, ok := o.known[name]; ok {
			w.Known(s, waiting, name, err)
		} else {
			w.Check(s, name, err)
		}
	}

	w.Control("enable")
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first wake", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"})))
	// The agent starts a child for part of the work.
	child.StartTurn()
	w.Advance(4 * time.Minute)
	s = w.Tick()
	w.Check(s, "working", all(action(s, "none"), nothingSent(s)))

	// The network drops mid-turn. The turn fails and BB says it will retry
	// it, so the check waits.
	w.Advance(2 * time.Minute)
	drop := w.Now
	w.Net.Offline()
	primary.NetworkError(true)
	child.NetworkError(true)
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "BB retrying", all(action(s, "none"), nothingSent(s), holds(s), noReports(s)))

	// BB gives up on both turns.
	w.Advance(time.Minute)
	primary.NetworkError(false)
	child.NetworkError(false)

	// The host is asleep except at these moments.
	var offline []*Step
	for _, at := range o.checks {
		w.At(drop.Add(at))
		offline = append(offline, w.Tick())
	}
	sentOffline, heldOffline, shownOffline, reportedOffline := []error{}, []error{}, []error{}, []error{}
	for _, s := range offline {
		sentOffline = append(sentOffline, stepErr(s, nothingSent(s)))
		heldOffline = append(heldOffline, stepErr(s, holds(s)))
		reportedOffline = append(reportedOffline, stepErr(s, noReports(s)))
		if s.Tick["network"] != "down" {
			shownOffline = append(shownOffline, fmt.Errorf("%s: tick line has network %v", s.At.Format(time.RFC3339), s.Tick["network"]))
		}
		// Never enforced as known: a child is never sent anything while the
		// network is down.
		w.Check(s, "child left alone while offline", nothingSentTo(s, childThread))
	}
	last := offline[len(offline)-1]
	expect(last, expectNoSendOffline, all(sentOffline...))
	expect(last, expectNoHoldOffline, all(heldOffline...))
	expect(last, expectNetworkShown, all(shownOffline...))
	expect(last, expectNoReportOffline, all(reportedOffline...))

	// The network returns behind a captive portal: the connectivity target
	// answers with a redirect to a sign-in page, and providers are still
	// unreachable. The loop should wait through it: nothing sent, no hold.
	w.At(drop.Add(o.length))
	w.Net.Portal()
	var portal []error
	for _, d := range []time.Duration{time.Minute, 2 * time.Minute} {
		w.Advance(d)
		s = w.Tick()
		portal = append(portal, stepErr(s, all(nothingSent(s), holds(s))))
	}
	expect(s, expectWaitsAtPortal, all(portal...))

	// The portal clears.
	w.Advance(3 * time.Minute)
	w.Net.Online()
	w.Advance(time.Minute)
	s = w.Tick()
	expect(s, expectRecoveredOnce, sentTo(s, primaryThread, Send{Kind: "retry", Thread: primaryThread}))
	expect(s, expectNotHeldOnline, holds(s))
	expect(s, expectChildRecovered, sentTo(s, childThread, Send{Kind: "retry", Thread: childThread}))
	w.Check(s, "child is never sent a message", noTellTo(s, childThread))
	if !o.laneRecovery {
		w.Check(s, "with lane recovery off the child is left alone", nothingSentTo(s, childThread))
	}

	// The daemon keeps checking every five minutes for half an hour. A
	// session that is still failed after that needs a person to resume it.
	for i := 0; i < 6 && primary.Status() == "error"; i++ {
		w.Advance(5 * time.Minute)
		s = w.Tick()
		w.Check(s, "child is never sent a message", noTellTo(s, childThread))
	}
	var intervened error
	if primary.Status() == "error" {
		intervened = fmt.Errorf("the session was still failed %s after the network returned; a person had to retry it in BB and resume the loop", w.Now.Sub(drop.Add(o.length)))
		// The person retries the turn in BB, outside keepgoing, and resumes
		// the loop.
		w.Advance(time.Minute)
		primary.StartTurn()
		s = w.Control("resume")
		w.Check(s, "resume", all(exit(s, 0), holds(s), nothingSent(s)))
	}
	expect(s, expectNoPerson, intervened)
	if primary.Status() != "active" {
		w.Failf(s, "the session did not recover: %s", primary.Status())
	}

	// The recovered turn is working: the next check leaves it alone.
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "working after recovery", all(holds(s), noTellTo(s, childThread)))
	expect(s, expectNoNudge, all(action(s, "none"), nothingSent(s)))

	// The work then finishes and the agent declares completion.
	w.Advance(10 * time.Minute)
	w.Commit("feat: fictional work after the outage")
	primary.FinishTurn()
	s = w.AgentRuns("complete", "--note", "Fictional task verified")
	w.Check(s, "complete", all(exit(s, 0), holds(s, "completed"), reported(s, "keepgoing.complete")))
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "completed", all(action(s, "none"), reasonIs(s, "completed"), nothingSent(s), noCalls(s)))

	var toPrimary []Send
	var reports []Report
	for _, st := range w.Journal {
		reports = append(reports, st.Reports...)
		for _, m := range st.Sent {
			if m.Thread == primaryThread {
				toPrimary = append(toPrimary, Send{Kind: m.Kind, Thread: m.Thread, Mode: m.Mode})
			}
		}
	}
	want := []Send{{Kind: "tell", Thread: primaryThread, Mode: "auto"}, {Kind: "retry", Thread: primaryThread}}
	var err error
	if fmt.Sprint(toPrimary) != fmt.Sprint(want) {
		err = fmt.Errorf("the agent received %s", describeSends(toPrimary))
	}
	expect(s, expectTwoSends, err)
	err = nil
	if got := reportTypes(reports); fmt.Sprint(got) != fmt.Sprint([]string{"keepgoing.complete"}) {
		err = fmt.Errorf("reported %v", got)
	}
	expect(s, expectOnlyComplete, err)
}

func stepErr(s *Step, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %v", s.At.Format(time.RFC3339), err)
}

// sentTo compares the sends to one thread in the step.
func sentTo(s *Step, thread string, want ...Send) error {
	only := &Step{}
	for _, m := range s.Sent {
		if m.Thread == thread {
			only.Sent = append(only.Sent, m)
		}
	}
	return sentExactly(only, want...)
}

func nothingSentTo(s *Step, thread string) error { return sentTo(s, thread) }

func noTellTo(s *Step, thread string) error {
	for _, m := range s.Sent {
		if m.Thread == thread && m.Kind == "tell" {
			return fmt.Errorf("told %s something", thread)
		}
	}
	return nil
}

// TestScenarioOutageHungTurn is the same outage with a turn that never
// fails: the host sleeps in the middle of a request, so BB still shows the
// turn as active when the network is gone. A session that looks stalled
// while offline is neither nudged nor held; the outage is shown once it has
// been noticed; and when the network is back the turn is not nudged for the
// time it could not work.
func TestScenarioOutageHungTurn(t *testing.T) {
	t.Parallel()
	for _, o := range outages {
		t.Run(o.name, func(t *testing.T) {
			t.Parallel()
			w := NewWorld(t, scenarioStart)
			w.Init("await_user", "complete", "stop")
			primary := w.Thread(primaryThread)

			w.Control("enable")
			w.Advance(time.Minute)
			s := w.Tick()
			w.Check(s, "first wake", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"})))
			w.Advance(4 * time.Minute)
			s = w.Tick()
			w.Check(s, "working", all(action(s, "none"), nothingSent(s)))

			// The network drops mid-turn and the turn hangs, still active.
			w.Advance(2 * time.Minute)
			drop := w.Now
			w.Net.Offline()
			noticed := false
			for _, at := range o.checks {
				w.At(drop.Add(at))
				s = w.Tick()
				w.Check(s, "offline with a hung turn: nothing sent, no hold, no report", all(action(s, "none"), nothingSent(s), holds(s), noReports(s)))
				if s.Tick["network"] == "down" {
					noticed = true
				} else if noticed {
					w.Failf(s, "the outage was noticed and then forgotten while it lasted: network %v", s.Tick["network"])
				}
			}
			if o.length >= 4*time.Hour && !noticed {
				w.Failf(s, "the outage was never shown in %s offline", o.length)
			}

			// Behind the captive portal the loop still waits.
			w.At(drop.Add(o.length))
			w.Net.Portal()
			for _, d := range []time.Duration{time.Minute, 2 * time.Minute} {
				w.Advance(d)
				s = w.Tick()
				w.Check(s, "behind the portal: nothing sent, no hold", all(action(s, "none"), nothingSent(s), holds(s)))
			}

			// The portal clears and the hung request goes through.
			w.Advance(3 * time.Minute)
			w.Net.Online()
			w.Advance(time.Minute)
			s = w.Tick()
			w.Check(s, "not nudged for the time the network was down", all(action(s, "none"), nothingSent(s), holds(s)))
			w.Advance(10 * time.Minute)
			s = w.Tick()
			w.Check(s, "still not nudged ten minutes on", all(action(s, "none"), nothingSent(s), holds(s)))

			w.Advance(5 * time.Minute)
			w.Commit("feat: fictional work after the outage")
			primary.FinishTurn()
			s = w.AgentRuns("complete", "--note", "Fictional task verified")
			w.Check(s, "complete", all(exit(s, 0), holds(s, "completed"), reported(s, "keepgoing.complete")))

			var toPrimary []Send
			var reports []Report
			for _, st := range w.Journal {
				reports = append(reports, st.Reports...)
				for _, m := range st.Sent {
					toPrimary = append(toPrimary, Send{Kind: m.Kind, Thread: m.Thread, Mode: m.Mode})
				}
			}
			var err error
			if fmt.Sprint(toPrimary) != fmt.Sprint([]Send{{Kind: "tell", Thread: primaryThread, Mode: "auto"}}) {
				err = fmt.Errorf("the agent received %s", describeSends(toPrimary))
			}
			w.Check(s, "the agent receives one thing in the whole journey: the wake", err)
			err = nil
			if got := reportTypes(reports); fmt.Sprint(got) != fmt.Sprint([]string{"keepgoing.complete"}) {
				err = fmt.Errorf("reported %v", got)
			}
			w.Check(s, "the only report in the whole journey is the completion", err)
		})
	}
}
