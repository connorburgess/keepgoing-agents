package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// replacementInProgress is the rule of a record whose marker exists: a
// refusal or "not found" is retried.
func replacementInProgress(err error) bool {
	var code syscall.Errno
	return heldByAnother(err) || (errors.As(err, &code) && code == syscall.ERROR_FILE_NOT_FOUND)
}

// delaysUntilBudget are the waits retryReplacing makes before giving up:
// doubling from 2 ms, capped at maxRetryDelay, until retryBudget is slept.
func delaysUntilBudget() []time.Duration {
	var out []time.Duration
	d, slept := 2*time.Millisecond, time.Duration(0)
	for slept < retryBudget {
		out = append(out, d)
		slept += d
		d = min(d*2, maxRetryDelay)
	}
	return out
}

// The waits double from 2 ms and are capped at 25 ms, and stop once 250 ms
// has been slept: 13 waits, 14 tries.
func TestWindowsRetryWaitsAreCappedAndBounded(t *testing.T) {
	want := []time.Duration{2, 4, 8, 16, 25, 25, 25, 25, 25, 25, 25, 25, 25}
	for i := range want {
		want[i] *= time.Millisecond
	}
	var got []time.Duration
	err := retryReplacing(func() error { return syscall.ERROR_ACCESS_DENIED }, heldByAnother, func(d time.Duration) { got = append(got, d) })
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) || !slices.Equal(got, want) || !slices.Equal(delaysUntilBudget(), want) {
		t.Fatalf("waits %v, want %v (%v)", got, want, err)
	}
}

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
			retry := replacementInProgress // OpenRecord's rule
			handle, err := openWithRetry(func() (syscall.Handle, error) {
				attempts++
				if attempts <= len(tc.failures) {
					return syscall.InvalidHandle, tc.failures[attempts-1]
				}
				return syscall.Handle(42), nil
			}, retry, func(d time.Duration) { slept += d })
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
	if _, err := LstatRecord(filepath.Join(dir, "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if took := time.Since(start); took >= quick {
		t.Fatalf("a never-written record waited %s", took)
	}
	start = time.Now()
	if _, err := Lstat(filepath.Join(dir, "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing, plain: %v", err)
	}
	if took := time.Since(start); took >= retryBudget {
		t.Fatalf("plain Lstat waited %s for a missing file", took)
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

// Open's rule retries a file held by another program, and reports "not
// found" at once: only OpenRecord waits that out.
func TestWindowsPlainOpenDoesNotWaitOutAMissingFile(t *testing.T) {
	for name, tc := range map[string]struct {
		failures []syscall.Errno
		attempts int
		ok       bool
	}{
		"held_then_open":      {[]syscall.Errno{5, 32}, 3, true},
		"missing_not_retried": {[]syscall.Errno{2}, 1, false},
	} {
		attempts := 0
		_, err := openWithRetry(func() (syscall.Handle, error) {
			attempts++
			if attempts <= len(tc.failures) {
				return syscall.InvalidHandle, tc.failures[attempts-1]
			}
			return syscall.Handle(42), nil
		}, heldByAnother, func(time.Duration) {})
		if attempts != tc.attempts || (err == nil) != tc.ok {
			t.Errorf("%s: attempts=%d err=%v", name, attempts, err)
		}
	}
}
