package scenario

import (
	"testing"
	"time"
)

// TestScenarioLimitWhileChildrenWork: the supervised conversation starts a
// child for part of the work and then reaches its usage limit while the child
// is still working. Child work keeps an idle parent from being woken, but it
// does not hold back the recovery of a parent that is in error: the loop
// moves to its confirmed fallback at the next check. The child is never sent
// anything. The second journey is the same with a child listing that cannot
// be read.
func TestScenarioLimitWhileChildrenWork(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// held is the reason an idle parent is left alone.
		held string
		// brief is what the handoff brief says about the children left behind.
		brief string
		// scan sets what listing the parent's children returns.
		scan func(w *World, primary *Thread)
	}{
		{"child working", "child work running", "1 child conversation started by the previous conversation was still working", func(w *World, primary *Thread) {
			primary.Child(childThread).StartTurn()
		}},
		{"child listing unreadable", "child status unverified", "could not list them, so their number is unknown", func(w *World, primary *Thread) {
			w.writeJSON(primaryThread+".children", "not a thread list")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := NewWorld(t, scenarioStart)
			limitLoop(w, existingFallback)
			primary, alt := w.Thread(primaryThread), w.Thread(altThread)
			w.Control("enable")

			w.Advance(time.Minute)
			s := w.Tick()
			w.Check(s, "first wake", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"})))

			// The agent starts a child and ends its own turn to wait for it.
			tc.scan(w, primary)
			w.Advance(4 * time.Minute)
			primary.FinishTurn()
			w.Advance(time.Minute)
			s = w.Tick()
			w.Check(s, "an idle parent is not woken while its child works", all(
				action(s, "none"), reasonIs(s, tc.held), nothingSent(s), probedModels(s), noReports(s), targetIs(s, "primary", primaryThread),
			))
			// Still not woken long after the stall interval.
			w.Advance(2 * time.Hour)
			s = w.Tick()
			w.Check(s, "still not woken", all(action(s, "none"), reasonIs(s, tc.held), nothingSent(s), targetIs(s, "primary", primaryThread)))

			// The parent takes a turn and reaches a limit that resets in five
			// hours. Nothing about the child has changed.
			w.Advance(10 * time.Minute)
			primary.StartTurn()
			w.Advance(time.Minute)
			primary.RateLimited(w.Now.Add(5 * time.Hour))
			w.Advance(time.Minute)
			s = w.Tick()
			w.Check(s, "handoff while the child works", all(
				action(s, "handoff"), reasonIs(s, "error: rate_limit"), probedModels(s, altModel),
				sentExactly(s, Send{Kind: "tell", Thread: altThread, Mode: "auto"}),
				sentContains(s, "rate_limit"), sentContains(s, tc.brief), sentContains(s, "not supervised by this loop"), reported(s, "keepgoing.handoff"), holds(s),
				targetIs(s, altCandidate, altThread),
			))

			// The fallback works; the limited parent and its child are left alone.
			w.Advance(time.Hour)
			w.Commit("feat: fictional fallback step")
			s = w.Tick()
			w.Check(s, "fallback working", all(action(s, "none"), nothingSent(s), targetIs(s, altCandidate, altThread)))
			if alt.Status() != "active" {
				t.Fatalf("the fallback is %s, want active", alt.Status())
			}
			w.Check(s, "the child was never sent anything", journeySends(w, childThread))
			w.Check(s, "the parent received only the first wake", journeySends(w, primaryThread, "tell"))
			w.Check(s, "the fallback received only the brief", journeySends(w, altThread, "tell"))
		})
	}
}
