package snapshot

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// A record replaced over and over, while readers look it up by name and open
// it, is never reported missing: it exists the whole time. CI once saw
// state.json reported missing to an os.Lstat during its replacement, and the
// readers that take a missing record for a new one would then have read it
// as empty. Run natively on Windows; the name puts it in the CI step that
// verifies control snapshots.
func TestWindowsControlSnapshotNeverMissingWhileReplaced(t *testing.T) {
	for name, replace := range map[string]func(string, string) error{"Replace": Replace, "ReplaceStrict": ReplaceStrict} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "record.json")
			write := func(n int) {
				t.Helper()
				staged := filepath.Join(dir, ".staged-"+strconv.Itoa(n))
				if err := os.WriteFile(staged, []byte(strconv.Itoa(n)), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := replace(staged, path); err != nil {
					t.Errorf("replace %d: %v", n, err)
				}
			}
			write(1)
			var stop atomic.Bool
			var lookups atomic.Int64
			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func(open bool) {
					defer wg.Done()
					for !stop.Load() {
						if open {
							f, err := OpenRecord(path)
							if errors.Is(err, os.ErrNotExist) {
								t.Errorf("OpenRecord reported an existing record missing: %v", err)
								return
							}
							if err == nil {
								_, _ = io.ReadAll(f)
								f.Close()
							}
						} else if _, err := LstatRecord(path); errors.Is(err, os.ErrNotExist) {
							t.Errorf("LstatRecord reported an existing record missing: %v", err)
							return
						}
						lookups.Add(1)
					}
				}(i%2 == 0)
			}
			for n := 2; n <= 500 && !t.Failed(); n++ {
				write(n)
			}
			for lookups.Load() < 8 && !t.Failed() {
				runtime.Gosched() // until the readers have looked, not for a time
			}
			stop.Store(true)
			wg.Wait()
		})
	}
}
