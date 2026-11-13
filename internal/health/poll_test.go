package health

import (
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot/snapshottest"
)

// status reads health.json while a tick saves probe evidence.
func TestHealthSnapshotSavesWhileReadersPoll(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	read := func() error { _, err := Load(dir); return err }
	snapshottest.ReadersPoll(t, 300, read, func(i int) error {
		r.Record("example-candidate", probe.Result{OK: i%2 == 0})
		return r.Save(dir)
	})
	if got, err := Load(dir); err != nil || got.Candidates["example-candidate"] == nil {
		t.Fatalf("final registry %+v %v", got, err)
	}
}
