package runner

import (
	"io"
	"os"

	"github.com/looptender/looptender/internal/snapshot"
)

// Only completed Headless Observe uses this path. Its retained shared turn
// guard excludes setup, reset and the wrapper publisher throughout
// parsing (with the existing specification bounds) and publication. Concurrent restores for the same specification read
// the same protected record/output and derive identical session bytes. Custom
// session-file readers likewise depend on the completed agent's stable file;
// unrelated external writers are outside the cooperative runner contract.
// Empty sessions are no-ops (legacy interpretation); a reset fence bypasses
// restoration entirely. The shared guard does NOT serialize arbitrary writers.
// Do not generalize this exception to SaveSession or another runner.
var writeRestoredSession = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
var closeRestoredSession = func(f *os.File) error { return f.Close() }
var replaceRestoredSession = snapshot.ReplaceAtomic

func (d Dir) restoreCompletedSession(id string) error {
	if id == "" {
		return nil
	}
	f, err := os.CreateTemp(string(d), ".session-restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	b := []byte(id + "\n")
	n, writeErr := writeRestoredSession(f, b)
	closeErr := closeRestoredSession(f)
	_ = f.Close()
	if writeErr != nil {
		return writeErr
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	if closeErr != nil {
		return closeErr
	}
	// The previous session survives every failure. No marker/remove-first
	// fallback, and no promise of durability across power loss.
	return replaceRestoredSession(f.Name(), d.path("session"))
}
