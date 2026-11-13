package scenario

import (
	"testing"
	"time"
)

// A turn fails with the provider still announcing a retry. Twice over: once
// where nothing says the retry is dead, and once where BB's retry plugin has
// recorded that it will not retry the turn. The first loop waits out the
// notice; the second recovers on the very next check, by retrying the failed
// turn, without a second copy of the prompt.
func TestScenarioDeclinedRetryNotice(t *testing.T) {
	t.Parallel()
	for _, declined := range []bool{false, true} {
		name := "no decision recorded"
		if declined {
			name = "BB declined the retry"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := NewWorld(t, scenarioStart)
			w.Init("await_user", "complete", "stop")
			primary := w.Thread(primaryThread)
			w.Control("enable")
			w.Advance(time.Minute)
			s := w.Tick()
			w.Check(s, "first wake", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"})))

			// The turn fails while the provider says it will retry.
			w.Advance(4 * time.Minute)
			primary.NetworkError(true)
			if declined {
				primary.RetryDeclined()
			}
			w.Advance(time.Minute)
			s = w.Tick()
			if !declined {
				w.Check(s, "the notice is honoured", all(action(s, "none"), reasonIs(s, "provider is retrying the turn"), nothingSent(s), holds(s)))
				// Ten minutes later it is still fresh and still honoured.
				w.Advance(10 * time.Minute)
				s = w.Tick()
				w.Check(s, "still waiting inside the interval", all(action(s, "none"), reasonIs(s, "provider is retrying the turn"), nothingSent(s)))
				// Past the interval the age-based expiry recovers it.
				w.Advance(6 * time.Minute)
				s = w.Tick()
				w.Check(s, "recovered when the notice aged out", all(action(s, "reprompt"), sentExactly(s, Send{Kind: "retry", Thread: primaryThread}), holds(s)))
				return
			}
			w.Check(s, "recovered on the first check after the decline", all(action(s, "reprompt"), sentExactly(s, Send{Kind: "retry", Thread: primaryThread}), holds(s)))
			if got := s.Tick["retrySignal"]; got != "declined" {
				w.Failf(s, "tick line has retrySignal %v, want declined", got)
			}
			// The retried turn is running: the next check leaves it alone.
			w.Advance(time.Minute)
			s = w.Tick()
			w.Check(s, "working after recovery", all(action(s, "none"), nothingSent(s), holds(s)))
		})
	}
}
