package snapshot

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

// A reader that arrives while the file is being replaced is refused briefly.
// Only that refusal is retried, a bounded number of times.
func TestWindowsControlSnapshotOpenRetriesOnlyDuringReplacement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures []syscall.Errno
		attempts int
		ok       bool
	}{
		{"immediate", nil, 1, true},
		{"denied_then_open", []syscall.Errno{syscall.ERROR_ACCESS_DENIED, syscall.ERROR_ACCESS_DENIED}, 3, true},
		{"sharing_then_open", []syscall.Errno{32}, 2, true},
		{"denied_until_limit", []syscall.Errno{5, 5, 5, 5, 5, 5, 5}, openAttempts, false},
		{"missing_is_not_retried", []syscall.Errno{syscall.ERROR_FILE_NOT_FOUND}, 1, false},
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
			if attempts != tc.attempts || (err == nil) != tc.ok {
				t.Fatalf("attempts=%d err=%v", attempts, err)
			}
			if tc.ok && handle != syscall.Handle(42) {
				t.Fatalf("handle %v", handle)
			}
			if !tc.ok && !errors.Is(err, tc.failures[tc.attempts-1]) {
				t.Fatalf("returned %v, not the last failure", err)
			}
			if slept > 40*time.Millisecond {
				t.Fatalf("waited %s", slept)
			}
		})
	}
}
