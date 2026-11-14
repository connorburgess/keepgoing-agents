package config

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	snapfile "github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// A loop's check of its loaded sources reads the standard list while it may
// be replaced. On Windows that read must not block the rename, nor fail
// because a rename is in progress.
func TestRouteEditSourceCheckDoesNotBlockReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, RoutesFileName)
	a := []byte("version: 1\nroutes: []\n")
	b := []byte("version: 1\nroutes: [] # changed\n")
	if err := os.WriteFile(path, a, 0o600); err != nil {
		t.Fatal(err)
	}
	snap := snapshot(path, a)
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			snap.matches()
			snapshotFile(path)
			_, _ = ReadRoutesFile(path)
		}
	}()
	for i := range 40 {
		next := a
		if i%2 == 0 {
			next = b
		}
		tmp := filepath.Join(dir, ".next")
		if err := os.WriteFile(tmp, next, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := snapfile.Replace(tmp, path); err != nil {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("replace %d while read: %v", i, err)
		}
	}
	stop.Store(true)
	wg.Wait()
}
