package snapshot

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// quick is well inside one retry budget: a read that did not wait.
const quick = retryBudget / 4

// A record that has never been written has no marker, so the Record reads
// believe it missing at once.
func TestWindowsRecordReadsBelieveANeverWrittenRecordAtOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sends.json")
	for name, read := range map[string]func() error{
		"OpenRecord":  func() error { _, err := OpenRecord(path); return err },
		"ReadRecord":  func() error { _, err := ReadRecord(path); return err },
		"LstatRecord": func() error { _, err := LstatRecord(path); return err },
	} {
		start := time.Now()
		if err := read(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: %v", name, err)
		}
		if took := time.Since(start); took >= quick {
			t.Errorf("%s waited %s for a record never written", name, took)
		}
	}
}

// Replace and ReplaceStrict create the marker before the record's first
// rename, and Remove deletes both, after which a read does not wait.
func TestWindowsMarkerIsCreatedFirstAndRemovedLast(t *testing.T) {
	for name, replace := range map[string]func(string, string) error{"Replace": Replace, "ReplaceStrict": ReplaceStrict} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "state.json")
			staged := filepath.Join(dir, ".tmp")
			if err := os.WriteFile(staged, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := replace(staged, target); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(markerFor(target)); err != nil || info.Size() != 0 {
				t.Fatalf("marker after the first save: %v %v", info, err)
			}
			if err := Remove(target); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{target, markerFor(target)} {
				if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s left after Remove: %v", filepath.Base(p), err)
				}
			}
			start := time.Now()
			if _, err := OpenRecord(target); !errors.Is(err, os.ErrNotExist) || time.Since(start) >= quick {
				t.Fatalf("read after Remove: %v after %s", err, time.Since(start))
			}
		})
	}
}

// While the marker says the record exists, "not found" is the moment of a
// replacement: the Record reads wait it out, here for the whole budget,
// since the record does not come back; the plain reads do not wait.
func TestWindowsRecordReadsWaitWhileTheMarkerExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.json")
	if err := os.WriteFile(markerFor(path), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := OpenRecord(path); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marked record must be unavailable, not missing: %v", err)
	}
	if took := time.Since(start); took < retryBudget {
		t.Fatalf("OpenRecord believed a record with a marker missing after %s", took)
	}
	start = time.Now()
	if _, err := Open(path); !errors.Is(err, os.ErrNotExist) || time.Since(start) >= quick {
		t.Fatalf("Open waited: %v after %s", err, time.Since(start))
	}
}

// The rule itself: a refusal is retried; "not found" only with a marker,
// which is looked at once; anything else, a missing directory included, is not.
func TestWindowsRecordRetryRule(t *testing.T) {
	dir := t.TempDir()
	without, with := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	if err := os.WriteFile(markerFor(with), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		path string
		err  error
		want bool
	}{
		"denied":                  {without, syscall.ERROR_ACCESS_DENIED, true},
		"sharing":                 {without, syscall.Errno(32), true},
		"missing, no marker":      {without, syscall.ERROR_FILE_NOT_FOUND, false},
		"missing, marker":         {with, syscall.ERROR_FILE_NOT_FOUND, true},
		"missing directory":       {with, syscall.ERROR_PATH_NOT_FOUND, false},
		"other error with marker": {with, syscall.Errno(87), false},
	} {
		if got := recordRetry(c.path)(c.err); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestWindowsMarkedRecordTimeoutNeverLooksLikeANewRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := ensureMarker(path); err != nil {
		t.Fatal(err)
	}
	for name, read := range map[string]func() error{
		"open":  func() error { _, err := OpenRecord(path); return err },
		"read":  func() error { _, err := ReadRecord(path); return err },
		"lstat": func() error { _, err := LstatRecord(path); return err },
	} {
		err := read()
		if err == nil || errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: must refuse instead of creating an empty record: %v", name, err)
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Errorf("%s: missing filesystem error type: %T", name, err)
		}
	}
}

func TestWindowsMarkerFailureRefusesPublication(t *testing.T) {
	for name, replace := range map[string]func(string, string) error{"Replace": Replace, "ReplaceStrict": ReplaceStrict} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path, staged := filepath.Join(dir, "state.json"), filepath.Join(dir, "staged")
			if err := os.Mkdir(markerFor(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(staged, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := replace(staged, path); err == nil {
				t.Fatal("published without a valid marker")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target created: %v", err)
			}
			if b, err := os.ReadFile(staged); err != nil || string(b) != "new" {
				t.Fatalf("staging changed: %s %v", b, err)
			}
		})
	}
}

func TestWindowsRefusedRemovalKeepsTheMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	// A nonempty directory at a record path cannot be removed by os.Remove.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureMarker(path); err != nil {
		t.Fatal(err)
	}
	if err := Remove(path); err == nil {
		t.Fatal("removed nonempty directory")
	}
	if _, err := os.Stat(markerFor(path)); err != nil {
		t.Fatalf("marker removed after refused deletion: %v", err)
	}
}

// Pause a real publication while its marker is held. A simultaneous delete
// must fail before touching the old record; it cannot strand the new record
// without a marker. Closing the writer's guard permits deliberate removal.
func TestWindowsDeletionCannotOvertakePublication(t *testing.T) {
	dir := t.TempDir()
	path, staged := filepath.Join(dir, "state.json"), filepath.Join(dir, "staged")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- replaceSnapshotWith(staged, path, func(h syscall.Handle, info *snapshotRenameInfo, size uint32) error {
			close(entered)
			<-release
			return setSnapshotRename(h, info, size)
		}, time.Sleep)
	}()
	<-entered
	err := Remove(path)
	b, readErr := os.ReadFile(path)
	close(release)
	saveErr := <-done
	if err == nil || readErr != nil || string(b) != "old" {
		t.Fatalf("deletion crossed publication: %v %q %v", err, b, readErr)
	}
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	if b, err := ReadRecord(path); err != nil || string(b) != "new" {
		t.Fatalf("published record: %q %v", b, err)
	}
	if _, err := os.Stat(markerFor(path)); err != nil {
		t.Fatalf("published without marker: %v", err)
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
}

// Interrupt publication after the marker exists, before rename. The old
// bytes survive, or a first publication is unavailable; neither is empty new
// state. This models the persistent files left by abrupt writer termination.
func TestWindowsInterruptedPublicationRetainsControlAndCaps(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprint("existing=", old), func(t *testing.T) {
			dir := t.TempDir()
			path, staged := filepath.Join(dir, "state.json"), filepath.Join(dir, "staged")
			preserved := []byte(`{"paused":true,"sends":7}`)
			if old {
				if err := os.WriteFile(path, preserved, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(staged, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			err := replaceSnapshotWith(staged, path, func(syscall.Handle, *snapshotRenameInfo, uint32) error {
				if _, err := os.Stat(markerFor(path)); err != nil {
					t.Errorf("publication began before marker: %v", err)
				}
				return syscall.Errno(87)
			}, func(time.Duration) {})
			if err == nil {
				t.Fatal("interrupted save succeeded")
			}
			b, err := ReadRecord(path)
			if old {
				if err != nil || !bytes.Equal(b, preserved) {
					t.Fatalf("old hold/caps lost: %s %v", b, err)
				}
			} else if err == nil || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete first publication read as empty new state: %s %v", b, err)
			}
		})
	}
}

func BenchmarkWindowsNeverWrittenRecordReads(b *testing.B) {
	path := filepath.Join(b.TempDir(), "sends.json")
	for name, read := range map[string]func() error{
		"OpenRecord":  func() error { _, err := OpenRecord(path); return err },
		"LstatRecord": func() error { _, err := LstatRecord(path); return err },
	} {
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if err := read(); !errors.Is(err, os.ErrNotExist) {
					b.Fatal(err)
				}
			}
		})
	}
}

// The helper terminates abruptly after the marker is held but before rename.
// Kernel handle release must leave either the prior record or an unavailable
// first publication, and must not leave a handle that blocks recovery.
func TestWindowsPublicationCrashHelper(t *testing.T) {
	dir := os.Getenv("KEEPGOING_MARKER_CRASH_FIXTURE")
	if dir == "" {
		return
	}
	_ = replaceSnapshotWith(filepath.Join(dir, "staged"), filepath.Join(dir, "state.json"),
		func(syscall.Handle, *snapshotRenameInfo, uint32) error { os.Exit(23); return nil }, time.Sleep)
	os.Exit(24)
}

func TestWindowsWriterCrashLeavesControlAndCapsRecoverable(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprint("existing=", old), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			preserved := []byte(`{"paused":true,"sends":7}`)
			if old {
				if err := os.WriteFile(path, preserved, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "staged"), []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsPublicationCrashHelper$")
			cmd.Env = append(os.Environ(), "KEEPGOING_MARKER_CRASH_FIXTURE="+dir)
			if out, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 23 {
				t.Fatalf("crash helper: %v %s", err, out)
			}
			if _, err := os.Stat(markerFor(path)); err != nil {
				t.Fatalf("crash lost marker: %v", err)
			}
			b, err := ReadRecord(path)
			if old {
				if err != nil || !bytes.Equal(b, preserved) {
					t.Fatalf("crash lost hold/caps: %s %v", b, err)
				}
			} else if err == nil || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("crash became empty new state: %s %v", b, err)
			}
			// A subsequent save proves the crashed process's guard was released.
			if err := ReplaceStrict(filepath.Join(dir, "staged"), path); err != nil {
				t.Fatal(err)
			}
			if b, err := ReadRecord(path); err != nil || string(b) != "new" {
				t.Fatalf("recovery: %s %v", b, err)
			}
		})
	}
}

func TestWindowsInitialAbsenceDoesNotBecomeAnUnwaitedFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	retry, marked := recordRetryState(path)
	if retry(syscall.ERROR_FILE_NOT_FOUND) {
		t.Fatal("never-written record was retried")
	}
	// The first publisher starts after the reader has observed true absence.
	if err := ensureMarker(path); err != nil {
		t.Fatal(err)
	}
	if err := recordReadError(path, syscall.ERROR_FILE_NOT_FOUND, *marked); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initial absence became unavailable without a retry: %v", err)
	}
}
