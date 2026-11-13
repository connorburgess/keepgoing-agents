//go:build unix

package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// Start returns only once the wrapper holds its lock or has finished, so the
// very next status read is never "crashed", for a turn that ends at once and
// for one that keeps running.
func TestStartHandsOverToALiveWrapper(t *testing.T) {
	for name, body := range map[string]string{"ends at once": "exit 0", "keeps running": "sleep 30"} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 5; i++ {
				d := Dir(t.TempDir())
				if err := d.Start([]string{fakeCLI(t, body)}, string(d), "fictional prompt", nil); err != nil {
					t.Fatal(err)
				}
				st, err := d.Status()
				if err != nil || st.Phase == Crashed || st.Phase == NeverRun || st.Record.Liveness != livenessLock {
					t.Fatalf("right after Start: %+v %v", st, err)
				}
				if name == "ends at once" {
					waitPhase(t, d, Finished)
					continue
				}
				if st.Phase != Running {
					t.Fatalf("a turn that keeps running reads as %s right after Start", st.Phase)
				}
				// Stopping it ends the wrapper, and its lock goes with it.
				if err := d.Stop(); err != nil {
					t.Fatal(err)
				}
				waitPhase(t, d, Crashed)
			}
		})
	}
}

// The reboot case end to end: a real wrapper is killed without a chance to
// record its exit code, and its saved PID is then pointed at a live unrelated
// process. The turn reads as crashed, not running.
func TestKilledWrapperIsCrashedEvenWhenItsPIDIsReused(t *testing.T) {
	d := Dir(t.TempDir())
	if err := d.Start([]string{fakeCLI(t, "sleep 30")}, string(d), "fictional prompt", nil); err != nil {
		t.Fatal(err)
	}
	st := waitPhase(t, d, Running)
	// The wrapper leads its own process group; kill the whole group.
	if err := syscall.Kill(-st.Record.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, d, Crashed)
	rec := st.Record
	rec.PID = os.Getpid()
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(string(d), "run.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Status(); err != nil || got.Phase != Crashed {
		t.Fatalf("%+v %v", got, err)
	}
}

// A new turn started the moment the previous one recorded its exit code must
// not read as crashed while the old wrapper is still letting go of the lock.
// A check that saw "crashed" there would re-prompt and start a second turn.
func TestBackToBackTurnsNeverReadAsCrashed(t *testing.T) {
	d := Dir(t.TempDir())
	cli := fakeCLI(t, "exit 0")
	for i := 0; i < 40; i++ {
		if err := d.Start([]string{cli}, string(d), "fictional prompt", nil); err != nil {
			t.Fatal(err)
		}
		// Poll hard until this turn's exit code appears: every read in
		// between must be running, never crashed.
		for {
			st, err := d.Status()
			if err != nil || st.Phase == Crashed || st.Phase == NeverRun {
				t.Fatalf("turn %d: %+v %v", i, st, err)
			}
			if st.Phase == Finished {
				break
			}
		}
	}
}

// Once a turn's exit code can be seen, the wrapper has let go of the lock, so
// the turn's directory can be removed at once. On Windows a held lock file
// would block that removal, which an existing Cursor fixture depends on.
func TestFinishedTurnHoldsNothingInItsDirectory(t *testing.T) {
	cli := fakeCLI(t, "exit 0")
	for i := 0; i < 20; i++ {
		d := Dir(filepath.Join(t.TempDir(), "target"))
		if err := d.Start([]string{cli}, filepath.Dir(string(d)), "fictional prompt", nil); err != nil {
			t.Fatal(err)
		}
		waitPhase(t, d, Finished)
		held, err := lock.AcquireNamed(string(d), turnLock)
		if err != nil {
			t.Fatalf("turn %d: the lock is still held after the exit code was published: %v", i, err)
		}
		held.Release()
		if err := os.RemoveAll(string(d)); err != nil {
			t.Fatal(err)
		}
	}
}
