//go:build unix

package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

// A wrapper that takes the lock after Start gave up finds the turn abandoned
// and runs nothing, whether or not a new turn has started by then.
func TestALateWrapperRunsNothing(t *testing.T) {
	shortStartLimit(t)
	for _, again := range []bool{false, true} {
		d := Dir(t.TempDir())
		log := filepath.Join(t.TempDir(), "runs")
		cli := fakeCLI(t, "echo run >> '"+log+"'")
		if err := d.Start([]string{cli}, string(d), "fictional prompt", []string{"KEEPGOING_TEST_WRAPPER=late"}); err == nil {
			t.Fatal("Start returned before the wrapper held the lock")
		}
		want := ""
		if again {
			// The next turn starts before the late wrapper wakes.
			if err := d.Start([]string{cli}, string(d), "fictional prompt", nil); err != nil {
				t.Fatal(err)
			}
			waitPhase(t, d, Finished)
			want = "run\n"
		}
		time.Sleep(2500 * time.Millisecond) // the late wrapper has come and gone
		if got, _ := os.ReadFile(log); string(got) != want {
			t.Fatalf("again=%t: the agent ran %q, want %q", again, got, want)
		}
		st, err := d.Status()
		if err != nil || (again && st.Phase != Finished) || (!again && (st.Phase != Crashed || !st.Record.Abandoned)) {
			t.Fatalf("again=%t: %+v %v", again, st, err)
		}
	}
}

// The moment a turn's exit code can be seen the lock is free, while the
// wrapper process is still alive. The wrapper here stays alive for three
// seconds after publishing, so a lock released only by its exit is caught
// every time.
func TestTheLockIsFreeAsSoonAsTheExitCodeIsVisible(t *testing.T) {
	d := Dir(filepath.Join(t.TempDir(), "target"))
	if err := d.Start([]string{fakeCLI(t, "exit 0")}, filepath.Dir(string(d)), "fictional prompt", []string{"KEEPGOING_TEST_WRAPPER=linger"}); err != nil {
		t.Fatal(err)
	}
	st := waitPhase(t, d, Finished)
	held, err := lock.AcquireNamed(string(d), turnLock)
	if err != nil {
		t.Fatalf("the lock is still held after the exit code was published: %v", err)
	}
	held.Release()
	if !alive(st.Record.PID) {
		t.Fatal("the wrapper had already ended, so this proved nothing")
	}
	if err := os.RemoveAll(string(d)); err != nil {
		t.Fatal(err)
	}
}

// Start is called under the loop's tick lock and is not single-flight on its
// own. This records what two concurrent Starts do without it: the agents
// never overlap, and a Start that reports success had its turn run.
func TestConcurrentStartsNeverRunTwoAgentsAtOnce(t *testing.T) {
	for i := 0; i < 5; i++ {
		d := Dir(t.TempDir())
		log := filepath.Join(t.TempDir(), "runs")
		cli := fakeCLI(t, "echo start >> '"+log+"'; sleep 0.3; echo end >> '"+log+"'")
		errs := make(chan error, 2)
		for j := 0; j < 2; j++ {
			go func() { errs <- d.Start([]string{cli}, string(d), "fictional prompt", nil) }()
		}
		started := 0
		for j := 0; j < 2; j++ {
			if <-errs == nil {
				started++
			}
		}
		time.Sleep(time.Second)
		waitPhase(t, d, Finished)
		got, _ := os.ReadFile(log)
		lines := strings.Fields(string(got))
		for k, line := range lines {
			if want := []string{"start", "end"}[k%2]; line != want {
				t.Fatalf("round %d: two agents ran at once: %v", i, lines)
			}
		}
		if runs := len(lines) / 2; started < 1 || runs < started || runs > 2 || len(lines)%2 != 0 {
			t.Fatalf("round %d: %d starts reported success and the agent ran %d times: %v", i, started, runs, lines)
		}
	}
}
