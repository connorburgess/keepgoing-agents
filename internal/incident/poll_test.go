package incident

import (
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot/snapshottest"
)

// timeline and doctor read the journal while every tick appends to it.
func TestIncidentSnapshotSavesWhileReadersPoll(t *testing.T) {
	dir := t.TempDir()
	read := func() error { _, err := Load(dir); return err }
	snapshottest.ReadersPoll(t, 300, read, func(int) error { return Append(dir, sample()) })
	if j, err := Load(dir); err != nil || j.NextSequence != 301 {
		t.Fatalf("journal next=%d %v", j.NextSequence, err)
	}
}
