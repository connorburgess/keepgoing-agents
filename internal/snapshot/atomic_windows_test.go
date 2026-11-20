package snapshot

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWindowsReplaceAtomicHeldReaderNoMarker(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "staging"), filepath.Join(dir, "session")
	for p, b := range map[string]string{source: "new", target: "old"} {
		if err := os.WriteFile(p, []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	oldInfo, err := reader.Stat()
	if err != nil {
		t.Fatal(err)
	}
	// File.Stat captures identity through the handle now. Path-based Stat can
	// defer that lookup until SameFile, when the staging name is already gone.
	staging, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	stageInfo, statErr := staging.Stat()
	closeErr := staging.Close()
	if statErr != nil {
		t.Fatal(statErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err := ReplaceAtomic(source, target); err != nil {
		t.Fatal(err)
	}
	published, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer published.Close()
	current, err := published.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(stageInfo, current) || os.SameFile(oldInfo, current) {
		t.Fatal("published identity is not closed staging")
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging name remains after publication: %v", err)
	}
	b, err := io.ReadAll(reader)
	if err != nil || string(b) != "old" {
		t.Fatalf("held reader changed: %q %v", b, err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "new" {
		t.Fatalf("target: %q %v", b, err)
	}
	if _, err := os.Stat(target + ".present"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker created: %v", err)
	}
}

// ReplaceAtomic delegates directly to this primitive. Inject only the native
// system call so refused/unsupported errors and its retry bound are observable.
func TestWindowsReplaceAtomicFailureAndStagingIdentity(t *testing.T) {
	for _, code := range []syscall.Errno{50, 87, syscall.ERROR_ACCESS_DENIED, 32} {
		t.Run(code.Error(), func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "staging"), filepath.Join(dir, "session")
			for p, b := range map[string]string{source: "new", target: "old"} {
				if err := os.WriteFile(p, []byte(b), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f, err := Open(source)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			var expected syscall.ByHandleFileInformation
			if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &expected); err != nil {
				t.Fatal(err)
			}
			calls := 0
			var slept time.Duration
			err = renameSnapshotWith(source, target, func(h syscall.Handle, _ *snapshotRenameInfo, _ uint32) error {
				calls++
				var actual syscall.ByHandleFileInformation
				if err := syscall.GetFileInformationByHandle(h, &actual); err != nil {
					t.Fatal(err)
				}
				if actual.VolumeSerialNumber != expected.VolumeSerialNumber || actual.FileIndexHigh != expected.FileIndexHigh || actual.FileIndexLow != expected.FileIndexLow {
					t.Fatal("rename authority is not staging identity")
				}
				return code
			}, func(d time.Duration) { slept += d })
			want := 1
			if code == syscall.ERROR_ACCESS_DENIED || code == 32 {
				want = len(delaysUntilBudget()) + 1
				if slept < retryBudget || slept >= retryBudget+maxRetryDelay {
					t.Fatalf("unbounded retry: %s", slept)
				}
			}
			if !errors.Is(err, code) || calls != want {
				t.Fatalf("error/calls: %v %d", err, calls)
			}
			for p, want := range map[string]string{source: "new", target: "old"} {
				if b, err := os.ReadFile(p); err != nil || string(b) != want {
					t.Fatalf("changed file: %q %v", b, err)
				}
			}
			if _, err := os.Stat(target + ".present"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("marker created: %v", err)
			}
		})
	}
}
