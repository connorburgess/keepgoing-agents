package snapshot

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// A reader that arrives while the file is being replaced is refused briefly,
// or told the file is not there. Only those answers are retried, a bounded
// number of times; a missing directory is not.
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
		{"missing_then_open", []syscall.Errno{syscall.ERROR_FILE_NOT_FOUND, syscall.ERROR_FILE_NOT_FOUND}, 3, true},
		{"missing_until_limit", []syscall.Errno{2, 2, 2, 2, 2, 2}, openAttempts, false},
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

// Lstat asks again about a file reported missing, a bounded number of times,
// and returns the last answer; other errors and a missing directory at once.
func TestWindowsLstatRetriesAMissingRecord(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures []syscall.Errno
		attempts int
		ok       bool
	}{
		{"immediate", nil, 1, true},
		{"missing_then_found", []syscall.Errno{2, 2}, 3, true},
		{"missing_until_limit", []syscall.Errno{2, 2, 2, 2, 2, 2}, openAttempts, false},
		{"missing_directory_is_not_retried", []syscall.Errno{syscall.ERROR_PATH_NOT_FOUND}, 1, false},
		{"denied_is_not_retried", []syscall.Errno{5}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			var slept time.Duration
			_, err := lstatWithRetry(func() (os.FileInfo, error) {
				attempts++
				if attempts <= len(tc.failures) {
					return nil, &os.PathError{Op: "lstat", Path: "example", Err: tc.failures[attempts-1]}
				}
				return nil, nil
			}, func(d time.Duration) { slept += d })
			if attempts != tc.attempts || (err == nil) != tc.ok {
				t.Fatalf("attempts=%d err=%v", attempts, err)
			}
			if !tc.ok && tc.failures[0] == 2 && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a record missing to the end is not ErrNotExist: %v", err)
			}
			if slept > 40*time.Millisecond {
				t.Fatalf("waited %s", slept)
			}
		})
	}
}
