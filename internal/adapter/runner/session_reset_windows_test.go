package runner

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

// This test is compiled locally and executed by the existing Windows native
// runner CI job. A source build or cross-compile is not Windows evidence.
func TestSessionResetWindowsSnapshotAndRemoval(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	if _, err := h.ResetSession(); err != nil {
		t.Fatal(err)
	}
	old, err := snapshot.ReadRecord(h.dir.path(sessionResetFile))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := snapshot.OpenRecord(h.dir.path(sessionResetFile))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := h.dir.saveRecord(Record{StopKey: "new-turn", Liveness: livenessLock, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	h.dir.SaveSession("sess_new")
	if _, err := h.ResetSession(); err != nil {
		t.Fatalf("strict replacement with delete-sharing reader: %v", err)
	}
	heldBytes := make([]byte, len(old))
	if _, err := reader.Read(heldBytes); err != nil || !bytes.Equal(heldBytes, old) {
		t.Fatalf("old snapshot changed: %q %v", heldBytes, err)
	}
	current, err := snapshot.OpenRecord(h.dir.path(sessionResetFile))
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if err := snapshot.Remove(h.dir.path(sessionResetFile)); err != nil {
		t.Fatalf("delete-sharing record removal: %v", err)
	}
	if _, err := os.Stat(h.dir.path(sessionResetFile + ".present")); !os.IsNotExist(err) {
		t.Fatalf("publication companion retained: %v", err)
	}
}

func holdResetFileWithoutDeleteSharing(t *testing.T, path string) func() {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(p, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return func() { syscall.CloseHandle(handle) }
}

func TestSessionResetWindowsSharingFailureRetainsFenceAndSession(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	if _, err := h.ResetSession(); err != nil {
		t.Fatal(err)
	}
	before, err := snapshot.ReadRecord(h.dir.path(sessionResetFile))
	if err != nil {
		t.Fatal(err)
	}
	h.dir.SaveSession("sess_retained")
	release := holdResetFileWithoutDeleteSharing(t, h.dir.path(sessionResetFile))
	defer release()
	if _, err := h.ResetSession(); err == nil {
		t.Fatal("legacy reader did not refuse strict replacement")
	}
	after, err := snapshot.ReadRecord(h.dir.path(sessionResetFile))
	if err != nil || !bytes.Equal(before, after) || h.dir.Session() != "sess_retained" {
		t.Fatalf("sharing failure changed state: %q %v %q", after, err, h.dir.Session())
	}
}

func TestSessionResetWindowsCompanionSharingRollsBack(t *testing.T) {
	h := resetLifecycleFixture(t, true)
	if _, err := h.ResetSession(); err != nil {
		t.Fatal(err)
	}
	before, err := snapshot.ReadRecord(h.dir.path(sessionResetFile))
	if err != nil {
		t.Fatal(err)
	}
	release := holdResetFileWithoutDeleteSharing(t, h.dir.path(sessionResetFile+".present"))
	held, err := lock.AcquireNamed(string(h.dir), turnLock)
	if err != nil {
		release()
		t.Fatal(err)
	}
	defer held.Release()
	if _, err := h.dir.setAside(); err == nil {
		release()
		t.Fatal("companion move under legacy handle accepted")
	}
	release()
	// A refused snapshot removal may prevent rollback while the companion is
	// held. That must remain explicitly unavailable, never absent/unfenced.
	b, readErr := snapshot.ReadRecord(h.dir.path(sessionResetFile))
	if readErr == nil && !bytes.Equal(before, b) {
		t.Fatal("rollback replaced the original fence")
	}
	st, err := h.dir.sampleStatus(1024, held)
	if err != nil {
		t.Fatal(err)
	}
	reset, fenceErr := h.dir.sessionWasReset(st.Record)
	if !reset && fenceErr == nil {
		t.Fatal("failed companion rollback was treated as no reset")
	}
}
