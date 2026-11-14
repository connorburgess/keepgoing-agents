package runner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func publicationFixture(t *testing.T) (Dir, string) {
	t.Helper()
	d := writeRecord(t, Record{StopKey: "old-turn", Liveness: livenessLock})
	held, err := lock.AcquireNamed(string(d), turnLock)
	if err != nil {
		t.Fatal(err)
	}
	turnLockHeld, turnKey = held, "old-turn"
	t.Cleanup(func() {
		held.Release()
		turnLockHeld = nil
		turnKey = ""
		beforeExitRename = nil
		afterExitRename = nil
		afterTurnRelease = nil
		afterFinishingRead = nil
		drainStopWatcher = nil
	})
	tmp := filepath.Join(string(d), "exit.tmp-old-turn")
	if err := os.WriteFile(tmp, []byte("11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return d, tmp
}

func TestPublisherOwnsLockUntilExitAndCleanup(t *testing.T) {
	d, tmp := publicationFixture(t)
	for _, at := range []*func(){&beforeExitRename, &afterExitRename, &afterFinishingRead} {
		*at = func() {
			if held, err := lock.AcquireNamed(string(d), turnLock); !errors.Is(err, lock.ErrHeld) {
				if held != nil {
					held.Release()
				}
				t.Errorf("setup acquired turn lock inside publication: %v", err)
			}
			st, err := d.Status()
			if err != nil || st.Phase != Running {
				t.Errorf("in-progress publisher read as %+v %v", st, err)
			}
		}
	}
	if err := publishExit(string(d), tmp); err != nil {
		t.Fatal(err)
	}
	if st, err := d.Status(); err != nil || st.Phase != Finished || st.ExitCode != 11 {
		t.Fatalf("released publisher: %+v %v", st, err)
	}
}

func TestReleasedPublisherCannotContaminateRollover(t *testing.T) {
	d, tmp := publicationFixture(t)
	afterTurnRelease = func() {
		setup, err := lock.AcquireNamed(string(d), turnLock)
		if err != nil {
			t.Fatal(err)
		}
		defer setup.Release()
		if _, err := d.setAside(); err != nil {
			t.Fatal(err)
		}
		if err := d.saveRecord(Record{StopKey: "new-turn", Liveness: livenessLock}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.path("exit"), []byte("22\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.path(finishingName), []byte("new-turn"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := publishExit(string(d), tmp); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(d.path("exit")); err != nil || string(b) != "22\n" {
		t.Fatalf("old publisher overwrote new exit: %q %v", b, err)
	}
	if b, err := os.ReadFile(d.path(finishingName)); err != nil || string(b) != "new-turn" {
		t.Fatalf("old publisher removed new marker: %q %v", b, err)
	}
}

func TestFinishingCleanupCannotRaceNewSetup(t *testing.T) {
	d, tmp := publicationFixture(t)
	afterFinishingRead = func() {
		setup, err := lock.AcquireNamed(string(d), turnLock)
		if errors.Is(err, lock.ErrHeld) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer setup.Release()
		if err := d.saveRecord(Record{StopKey: "new-turn", Liveness: livenessLock}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.path(finishingName), []byte("new-turn"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := publishExit(string(d), tmp); err != nil {
		t.Fatal(err)
	}
	rec, err := d.record()
	if err != nil {
		t.Fatal(err)
	}
	if rec.StopKey == "new-turn" {
		if b, err := os.ReadFile(d.path(finishingName)); err != nil || string(b) != "new-turn" {
			t.Fatalf("old cleanup removed new marker: %q %v", b, err)
		}
	}
}

func TestStatusRereadsGenerationBeforeCompletion(t *testing.T) {
	d := writeRecord(t, Record{StopKey: "old-turn", Liveness: livenessLock})
	afterStatusRecordRead = func() {
		held, err := lock.AcquireNamed(string(d), turnLock)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release()
		if err := d.saveRecord(Record{StopKey: "new-turn", Liveness: livenessLock}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.path("exit"), []byte("22\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterStatusRecordRead = nil; beforeStatusFinished = nil })
	beforeStatusFinished = func() {
		held, err := lock.AcquireNamed(string(d), turnLock)
		if !errors.Is(err, lock.ErrHeld) {
			if held != nil {
				held.Release()
			}
			t.Errorf("rollover not fenced while reading completed result: %v", err)
		}
	}
	st, err := d.Status()
	if err != nil || st.Phase != Finished || st.Record.StopKey != "new-turn" || st.ExitCode != 22 {
		t.Fatalf("mixed generations: %+v %v", st, err)
	}
}

func TestStalePublisherIsRefusedAfterRollover(t *testing.T) {
	d, tmp := publicationFixture(t)
	turnLockHeld.Release()
	turnLockHeld = nil
	if err := d.saveRecord(Record{StopKey: "new-turn", Liveness: livenessLock}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(d.path("exit"), []byte("22\n"), 0600)
	os.WriteFile(d.path(finishingName), []byte("new-turn"), 0600)
	if err := publishExit(string(d), tmp); err == nil {
		t.Fatal("publisher without its lock accepted")
	}
	held, err := lock.AcquireNamed(string(d), turnLock)
	if err != nil {
		t.Fatal(err)
	}
	turnLockHeld = held // a late wrapper which reacquired a lock still has the old key
	if err := publishExit(string(d), tmp); err == nil {
		t.Fatal("superseded publisher accepted")
	}
	if b, err := os.ReadFile(d.path("exit")); err != nil || string(b) != "22\n" {
		t.Fatalf("new exit changed: %q %v", b, err)
	}
	if b, err := os.ReadFile(d.path(finishingName)); err != nil || string(b) != "new-turn" {
		t.Fatalf("new marker changed: %q %v", b, err)
	}
}

func TestFailedPublicationReleasesLockAndRetainsBoundedRecovery(t *testing.T) {
	d, tmp := publicationFixture(t)
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if err := publishExit(string(d), tmp); err == nil {
		t.Fatal("missing staged exit accepted")
	}
	held, err := lock.AcquireNamed(string(d), turnLock)
	if err != nil {
		t.Fatalf("failed publisher retained lock: %v", err)
	}
	held.Release()
	old := finishingWait
	finishingWait = 50 * time.Millisecond
	t.Cleanup(func() { finishingWait = old })
	began := time.Now()
	st, err := d.Status()
	if err != nil || st.Phase != Crashed || time.Since(began) > time.Second {
		t.Fatalf("failed publisher did not recover boundedly: %+v %v", st, err)
	}
}

func TestExitFromDeadPublisherBecomesFinishedAfterLockRelease(t *testing.T) {
	d, _ := publicationFixture(t)
	if err := os.WriteFile(d.path("exit"), []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if st, err := d.Status(); err != nil || st.Phase != Running {
		t.Fatalf("live publisher completed early: %+v %v", st, err)
	}
	// Releasing the owned handle models the kernel release when the wrapper
	// dies after rename. A result already published remains authoritative.
	turnLockHeld.Release()
	turnLockHeld = nil
	if st, err := d.Status(); err != nil || st.Phase != Finished {
		t.Fatalf("dead publisher's exit lost: %+v %v", st, err)
	}
}

func TestPublicationDrainsWatcherBeforeAnyCompletionBoundary(t *testing.T) {
	d, tmp := publicationFixture(t)
	drained := false
	drainStopWatcher = func() { drained = true }
	beforeExitRename = func() {
		if !drained {
			t.Error("exit published before watcher drain")
		}
	}
	afterTurnRelease = func() {
		if !drained {
			t.Error("lock released before watcher drain")
		}
	}
	afterPublish = func() {
		if !drained {
			t.Error("linger began before watcher drain")
		}
	}
	t.Cleanup(func() { afterPublish = nil })
	if err := publishExit(string(d), tmp); err != nil {
		t.Fatal(err)
	}
}

func TestStopWatcherCancellationJoinsWithoutTurnLockOrPublication(t *testing.T) {
	for _, mode := range []string{"normal completion", "early failure"} {
		t.Run(mode, func(t *testing.T) {
			d := Dir(t.TempDir())
			key, _ := randomStopID()
			held, err := lock.AcquireNamed(string(d), turnLock)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			drain := startStopWatcher(d, key, func() error { t.Error("idle watcher terminated a process"); return nil })
			done := make(chan struct{})
			go func() { drain(); drain(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("drain required turn.lock or publication")
			}
		})
	}
}

func TestStopWatcherDrainWaitsForAcknowledgementAndFailedTerminationCleanup(t *testing.T) {
	d := Dir(t.TempDir())
	key, _ := randomStopID()
	request, _ := randomStopID()
	m := stopMessage{Key: key, Request: request}
	if err := writeStopMessage(d.path("stop-request.json"), m); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	drain := startStopWatcher(d, key, func() error {
		if ack, err := readStopMessage(d.path("stop-ack.json")); err != nil || ack != m {
			t.Error("termination preceded acknowledgement")
		}
		close(entered)
		<-release
		return errors.New("injected termination failure")
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("watcher did not accept stop")
	}
	drained := make(chan struct{})
	go func() { drain(); close(drained) }()
	select {
	case <-drained:
		t.Error("drain returned with stop operation active")
	default:
	}
	close(release)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not join completed stop")
	}
	if _, err := os.Stat(d.path("stop-ack.json")); !os.IsNotExist(err) {
		t.Fatalf("failed stop acknowledgement remained after drain: %v", err)
	}
}

func TestEarlyTurnFailureDrainsWatcherBeforeReturn(t *testing.T) {
	d := Dir(t.TempDir()) // no prompt: fail before starting an agent
	key, _ := randomStopID()
	actual := startStopWatcher(d, key, func() error { t.Error("unexpected termination"); return nil })
	drained := false
	code := runOwnedTurn(string(d), []string{"fictional-never-started"}, "", func() { actual(); drained = true })
	if code != 2 || !drained {
		t.Fatalf("early failure returned without drain: code=%d drained=%t", code, drained)
	}
}

func TestCompletedStatusReadersDoNotInventRunningWork(t *testing.T) {
	d := writeRecord(t, Record{StopKey: "finished-turn", Liveness: livenessLock})
	if err := os.WriteFile(d.path("exit"), []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := false
	beforeStatusFinished = func() {
		if nested {
			return
		}
		nested = true
		st, err := d.Status()
		if err != nil || st.Phase != Finished {
			t.Errorf("another status reader invented work: %+v %v", st, err)
		}
	}
	t.Cleanup(func() { beforeStatusFinished = nil })
	if st, err := d.Status(); err != nil || st.Phase != Finished {
		t.Fatalf("completed status: %+v %v", st, err)
	}
}
