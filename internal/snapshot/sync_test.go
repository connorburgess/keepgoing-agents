package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

// Sync flushes unless a test binary asked it not to, and reports the
// flush's own error either way it is asked.
func TestSyncFlushesUnlessSkipped(t *testing.T) {
	t.Cleanup(func() { skipSync.Store(false) })
	f, err := os.Create(filepath.Join(t.TempDir(), "record"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Sync(f); err != nil {
		t.Fatalf("sync of an open file: %v", err)
	}
	f.Close()
	// A closed file cannot be flushed: the error shows the flush was tried.
	if err := Sync(f); err == nil {
		t.Fatal("Sync did not try to flush")
	}
	SkipSyncInTests()
	if err := Sync(f); err != nil {
		t.Fatalf("a skipped sync still flushed: %v", err)
	}
}
