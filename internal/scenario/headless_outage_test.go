package scenario

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// headlessSession is the session the stand-in headless CLIs report (see
// standin.HeadlessSession).
const headlessSession = "00000000-0000-4000-8000-0000000000aa"

// Expectations of the headless outage scenario, beyond the shared ones.
const (
	expectResumeRecovers = "after the hold, resume alone recovers the loop on the next check online"
	expectThreeTurns     = "the agent runs three turns in the whole journey: the first, the one the outage broke, and one re-prompt"
)

// headlessKnown lists the assertions that still fail. The connectivity check
// waits the outage and the portal out without a re-prompt, a hold or a
// report, so no loop is held and nothing here waits on it any more.
var headlessKnown = map[string][]Waiting{}

// TestScenarioHeadlessOutage is the owner's outage on a headless Claude Code
// or Codex loop: a turn breaks when the network drops, the host sleeps so
// checks run irregularly, the network returns behind a captive portal, and
// the portal clears. It replaces cmd/keepgoing's TestOfflineScenarioHeadless.
func TestScenarioHeadlessOutage(t *testing.T) {
	t.Parallel()
	for _, cli := range []string{"claude", "codex"} {
		for _, o := range outages {
			t.Run(cli+"/"+o.name, func(t *testing.T) {
				t.Parallel()
				runHeadlessOutage(t, cli, o, headlessKnown)
			})
		}
	}
}

func runHeadlessOutage(t *testing.T, cli string, o outage, known map[string][]Waiting) {
	w := NewWorld(t, scenarioStart)
	w.InitHeadless(cli, "await_user", "complete", "stop")
	expect := func(s *Step, name string, err error) {
		t.Helper()
		if waiting, ok := known[name]; ok {
			w.Known(s, waiting, name, err)
		} else {
			w.Check(s, name, err)
		}
	}
	w.Control("enable")

	// A healthy first turn starts a session.
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first turn", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: cli})))

	// The network drops. The idle session is woken at the next check, and
	// that turn breaks; it resumes the saved session.
	w.Net.Offline()
	w.Advance(10 * time.Minute)
	drop := w.Now
	s = w.Tick()
	w.Check(s, "the turn the outage breaks", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: cli, Mode: headlessSession})))

	var offline []*Step
	for _, at := range o.checks {
		w.At(drop.Add(at))
		offline = append(offline, w.Tick())
	}
	var sentOffline, heldOffline, reportedOffline []error
	for _, s := range offline {
		sentOffline = append(sentOffline, stepErr(s, nothingSent(s)))
		heldOffline = append(heldOffline, stepErr(s, holds(s)))
		reportedOffline = append(reportedOffline, stepErr(s, noReports(s)))
	}
	last := offline[len(offline)-1]
	expect(last, expectNoSendOffline, all(sentOffline...))
	expect(last, expectNoHoldOffline, all(heldOffline...))
	expect(last, expectNoReportOffline, all(reportedOffline...))

	w.At(drop.Add(o.length))
	w.Net.Portal()
	var portal []error
	for _, d := range []time.Duration{time.Minute, 2 * time.Minute} {
		w.Advance(d)
		s = w.Tick()
		portal = append(portal, stepErr(s, all(nothingSent(s), holds(s))))
	}
	expect(s, expectWaitsAtPortal, all(portal...))

	// The portal clears: one re-prompt into the saved session recovers.
	w.Advance(3 * time.Minute)
	w.Net.Online()
	w.Advance(time.Minute)
	s = w.Tick()
	expect(s, expectRecoveredOnce, all(action(s, "reprompt"), sentExactly(s, Send{Kind: "turn", Thread: cli, Mode: headlessSession})))
	expect(s, expectNotHeldOnline, holds(s))

	// The daemon keeps checking every five minutes until the loop re-prompts
	// or is held.
	for i := 0; i < 6 && s.action() != "reprompt" && !s.Status.Control.AwaitingUser; i++ {
		w.Advance(5 * time.Minute)
		s = w.Tick()
	}
	var intervened error
	if s.Status.Control.AwaitingUser {
		intervened = fmt.Errorf("held %s after the network returned; a person had to act", w.Now.Sub(drop.Add(o.length)))
		// A person resumes the loop.
		w.Advance(10 * time.Minute)
		w.Control("resume")
		w.Advance(time.Minute)
		s = w.Tick()
		expect(s, expectResumeRecovers, all(action(s, "reprompt"), sentExactly(s, Send{Kind: "turn", Thread: cli, Mode: headlessSession})))
		if s.action() != "reprompt" {
			// Today's way out: reselecting the target starts a new error
			// episode, then resume. The saved session is resumed.
			w.Advance(time.Minute)
			s = w.Control("retarget", "primary")
			w.Check(s, "retarget", exit(s, 0))
			s = w.Control("resume")
			w.Check(s, "resume", all(exit(s, 0), holds(s)))
			w.Advance(time.Minute)
			s = w.Tick()
		}
	}
	w.Check(s, "recovered by a re-prompt into the saved session", all(action(s, "reprompt"), sentExactly(s, Send{Kind: "turn", Thread: cli, Mode: headlessSession}), holds(s)))
	expect(s, expectNoPerson, intervened)

	// The recovered turn ends; the agent declares completion.
	w.Commit("feat: fictional work after the outage")
	w.Advance(10 * time.Minute)
	s = w.AgentRuns("complete", "--note", "Fictional task verified")
	w.Check(s, "complete", all(exit(s, 0), holds(s, "completed"), reported(s, "keepgoing.complete")))
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "completed", all(action(s, "none"), reasonIs(s, "completed"), nothingSent(s)))

	// Every turn after the first resumed exactly the saved session and
	// carried the task prompt.
	var turns []Send
	var reports []Report
	for _, st := range w.Journal {
		turns = append(turns, st.Sent...)
		reports = append(reports, st.Reports...)
	}
	for i, m := range turns {
		w.Check(s, fmt.Sprintf("turn %d", i+1), all(
			check(m.Kind == "turn" && m.Thread == cli, "sent %s to %s", m.Kind, m.Thread),
			check(i == 0 && m.Mode == "" || i > 0 && m.Mode == headlessSession, "resumed %q", m.Mode),
			check(strings.Contains(m.Message, scenarioTask), "the prompt does not carry the task"),
		))
	}
	expect(s, expectThreeTurns, check(len(turns) == 3, "the agent ran %d turns", len(turns)))
	expect(s, expectOnlyComplete, check(fmt.Sprint(reportTypes(reports)) == "[keepgoing.complete]", "reported %v", reportTypes(reports)))
}

// TestScenarioHeadlessFirstTurnFails: when the very first turn fails before
// the CLI reports a session, nothing is saved, so each re-prompt starts a new
// conversation with the whole task, until the re-prompts are spent and the
// loop is held. This is today's behaviour, listed as a gap in
// docs/adapters.md; it replaces cmd/keepgoing's
// TestOfflineFirstTurnStartsANewSessionOnEveryReprompt.
func TestScenarioHeadlessFirstTurnFails(t *testing.T) {
	t.Parallel()
	for _, cli := range []string{"claude", "codex"} {
		t.Run(cli, func(t *testing.T) {
			t.Parallel()
			w := NewWorld(t, scenarioStart)
			w.InitHeadless(cli, "await_user")
			w.HeadlessMode("silent")
			w.Control("enable")
			w.Advance(time.Minute)
			s := w.Tick()
			w.Check(s, "first turn", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: cli})))
			for i, after := range []time.Duration{time.Minute, 10 * time.Minute, 10 * time.Minute} {
				w.Advance(after)
				s = w.Tick()
				w.Check(s, fmt.Sprintf("re-prompt %d starts a new conversation", i+1), all(
					action(s, "reprompt"), sentExactly(s, Send{Kind: "turn", Thread: cli}), sentContains(s, scenarioTask), noReports(s),
				))
			}
			w.Advance(10 * time.Minute)
			s = w.Tick()
			w.Check(s, "held", all(action(s, "await_user"), nothingSent(s), holds(s, "awaitingUser"), reported(s, "keepgoing.await_user")))
		})
	}
}
