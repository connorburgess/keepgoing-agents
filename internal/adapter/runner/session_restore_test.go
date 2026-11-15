package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

// The old exclusive commit deterministically made the nested Observe Active.
func TestCompletedRestoreContentionObserve(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	var calls atomic.Int32
	replaceRestoredSession = func(source, target string) error {
		if calls.Add(1) == 1 {
			obs, err := h.Observe(context.Background())
			if err != nil || obs.State != decide.Idle {
				t.Errorf("completed contention: %+v %v", obs, err)
			}
			if _, err := h.ResetSession(); !errors.Is(err, ErrTurnRunning) {
				t.Errorf("reset crossed publication: %v", err)
			}
		}
		return snapshot.ReplaceAtomic(source, target)
	}
	t.Cleanup(func() { replaceRestoredSession = snapshot.ReplaceAtomic })
	if obs, err := h.Observe(context.Background()); err != nil || obs.State != decide.Idle {
		t.Fatalf("observe: %+v %v", obs, err)
	}
	if h.dir.Session() != "sess_old" {
		t.Fatal("session not restored")
	}
}

func TestCompletedRestoreConcurrentIdentical(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	ready, release := make(chan struct{}, 2), make(chan struct{})
	replaceRestoredSession = func(source, target string) error {
		ready <- struct{}{}
		<-release
		return snapshot.ReplaceAtomic(source, target)
	}
	t.Cleanup(func() { replaceRestoredSession = snapshot.ReplaceAtomic })
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			obs, err := h.Observe(context.Background())
			if err != nil || obs.State != decide.Idle {
				t.Errorf("observe: %+v %v", obs, err)
			}
		}()
	}
	<-ready
	<-ready
	if _, err := h.ResetSession(); !errors.Is(err, ErrTurnRunning) {
		t.Errorf("reset crossed restores: %v", err)
	}
	close(release)
	wg.Wait()
	if b, err := os.ReadFile(h.dir.path("session")); err != nil || string(b) != "sess_old\n" {
		t.Fatalf("session: %q %v", b, err)
	}
	if _, err := os.Stat(h.dir.path("session.present")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected marker: %v", err)
	}
}

func TestCompletedRestorePublicationFailurePreservesSession(t *testing.T) {
	for _, failure := range []string{"write", "short", "close", "rename"} {
		t.Run(failure, func(t *testing.T) {
			h := resetLifecycleFixture(t, true)
			if err := h.dir.SaveSession("sess_previous"); err != nil {
				t.Fatal(err)
			}
			oldWrite, oldClose, oldReplace := writeRestoredSession, closeRestoredSession, replaceRestoredSession
			defer func() {
				writeRestoredSession, closeRestoredSession, replaceRestoredSession = oldWrite, oldClose, oldReplace
			}()
			sentinel := errors.New("fictional publication failure")
			switch failure {
			case "write":
				writeRestoredSession = func(f *os.File, b []byte) (int, error) { return 0, sentinel }
			case "short":
				writeRestoredSession = func(f *os.File, b []byte) (int, error) { return f.Write(b[:1]) }
			case "close":
				closeRestoredSession = func(f *os.File) error { _ = f.Close(); return sentinel }
			case "rename":
				replaceRestoredSession = func(string, string) error { return sentinel }
			}
			obs, err := h.Observe(context.Background())
			if err == nil || obs.State != decide.Unknown || h.dir.Session() != "sess_previous" {
				t.Fatalf("failed publication: %+v %v %q", obs, err, h.dir.Session())
			}
			if failure == "short" && !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("short write: %v", err)
			}
			files, _ := os.ReadDir(string(h.dir))
			for _, f := range files {
				if strings.HasPrefix(f.Name(), ".session-restore-") {
					t.Errorf("staging leaked: %s", f.Name())
				}
			}
		})
	}
}

func TestCompletedRestoreEmptyAndResetNoOp(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "reset"}[reset], func(t *testing.T) {
			h := resetLifecycleFixture(t, true)
			if reset {
				if _, err := h.ResetSession(); err != nil {
					t.Fatal(err)
				}
			} else {
				h.spec.Read = func(Status) Reading { return Reading{Completed: true} }
			}
			old := replaceRestoredSession
			replaceRestoredSession = func(string, string) error { t.Error("unexpected publication"); return nil }
			defer func() { replaceRestoredSession = old }()
			if _, err := h.Observe(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := "sess_old"
			if reset {
				want = ""
			}
			if h.dir.Session() != want {
				t.Fatalf("session %q", h.dir.Session())
			}
		})
	}
}

// Rollover wins before shared sampling: the status reread must not restore the
// old completed generation. This synthetic fixture complements real sends.
func TestCompletedRestoreRereadsGeneration(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	afterStatusRecordRead = func() {
		held, err := lock.AcquireNamed(string(h.dir), turnLock)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release()
		if _, err := h.dir.setAside(); err != nil {
			t.Fatal(err)
		}
		if err := h.dir.saveRecord(Record{StopKey: "new-turn", Liveness: livenessLock}); err != nil {
			t.Fatal(err)
		}
		if err := h.dir.SaveSession("sess_new"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterStatusRecordRead = nil })
	obs, err := h.Observe(context.Background())
	if err != nil || obs.State != decide.Error || h.dir.Session() != "sess_new" {
		t.Fatalf("stale restore: %+v %v %q", obs, err, h.dir.Session())
	}
}
