package main

import (
	"context"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
)

func TestClientHoldRecordsTheRegistryReferenceItUsed(t *testing.T) {
	dir, entry := clientFixture(t)
	if err := control.Save(entry.StateDir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, availability := rungraph.Load(entry.StateDir); availability != rungraph.Missing {
		t.Fatal("registration alone wrote a graph")
	}
	for i := 0; i < 2; i++ {
		if r, code := applyClientHold(context.Background(), dir, entry.ID, "pause"); code != 0 {
			t.Fatal(code, r)
		}
	}
	g, availability := rungraph.Load(entry.StateDir)
	if availability != rungraph.Available || len(g.Registrations) != 1 || g.Registrations[0].ID != entry.ID || g.Registrations[0].Source != rungraph.SourceRegistered {
		t.Fatalf("%s %#v", availability, g)
	}
	// A held legacy loop has a durable identity and a reference, but no run.
	n, status := g.Lookup(entry.ID)
	if status != rungraph.Found || len(g.Follow(n, rungraph.EdgeLoop)) != 1 || g.CurrentRun != "" || len(g.Runs) != 0 {
		t.Fatalf("%s %#v", status, g)
	}
}
