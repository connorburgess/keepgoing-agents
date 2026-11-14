package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A reader that arrives while the file is being replaced is refused briefly,
// or told the file is not there. Only those answers are retried, and only
// until retryBudget has been slept; a missing directory is not retried.
func TestWindowsControlSnapshotOpenRetriesOnlyDuringReplacement(t *testing.T) {
	many := func(code syscall.Errno) []syscall.Errno {
		out := make([]syscall.Errno, 100)
		for i := range out {
			out[i] = code
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		failures []syscall.Errno
		attempts int // 0: as many as the budget allows
		ok       bool
	}{
		{"immediate", nil, 1, true},
		{"denied_then_open", []syscall.Errno{syscall.ERROR_ACCESS_DENIED, syscall.ERROR_ACCESS_DENIED}, 3, true},
		{"sharing_then_open", []syscall.Errno{32}, 2, true},
		{"missing_then_open", []syscall.Errno{2, 2}, 3, true},
		{"missing_for_a_long_time_then_open", many(2)[:10], 11, true},
		{"denied_until_budget", many(5), 0, false},
		{"missing_until_budget", many(2), 0, false},
		{"missing_path_is_not_retried", []syscall.Errno{syscall.ERROR_PATH_NOT_FOUND}, 1, false},
		{"other_error_after_denial", []syscall.Errno{5, 87}, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			var slept time.Duration
			handle, err := openWithRetry(func() (syscall.Handle, error) {
				attempts++
				if attempts <= len(tc.failures) {
					return syscall.InvalidHandle, tc.failures[attempts-1]
				}
				return syscall.Handle(42), nil
			}, func(d time.Duration) { slept += d })
			if (err == nil) != tc.ok || (tc.attempts != 0 && attempts != tc.attempts) {
				t.Fatalf("attempts=%d err=%v", attempts, err)
			}
			if tc.ok && handle != syscall.Handle(42) {
				t.Fatalf("handle %v", handle)
			}
			if !tc.ok && !errors.Is(err, tc.failures[attempts-1]) {
				t.Fatalf("returned %v, not the last failure", err)
			}
			if tc.attempts == 0 && (slept < retryBudget || slept >= retryBudget+maxRetryDelay) {
				t.Fatalf("slept %s for a budget of %s", slept, retryBudget)
			}
			if slept >= retryBudget+maxRetryDelay {
				t.Fatalf("waited %s", slept)
			}
		})
	}
}

// Lstat describes a file, a directory and a missing path as os.Lstat does,
// from a handle it opened itself.
func TestWindowsLstatDescribesWhatIsThere(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "record.json")
	if err := os.WriteFile(file, []byte(`{"example":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := Lstat(file); err != nil || !info.Mode().IsRegular() || info.Size() != 16 || info.Name() != "record.json" {
		t.Fatalf("file: %v %v", info, err)
	}
	if info, err := Lstat(dir); err != nil || !info.IsDir() {
		t.Fatalf("directory: %v %v", info, err)
	}
	start := time.Now()
	if _, err := Lstat(filepath.Join(dir, "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if took := time.Since(start); took < retryBudget {
		t.Fatalf("a missing record was believed after %s, before the %s budget", took, retryBudget)
	}
	if _, err := Lstat(filepath.Join(dir, "no-such-dir", "record.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if info, err := Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link is followed or not described: %v %v", info, err)
	}
}
