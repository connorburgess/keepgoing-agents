package report

import (
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot/snapshottest"
)

// status inspects delivery history while a report records its attempts.
func TestDeliverySnapshotSavesWhileReadersPoll(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	d := Delivery{Event: "await_user", StartedAt: now, UpdatedAt: now, Outcome: "prepared", Receipt: "unverified", Attempts: []Attempt{}}
	read := func() error { _, err := LoadDeliveries(dir); return err }
	snapshottest.ReadersPoll(t, 300, read, func(int) error { _, err := saveDelivery(dir, d); return err })
	if h, err := LoadDeliveries(dir); err != nil || h.NextSequence != 301 {
		t.Fatalf("history next=%d %v", h.NextSequence, err)
	}
}
