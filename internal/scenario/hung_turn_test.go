package scenario

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// TestScenarioHungTurnAfterAShortOutageAndASleep is the owner's 20-minute
// journey with a turn that never finishes. Checks run every five minutes
// (the default daemon interval). The network drops mid-turn; one check runs
// three minutes later, before the turn has been silent for stall_after, so
// nothing checks connectivity; then the host sleeps until the network is
// back. No connectivity check ever fails: the gap is the only evidence, and
// the missed-time rule judges it missed. The turn is waited on, not nudged,
// is reported once, and the loop asks a person when the stall rule would
// have held it, with a reason that says what happened.
func TestScenarioHungTurnAfterAShortOutageAndASleep(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init("await_user", "hung_turn", "complete", "stop")
	w.EditConfig(func(loop map[string]any) {
		loop["daemon"] = map[string]any{"interval": "5m"}
	})
	w.Thread(primaryThread)

	w.Control("enable")
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first wake", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"})))
	for i := 0; i < 4; i++ {
		w.Advance(5 * time.Minute)
		s = w.Tick()
		w.Check(s, "working", all(action(s, "none"), nothingSent(s)))
	}

	// The network drops; one check, then the host sleeps for the rest of
	// the outage.
	w.Advance(2 * time.Minute)
	drop := w.Now
	w.Net.Offline()
	w.At(drop.Add(3 * time.Minute))
	s = w.Tick()
	w.Check(s, "just after the drop: nothing sent, no hold", all(action(s, "none"), nothingSent(s), holds(s), noReports(s)))
	w.At(drop.Add(20 * time.Minute))
	w.Net.Online()

	// From the first check after the sleep, every five minutes, for as
	// long as the stall rule would have taken to hold.
	var hungReports, holdAt int
	var sent []string
	for i := 1; i <= 60 && holdAt == 0; i++ {
		w.At(drop.Add(20*time.Minute + time.Duration(i)*5*time.Minute))
		s = w.Tick()
		for _, m := range s.Sent {
			sent = append(sent, fmt.Sprintf("+%dm %s %s", i*5, m.Kind, m.Mode))
		}
		for _, r := range s.Reports {
			if r.Type == "keepgoing.hung_turn" {
				hungReports++
			}
		}
		if s.action() == "await_user" {
			holdAt = i * 5
			w.Check(s, "held with the hung-turn reason", all(holds(s, "awaitingUser"), reported(s, "keepgoing.await_user")))
			if !strings.Contains(s.reason(), decide.HungTurnHoldReason) {
				w.Failf(s, "held with %q", s.reason())
			}
		} else if s.reason() != "active" && s.reason() != decide.HungTurnReason && s.reason() != decide.HungTurnNudgeReason {
			w.Failf(s, "after the sleep: %s (%s)", s.action(), s.reason())
		}
	}
	var err error
	if len(sent) != 0 {
		err = fmt.Errorf("sent into the hung turn: %v", sent)
	}
	w.Check(s, "nothing is sent into the hung turn", err)
	err = nil
	if hungReports != 1 {
		err = fmt.Errorf("%d hung_turn reports", hungReports)
	}
	w.Check(s, "the wait is reported once", err)
	err = nil
	// Without the wait the stall rule nudges at +20m, +80m and +140m and
	// holds at +200m. The wait asks a person when the stall clock reaches
	// stall_after plus three min_wake_intervals: never later than that, and
	// not before the stall rule's last nudge would have gone.
	if holdAt == 0 || holdAt > 200 || holdAt <= 140 {
		err = fmt.Errorf("held at +%dm, want after +140m and no later than +200m", holdAt)
	}
	w.Check(s, "a person is asked when the stall rule would have held", err)
}

// TestScenarioHungTurnAfterALidClose is the usual lid-close: checks every
// five minutes, the turn logs once more two minutes after the last of them,
// and the computer sleeps a minute later until the network and the computer
// are back. No connectivity check ever fails: only the gap shows what
// happened, and the turn's last log came after the last check before it. It
// is waited on, never nudged, reported once, and held no later than the
// stall rule alone would have held it (lidCloseStallRuleHold, measured with
// the watch switched off).
func TestScenarioHungTurnAfterALidClose(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init("await_user", "hung_turn", "complete", "stop")
	w.EditConfig(func(loop map[string]any) {
		loop["daemon"] = map[string]any{"interval": "5m"}
	})
	primary := w.Thread(primaryThread)
	start := w.Now

	w.Control("enable")
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first wake", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"})))
	for i := 0; i < 4; i++ {
		w.Advance(5 * time.Minute)
		s = w.Tick()
		w.Check(s, "working", all(action(s, "none"), nothingSent(s)))
	}
	// One more log, then the lid closes.
	w.At(start.Add(23 * time.Minute))
	primary.Event("item/completed", map[string]any{"item": map[string]any{"id": "item_example1", "type": "agentMessage"}})

	var hungReports int
	var holdAt time.Duration
	var sent []string
	for at := 2 * time.Hour; at <= 10*time.Hour && holdAt == 0; at += 5 * time.Minute {
		w.At(start.Add(at))
		s = w.Tick()
		for _, m := range s.Sent {
			sent = append(sent, fmt.Sprintf("+%s %s %s", at, m.Kind, m.Mode))
		}
		for _, r := range s.Reports {
			if r.Type == "keepgoing.hung_turn" {
				hungReports++
			}
		}
		if s.action() == "await_user" {
			holdAt = at
		}
	}
	var err error
	if len(sent) != 0 {
		err = fmt.Errorf("sent into the hung turn: %v", sent)
	}
	w.Check(s, "nothing is sent into the hung turn", err)
	err = nil
	if hungReports != 1 {
		err = fmt.Errorf("%d hung_turn reports", hungReports)
	}
	w.Check(s, "the wait is reported once", err)
	err = nil
	if holdAt == 0 || holdAt > lidCloseStallRuleHold {
		err = fmt.Errorf("held at +%s, the stall rule alone at +%s", holdAt, lidCloseStallRuleHold)
	} else if !strings.Contains(s.reason(), decide.HungTurnHoldReason) {
		err = fmt.Errorf("held with %q", s.reason())
	}
	w.Check(s, "held with the hung-turn reason, no later than the stall rule", err)
}

// lidCloseStallRuleHold is when the stall rule alone holds that journey,
// after three nudges at +2h5m, +3h5m and +4h5m: measured with the watch
// switched off.
const lidCloseStallRuleHold = 5*time.Hour + 5*time.Minute
