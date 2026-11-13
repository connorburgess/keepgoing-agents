package rungraph

import (
	"errors"
	"testing"

	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
)

// Only a lane-retry intent that has no outcome can be discarded: the one case
// where the provider answered that it did nothing. Every other attempt, and
// one whose call has returned, stays in the graph.
func TestDiscardRemovesOnlyAnUnfinishedLaneRetryIntent(t *testing.T) {
	dir := t.TempDir()
	sync(t, dir, enabled, "start", 1)
	begin := func(n int, action, mode string) {
		t.Helper()
		update(t, dir, func(g *Graph) (bool, error) {
			return true, g.Begin(Attempt{ID: id("attempt_", n), Target: "primary", Action: action, Mode: mode, RecordedAt: at})
		})
	}
	begin(1, "wake", "auto")
	begin(2, "child_retry", "turn_retry")
	begin(3, "child_retry", "turn_retry")
	update(t, dir, func(g *Graph) (bool, error) { return g.Finish(id("attempt_", 3), rd.AdapterReturned, at) })
	for _, n := range []int{1, 3, 9} { // a wake, a returned lane retry, an unknown ID
		err := Update(dir, at, func(g *Graph) (bool, error) { return g.Discard(id("attempt_", n)) })
		if !errors.Is(err, rd.ErrInvalid) {
			t.Fatalf("attempt %d was discarded: %v", n, err)
		}
	}
	update(t, dir, func(g *Graph) (bool, error) { return g.Discard(id("attempt_", 2)) })
	g := load(t, dir)
	if len(g.Attempts) != 2 || g.Attempts[0].ID != id("attempt_", 1) || g.Attempts[1].ID != id("attempt_", 3) {
		t.Fatalf("%+v", g.Attempts)
	}
}
