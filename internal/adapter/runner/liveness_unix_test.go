//go:build unix

package runner

import (
	"encoding/json"
	"errors"
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
		declined := filepath.Join(t.TempDir(), "declined")
		if err := d.Start([]string{cli}, string(d), "fictional prompt", []string{"KEEPGOING_TEST_WRAPPER=late", "KEEPGOING_TEST_DECLINED=" + declined}); err == nil {
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
		// Wait until the late wrapper has taken the lock, read the record and
		// declined. Without that this would pass for a wrapper still asleep.
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(declined); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("again=%t: the late wrapper never reached its check", again)
			}
		}
		time.Sleep(200 * time.Millisecond) // long enough for an agent to have run
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

// When the previous turn's exit code cannot be removed, Start refuses. It
// used to go on: the new turn then read as finished, with the old code,
// before it had run. Here the directory is read-only and its files writable.
func TestStartRefusesWhenThePreviousExitCodeCannotBeRemoved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	d := Dir(filepath.Join(t.TempDir(), "target"))
	log := filepath.Join(t.TempDir(), "runs")
	cli := fakeCLI(t, "echo run >> '"+log+"'; echo first; exit 7")
	if err := d.Start([]string{cli}, filepath.Dir(string(d)), "first prompt", nil); err != nil {
		t.Fatal(err)
	}
	first := waitPhase(t, d, Finished)
	before := map[string]string{}
	for _, name := range []string{"run.json", "exit", "stdout", "prompt"} {
		b, err := os.ReadFile(d.path(name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = string(b)
	}
	if err := os.Chmod(string(d), 0o500); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			os.Chmod(string(d), 0o700)
		}
	}
	defer restore()
	err := d.Start([]string{cli}, filepath.Dir(string(d)), "second prompt", nil)
	var none notSpawned
	if !errors.As(err, &none) {
		t.Fatalf("Start with an exit code it could not remove returned %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	for name, want := range before {
		if got, _ := os.ReadFile(d.path(name)); string(got) != want {
			t.Errorf("%s changed to %q", name, got)
		}
	}
	if runs, _ := os.ReadFile(log); string(runs) != "run\n" {
		t.Fatalf("the agent ran %q; the refused start must not run it", runs)
	}
	if st, err := d.Status(); err != nil || st.Phase != Finished || st.ExitCode != 7 || st.Record.StopKey != first.Record.StopKey {
		t.Fatalf("the earlier turn reads as %+v %v", st, err)
	}
	// Once the directory can be written again a start runs normally.
	restore()
	if err := d.Start([]string{fakeCLI(t, "exit 0")}, filepath.Dir(string(d)), "third prompt", nil); err != nil {
		t.Fatal(err)
	}
	if st := waitPhase(t, d, Finished); st.ExitCode != 0 || st.Record.StopKey == first.Record.StopKey {
		t.Fatalf("%+v", st)
	}
}

// The same when a file other than the exit code is the one that cannot be
// moved: the exit code is put back, so the finished turn still reads as
// finished and the next start is not refused for ever.
func TestStartRefusedPartWayLeavesThePreviousTurnIntact(t *testing.T) {
	d := Dir(filepath.Join(t.TempDir(), "target"))
	if err := d.Start([]string{fakeCLI(t, "echo first; exit 7")}, filepath.Dir(string(d)), "first prompt", nil); err != nil {
		t.Fatal(err)
	}
	first := waitPhase(t, d, Finished)
	// A stop request too, so more than one file is moved before the failure.
	if err := os.WriteFile(d.path("stop-request.json"), []byte("earlier request"), 0o600); err != nil {
		t.Fatal(err)
	}
	listing := func() string {
		var b strings.Builder
		entries, _ := os.ReadDir(string(d))
		for _, e := range entries {
			if e.Name() == "turn.lock" {
				continue
			}
			body, _ := os.ReadFile(d.path(e.Name()))
			b.WriteString(e.Name() + "=" + string(body) + ";")
		}
		return b.String()
	}
	before := listing()
	for _, immovable := range []string{"stdout", "stop-request.json", "exit"} {
		renameFile = func(from, to string) error {
			if filepath.Base(from) == immovable {
				return syscall.EPERM
			}
			return os.Rename(from, to)
		}
		err := d.Start([]string{fakeCLI(t, "exit 0")}, filepath.Dir(string(d)), "second prompt", nil)
		renameFile = os.Rename
		var none notSpawned
		if !errors.As(err, &none) {
			t.Fatalf("%s immovable: Start returned %v", immovable, err)
		}
		if after := listing(); after != before {
			t.Fatalf("%s immovable: the previous turn's files changed\n before: %s\n after:  %s", immovable, before, after)
		}
		if st, err := d.Status(); err != nil || st.Phase != Finished || st.ExitCode != 7 || st.Record.StopKey != first.Record.StopKey {
			t.Fatalf("%s immovable: the earlier turn reads as %+v %v", immovable, st, err)
		}
	}
	// Nothing was left that stops a later start.
	if err := d.Start([]string{fakeCLI(t, "exit 0")}, filepath.Dir(string(d)), "third prompt", nil); err != nil {
		t.Fatal(err)
	}
	if st := waitPhase(t, d, Finished); st.ExitCode != 0 {
		t.Fatalf("%+v", st)
	}
	entries, _ := os.ReadDir(string(d))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".old") {
			t.Errorf("%s was left behind", e.Name())
		}
	}
}
