package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

func resetLifecycleFixture(t *testing.T, finished bool) *Headless {
	t.Helper()
	d := Dir(t.TempDir())
	h := &Headless{dir: d, spec: &Spec{MaxOutput: 1024, Read: func(Status) Reading { return Reading{Completed: true, Session: "sess_old"} }}}
	if finished {
		if err := d.saveRecord(Record{StopKey: "old-turn", Liveness: livenessLock, StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.path("exit"), []byte("0\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := d.SaveSession("sess_old"); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// Synthetic generation fixtures use the same turn lock and record writes as
// setup. They do not claim execution of a real wrapper/provider.
func TestSessionResetFencesSetupIncludingFirstStart(t *testing.T) {
	for _, finished := range []bool{false, true} {
		t.Run(map[bool]string{true: "completed", false: "never-run"}[finished], func(t *testing.T) {
			h := resetLifecycleFixture(t, finished)
			afterResetSample = func() {
				held, err := lock.AcquireNamed(string(h.dir), turnLock)
				if held != nil {
					held.Release()
				}
				if !errors.Is(err, lock.ErrHeld) {
					t.Errorf("setup acquired reset generation: %v", err)
				}
				if _, err := h.Observe(context.Background()); err != nil { // NeverRun may be a sampled idle, existing turn conservatively active.
					t.Errorf("guarded observation: %v", err)
				}
			}
			t.Cleanup(func() { afterResetSample = nil })
			if _, err := h.ResetSession(); err != nil {
				t.Fatal(err)
			}
			afterResetSample = nil
			held, err := lock.AcquireNamed(string(h.dir), turnLock)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			if _, err := h.dir.setAside(); err != nil {
				t.Fatal(err)
			}
			if err := h.dir.saveRecord(Record{StopKey: "new-turn", Liveness: livenessLock, StartedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			if err := h.dir.SaveSession("sess_new"); err != nil {
				t.Fatal(err)
			}
			if _, err := h.ResetSession(); !errors.Is(err, ErrTurnRunning) || h.dir.Session() != "sess_new" {
				t.Fatalf("running setup reset: %v %q", err, h.dir.Session())
			}
		})
	}
}

func TestSessionResetRechecksBeforeObserveRestore(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	beforeSessionCommit = func() {
		beforeSessionCommit = nil
		if _, err := h.ResetSession(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeSessionCommit = nil })
	obs, err := h.Observe(context.Background())
	if err != nil || obs.State != decide.Idle || h.dir.Session() != "" {
		t.Fatalf("restored reset session: %+v %v %q", obs, err, h.dir.Session())
	}
}

func TestSessionResetRejectsStaleObserveAndSend(t *testing.T) {
	for _, send := range []bool{false, true} {
		t.Run(map[bool]string{false: "observe", true: "send"}[send], func(t *testing.T) {
			h := resetLifecycleFixture(t, true)
			beforeSessionCommit = func() {
				held, err := lock.AcquireNamed(string(h.dir), turnLock)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Release()
				if _, err := h.dir.setAside(); err != nil {
					t.Fatal(err)
				}
				if err := h.dir.saveRecord(Record{StopKey: "new-turn", Liveness: livenessLock, StartedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				h.dir.SaveSession("sess_new")
			}
			t.Cleanup(func() { beforeSessionCommit = nil })
			var err error
			if send {
				err = h.Send(context.Background(), "fictional task", adapter.ModeAuto)
			} else {
				var obs decide.Observation
				obs, err = h.Observe(context.Background())
				if obs.State != decide.Unknown {
					t.Errorf("stale observation: %+v", obs)
				}
			}
			if !errors.Is(err, errSuperseded) || h.dir.Session() != "sess_new" {
				t.Fatalf("stale commit: %v saved %q", err, h.dir.Session())
			}
		})
	}
}

func TestSessionResetInvalidFencesNeverRestore(t *testing.T) {
	for _, bad := range []string{"", "x", "old-turn|2026-01-01", "old-turn|2026-01-01T00:00:00Z\ntrailing", "other-turn|2026-01-01T00:00:00Z\n", strings.Repeat("x", 8192)} {
		t.Run("bytes-"+strconv.Itoa(len(bad)), func(t *testing.T) {
			h := resetLifecycleFixture(t, true)
			if _, err := h.ResetSession(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(h.dir.path(sessionResetFile), []byte(bad), 0600); err != nil {
				t.Fatal(err)
			}
			obs, err := h.Observe(context.Background())
			if err == nil || obs.State != decide.Unknown || h.dir.Session() != "" {
				t.Fatalf("invalid fence restored: %+v %v %q", obs, err, h.dir.Session())
			}
			if err := h.Send(context.Background(), "fictional task", adapter.ModeAuto); err == nil {
				t.Fatal("sent past invalid fence")
			}
		})
	}
}

func TestSessionResetPublicationFailuresPreservePreviousFence(t *testing.T) {
	for _, failure := range []string{"short-write", "write-error", "close-error", "rename-error"} {
		t.Run(failure, func(t *testing.T) {
			h := resetLifecycleFixture(t, true)
			if _, err := h.ResetSession(); err != nil {
				t.Fatal(err)
			}
			h.dir.SaveSession("sess_retained")
			before, err := snapshot.ReadRecord(h.dir.path(sessionResetFile))
			if err != nil {
				t.Fatal(err)
			}
			write, close, replace := writeResetFence, closeResetFence, replaceResetFence
			t.Cleanup(func() { writeResetFence, closeResetFence, replaceResetFence = write, close, replace })
			switch failure {
			case "short-write":
				writeResetFence = func(f *os.File, b []byte) (int, error) { return f.Write(b[:1]) }
			case "write-error":
				writeResetFence = func(f *os.File, b []byte) (int, error) { n, _ := f.Write(b[:1]); return n, io.ErrUnexpectedEOF }
			case "close-error":
				closeResetFence = func(f *os.File) error { f.Close(); return os.ErrPermission }
			case "rename-error":
				replaceResetFence = func(string, string) error { return os.ErrPermission }
			}
			if _, err := h.ResetSession(); err == nil {
				t.Fatal("failed publication accepted")
			}
			after, err := snapshot.ReadRecord(h.dir.path(sessionResetFile))
			if err != nil || !bytes.Equal(before, after) || h.dir.Session() != "sess_retained" {
				t.Fatalf("publication changed old state: %q %v %q", after, err, h.dir.Session())
			}
			staged, _ := filepath.Glob(h.dir.path(".session-reset-*"))
			if len(staged) != 0 {
				t.Fatalf("staging leaked: %v", staged)
			}
		})
	}
}

func TestSessionResetRemovalFailureIsPartial(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	original := removeResetSession
	t.Cleanup(func() { removeResetSession = original })
	removeResetSession = func(string) error { return os.ErrPermission }
	if had, err := h.ResetSession(); !had || err == nil || !strings.Contains(err.Error(), "reset is incomplete") {
		t.Fatalf("partial reset not reported: %v %v", had, err)
	}
	if h.dir.Session() != "sess_old" {
		t.Fatal("session unexpectedly removed")
	}
	st, _, err := h.status()
	if err != nil {
		t.Fatal(err)
	}
	guard, err := lock.AcquireNamedShared(string(h.dir), turnLock)
	if err != nil {
		t.Fatal(err)
	}
	reset, fenceErr := h.dir.sessionWasReset(st.Record)
	guard.Release()
	if !reset || fenceErr != nil {
		t.Fatalf("fence not retained: %v %v", reset, fenceErr)
	}
	removeResetSession = original
	if _, err := h.ResetSession(); err != nil || h.dir.Session() != "" {
		t.Fatalf("explicit retry: %v", err)
	}
}

func TestSessionResetFenceAndCompanionRollback(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	if _, err := h.ResetSession(); err != nil {
		t.Fatal(err)
	}
	// The companion is real on Windows; synthetic elsewhere so both lifecycle
	// entries are exercised without pretending POSIX needs a .present record.
	marker := h.dir.path(sessionResetFile + ".present")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	held, err := lock.AcquireNamed(string(h.dir), turnLock)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	rename := renameFile
	t.Cleanup(func() { renameFile = rename })
	renameFile = func(from, to string) error {
		if from == h.dir.path("exit") {
			return os.ErrPermission
		}
		return os.Rename(from, to)
	}
	if _, err := h.dir.setAside(); err == nil {
		t.Fatal("setup failure accepted")
	}
	renameFile = rename
	st, err := h.dir.sampleStatus(1024, held)
	if err != nil {
		t.Fatal(err)
	}
	if reset, err := h.dir.sessionWasReset(st.Record); !reset || err != nil {
		t.Fatalf("rollback lost fence: %v %v", reset, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("rollback lost companion: %v", err)
	}
	moved, err := h.dir.setAside()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.dir.putBack(moved); err != nil {
		t.Fatal(err)
	}
	moved, err = h.dir.setAside()
	if err != nil {
		t.Fatal(err)
	}
	h.dir.discardAside()
	for _, name := range []string{sessionResetFile, sessionResetFile + ".present"} {
		for _, path := range []string{h.dir.path(name), h.dir.aside(name)} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fence survived successful rollover: %s %v", path, err)
			}
		}
	}
}

func TestSessionResetIncompleteRollbackRefusesRestoration(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	if _, err := h.ResetSession(); err != nil {
		t.Fatal(err)
	}
	held, err := lock.AcquireNamed(string(h.dir), turnLock)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := h.dir.setAside()
	if err != nil {
		held.Release()
		t.Fatal(err)
	}
	remove := removeResetFence
	t.Cleanup(func() { removeResetFence = remove })
	removeResetFence = func(string) error { return os.ErrPermission }
	if err := h.dir.putBack(moved); err == nil {
		held.Release()
		t.Fatal("rollback failure hidden")
	}
	held.Release()
	removeResetFence = remove
	// Model a late leftover exit too: the aside fence must still fence restores.
	if err := os.WriteFile(h.dir.path("exit"), []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	obs, err := h.Observe(context.Background())
	if err == nil || obs.State != decide.Unknown || h.dir.Session() != "" {
		t.Fatalf("partial rollback restored context: %+v %v %q", obs, err, h.dir.Session())
	}
	if err := h.Send(context.Background(), "fictional task", adapter.ModeAuto); err == nil {
		t.Fatal("sent through incomplete reset rollback")
	}
}

func TestSessionResetFenceMustBeRegular(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			h := resetLifecycleFixture(t, true)
			if _, err := h.ResetSession(); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(h.dir.path(sessionResetFile)); err != nil {
				t.Fatal(err)
			}
			if kind == "directory" {
				if err := os.Mkdir(h.dir.path(sessionResetFile), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Symlink(h.dir.path("stdout"), h.dir.path(sessionResetFile)); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			obs, err := h.Observe(context.Background())
			if err == nil || obs.State != decide.Unknown || h.dir.Session() != "" {
				t.Fatalf("nonregular fence restored: %+v %v %q", obs, err, h.dir.Session())
			}
		})
	}
}

func TestSessionResetProviderLookupRunsOutsideGuard(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	h.spec.Read = func(Status) Reading { return Reading{Class: decide.ErrUnknown, Session: "sess_old"} }
	called := false
	h.spec.Limits = &LimitLookup{Read: func(context.Context, string, []string) (Limits, error) {
		called = true
		held, err := lock.AcquireNamed(string(h.dir), turnLock)
		if err != nil {
			t.Errorf("provider lookup retained turn guard: %v", err)
		} else {
			held.Release()
		}
		return Limits{}, nil
	}}
	if _, err := h.Observe(context.Background()); err != nil || !called {
		t.Fatalf("lookup not exercised: %v %v", called, err)
	}
}
