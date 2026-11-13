package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func writeRecord(t *testing.T, rec Record) Dir {
	t.Helper()
	dir := Dir(t.TempDir())
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(string(dir), "run.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A turn is running when its wrapper holds the turn lock, never because a
// process with its saved PID exists. After a reboot that PID can belong to
// anything; here it belongs to this test.
func TestRunningMeansTheTurnLockIsHeldNotThatAPIDExists(t *testing.T) {
	old := time.Now().Add(-21 * 24 * time.Hour)
	phase := func(d Dir) Phase {
		t.Helper()
		st, err := d.Status()
		if err != nil {
			t.Fatal(err)
		}
		return st.Phase
	}
	// The wrapper is gone and an unrelated live process has its PID.
	gone := writeRecord(t, Record{PID: os.Getpid(), StartedAt: old, Argv0: "stand-in", Liveness: livenessLock})
	if got := phase(gone); got != Crashed {
		t.Fatalf("a three-week-old turn whose PID was reused reads as %s", got)
	}
	// Reading the status left no lock behind, and a second read agrees.
	if got := phase(gone); got != Crashed {
		t.Fatal(got)
	}
	// The wrapper is alive: it holds the lock, whatever its PID.
	live := writeRecord(t, Record{PID: 0, StartedAt: old, Argv0: "stand-in", Liveness: livenessLock})
	held, err := lock.AcquireNamed(string(live), turnLock)
	if err != nil {
		t.Fatal(err)
	}
	if got := phase(live); got != Running {
		t.Fatalf("a turn whose wrapper holds the lock reads as %s", got)
	}
	// A recorded exit code wins, even while the wrapper is still exiting.
	if err := os.WriteFile(filepath.Join(string(live), "exit"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := phase(live); got != Finished {
		t.Fatal(got)
	}
	if err := os.Remove(filepath.Join(string(live), "exit")); err != nil {
		t.Fatal(err)
	}
	// The wrapper ends without recording an exit code: the lock is released
	// with it, and the turn is crashed.
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if got := phase(live); got != Crashed {
		t.Fatal(got)
	}
	// A directory that never ran a turn is not touched by a status read.
	never := Dir(filepath.Join(t.TempDir(), "never-run"))
	if got := phase(never); got != NeverRun {
		t.Fatal(got)
	}
	if _, err := os.Stat(string(never)); !os.IsNotExist(err) {
		t.Fatal("a status read created the turn directory")
	}
}

// A record written by an older build has no lock behind it: its wrapper never
// took one. Only for those is the saved PID still consulted, so a turn that
// build started is not re-prompted while it runs. The old weakness remains
// for such a record until its turn ends.
func TestLegacyTurnRecordIsStillJudgedByItsPID(t *testing.T) {
	st, err := writeRecord(t, Record{PID: os.Getpid(), StartedAt: time.Now(), Argv0: "stand-in"}).Status()
	if err != nil || st.Phase != Running {
		t.Fatalf("%+v %v", st, err)
	}
	st, err = writeRecord(t, Record{PID: 0, StartedAt: time.Now(), Argv0: "stand-in"}).Status()
	if err != nil || st.Phase != Crashed {
		t.Fatalf("%+v %v", st, err)
	}
}
