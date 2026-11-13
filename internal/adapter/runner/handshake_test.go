package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// shortStartLimit makes both of Start's waits short for one test.
func shortStartLimit(t *testing.T) {
	t.Helper()
	previous := wrapperStartTimeout
	wrapperStartTimeout = 300 * time.Millisecond
	t.Cleanup(func() { wrapperStartTimeout = previous })
}

func ranAgent(cli string) bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(cli), "prompt.txt"))
	return err == nil
}

// While a turn's wrapper holds the lock, Start waits for its limit, refuses,
// and changes nothing: the running turn's files are as they were and no
// process was started.
func TestStartRefusesWhileATurnHoldsTheLockAndChangesNothing(t *testing.T) {
	shortStartLimit(t)
	d := writeRecord(t, Record{StopKey: "earlier", StartedAt: time.Now(), Argv0: "stand-in", Liveness: livenessLock})
	files := map[string]string{"prompt": "earlier prompt", "stdout": "earlier output", "stop-request.json": "earlier request", startedName: "earlier"}
	for name, body := range files {
		if err := os.WriteFile(d.path(name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	record, _ := os.ReadFile(d.path("run.json"))
	held, err := lock.AcquireNamed(string(d), turnLock)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	cli := helperCLI(t, "{}", 0, 0)
	began := time.Now()
	err = d.Start([]string{cli}, string(d), "new prompt", nil)
	var none notSpawned
	if !errors.As(err, &none) {
		t.Fatalf("Start under a held turn lock returned %v", err)
	}
	if took := time.Since(began); took < wrapperStartTimeout || took > 5*time.Second {
		t.Fatalf("Start waited %s for a lock with a %s limit", took, wrapperStartTimeout)
	}
	for name, body := range files {
		if got, _ := os.ReadFile(d.path(name)); string(got) != body {
			t.Errorf("%s was changed to %q", name, got)
		}
	}
	if got, _ := os.ReadFile(d.path("run.json")); string(got) != string(record) {
		t.Errorf("the running turn's record was replaced: %s", got)
	}
	time.Sleep(300 * time.Millisecond)
	if ranAgent(cli) {
		t.Fatal("the agent ran although Start refused")
	}
	if st, err := d.Status(); err != nil || st.Phase != Running {
		t.Fatalf("the earlier turn reads as %s %v", st.Phase, err)
	}
}

// A wrapper that never takes the lock leaves a defined state: Start says no
// process will run the turn, the turn reads as crashed at once and every
// time, and the next Start runs normally.
func TestAWrapperThatNeverStartsLeavesACrashedTurn(t *testing.T) {
	shortStartLimit(t)
	d := Dir(filepath.Join(t.TempDir(), "target"))
	cli := helperCLI(t, "{}", 0, 0)
	began := time.Now()
	err := d.Start([]string{cli}, filepath.Dir(string(d)), "fictional prompt", []string{"KEEPGOING_TEST_WRAPPER=dead"})
	var none notSpawned
	if !errors.As(err, &none) || !errors.Is(err, errNoWrapper) {
		t.Fatalf("Start returned %v", err)
	}
	if took := time.Since(began); took < wrapperStartTimeout || took > 5*time.Second {
		t.Fatalf("Start took %s with a %s limit", took, wrapperStartTimeout)
	}
	for i := 0; i < 3; i++ {
		st, err := d.Status()
		if err != nil || st.Phase != Crashed || !st.Record.Abandoned {
			t.Fatalf("read %d: %+v %v", i, st, err)
		}
	}
	if _, err := os.Stat(d.path("stop-request.json")); !os.IsNotExist(err) {
		t.Fatal("a stop request was left for a turn that never started")
	}
	if ranAgent(cli) {
		t.Fatal("the agent ran")
	}
	if err := d.Start([]string{cli}, filepath.Dir(string(d)), "fictional prompt", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, d, Finished)
	if st, _ := d.Status(); st.Record.Abandoned || !ranAgent(cli) {
		t.Fatalf("%+v", st)
	}
}

// The same for the Antigravity runner: a worker that never starts is
// recorded as not started, so the turn is an ordinary unknown failure and a
// later send proceeds. It is not left unconfirmed, which nothing clears.
func TestAntigravityWorkerThatNeverStartsIsNotUnconfirmed(t *testing.T) {
	shortStartLimit(t)
	w := newAntigravityWorld(t, "ok", false)
	w.a.cfg.Env["KEEPGOING_TEST_WRAPPER"] = "dead"
	if err := w.a.Send(context.Background(), "fictional task", adapter.ModeAuto); err == nil {
		t.Fatal("a send whose worker never started reported success")
	}
	turn, _, _ := w.a.dir.antigravityTurn()
	if _, err := os.Stat(w.a.dir.antigravityTurnDir(turn).path(antigravityNotStartedName)); err != nil {
		t.Fatalf("the turn was not recorded as not started: %v", err)
	}
	if obs := w.observe(t); !unknownError(obs) {
		t.Fatalf("%+v", obs)
	}
	delete(w.a.cfg.Env, "KEEPGOING_TEST_WRAPPER")
	w.send(t, "fictional retry")
	waitFor(t, w.a.dir, Finished)
	if obs := w.observe(t); obs.State != decide.Idle || len(w.named(t, "prompt")) != 1 {
		t.Fatalf("%+v %v", obs, w.events(t))
	}
}

// Something at the exit code's place that cannot be read is an error, at
// once. It used to be retried without end.
func TestAnUnreadableExitCodeIsAnErrorNotAnEndlessRetry(t *testing.T) {
	d := writeRecord(t, Record{StartedAt: time.Now(), Argv0: "stand-in", Liveness: livenessLock})
	if err := os.Mkdir(d.path("exit"), 0o700); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := d.Status(); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a directory in place of the exit code read as a status")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the status read did not return")
	}
}

// A finishing marker left by a wrapper that died while publishing is waited
// for once, when it is fresh. An old one costs a read nothing.
func TestAnOldFinishingMarkerIsNotWaitedFor(t *testing.T) {
	d := writeRecord(t, Record{StartedAt: time.Now(), Argv0: "stand-in", Liveness: livenessLock})
	if err := os.WriteFile(d.path(finishingName), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(d.path(finishingName), old, old); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	st, err := d.Status()
	if err != nil || st.Phase != Crashed {
		t.Fatalf("%+v %v", st, err)
	}
	if took := time.Since(began); took > finishingWait/2 {
		t.Fatalf("a read waited %s on a marker a minute old", took)
	}
}

// A record an older build wrote is judged by its PID, except that nothing
// outlives a restart of the machine: one from before the machine started is
// crashed even though a process has its PID. This frees a loop that was
// already stuck on a reused PID when it was upgraded.
func TestLegacyRecordFromBeforeTheMachineStartedIsCrashed(t *testing.T) {
	previous := machineStarted
	t.Cleanup(func() { machineStarted = previous })
	boot := time.Now().Add(-time.Hour)
	phase := func(started time.Time, known bool) Phase {
		t.Helper()
		machineStarted = func() (time.Time, bool) { return boot, known }
		st, err := writeRecord(t, Record{PID: os.Getpid(), StartedAt: started, Argv0: "stand-in"}).Status()
		if err != nil {
			t.Fatal(err)
		}
		return st.Phase
	}
	if got := phase(boot.Add(-24*time.Hour), true); got != Crashed {
		t.Fatalf("a turn from before the machine started reads as %s", got)
	}
	if got := phase(boot.Add(time.Minute), true); got != Running {
		t.Fatalf("a turn started since the machine did reads as %s", got)
	}
	// Close to the start, clocks are not trusted to tell the order.
	if got := phase(boot.Add(-bootSlack/2), true); got != Running {
		t.Fatalf("a turn started just before the machine's recorded start reads as %s", got)
	}
	if got := phase(boot.Add(-24*time.Hour), false); got != Running {
		t.Fatalf("with no start time for the machine, a turn reads as %s", got)
	}
	// The real reading, where the platform has one.
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		at, ok := bootTime()
		if !ok || at.After(time.Now()) || at.Year() < 2001 {
			t.Fatalf("machine start read as %s %t", at, ok)
		}
	}
}

// A binary on disk that is not this build would run the agent as a wrapper
// without taking the turn lock, so the daemon would see no turn and start
// another. Start asks the binary which wrapper it is first, and refuses,
// changing nothing and running nothing, when the answer is not its own.
func TestStartRefusesABinaryFromAnotherBuild(t *testing.T) {
	forget := func() {
		agreed.Lock()
		agreed.path = ""
		agreed.Unlock()
	}
	forget()
	t.Cleanup(forget)
	d := Dir(filepath.Join(t.TempDir(), "target"))
	cli := helperCLI(t, "{}", 0, 0)
	err := d.Start([]string{cli}, filepath.Dir(string(d)), "fictional prompt", []string{"KEEPGOING_TEST_WRAPPER=other-build"})
	var none notSpawned
	if !errors.As(err, &none) || !strings.Contains(err.Error(), "stop the daemon before replacing or restoring a binary") {
		t.Fatalf("Start with another build's binary on disk returned %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, statErr := os.Stat(string(d)); !os.IsNotExist(statErr) || ranAgent(cli) {
		t.Fatal("a refused start created the turn directory or ran the agent")
	}
	if strings.Contains(err.Error(), filepath.Dir(string(d))) {
		t.Fatalf("the refusal names a path: %v", err)
	}
	// This build's own binary answers, once: a second start does not ask again.
	if err := d.Start([]string{cli}, filepath.Dir(string(d)), "fictional prompt", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, d, Finished)
	agreed.Lock()
	remembered := agreed.path != ""
	agreed.Unlock()
	if !remembered {
		t.Fatal("the answer was not kept")
	}
	// Kept answers are not asked for again, even by a turn whose environment
	// would make the binary answer wrongly; a changed file is asked again.
	if err := d.Start([]string{cli}, filepath.Dir(string(d)), "fictional prompt", []string{"KEEPGOING_TEST_WRAPPER=other-build"}); err != nil {
		t.Fatalf("a kept answer was asked for again: %v", err)
	}
	waitFor(t, d, Finished)
	agreed.Lock()
	agreed.size++
	agreed.Unlock()
	if err := d.Start([]string{cli}, filepath.Dir(string(d)), "fictional prompt", []string{"KEEPGOING_TEST_WRAPPER=other-build"}); err == nil {
		t.Fatal("a binary that changed on disk was not asked again")
	}
}

// An abandoned first turn left no conversation: no process ever ran it. The
// Cursor runner treats it like a turn that never started, so the next send
// starts normally. It used to refuse every later send.
func TestCursorSendsAgainAfterAnAbandonedFirstTurn(t *testing.T) {
	shortStartLimit(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	capture := filepath.Join(root, "sent.json")
	env := map[string]string{"KEEPGOING_TEST_HELPER": "cursor", "KEEPGOING_TEST_RESULT": capture, "KEEPGOING_CURSOR_FIXTURE_STDOUT": cursorSuccess, "KEEPGOING_TEST_WRAPPER": "dead"}
	c := NewCursor(config.Cursor{CLI: self, Workdir: root, Env: env}, filepath.Join(root, "target"))
	if err := c.Send(context.Background(), "fictional task", adapter.ModeAuto); !errors.Is(err, errNoWrapper) {
		t.Fatalf("a send whose wrapper never started returned %v", err)
	}
	if st, err := c.dir.Status(); err != nil || st.Phase != Crashed || !st.Record.Abandoned {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Fatal("the agent ran")
	}
	delete(env, "KEEPGOING_TEST_WRAPPER")
	c = NewCursor(config.Cursor{CLI: self, Workdir: root, Env: env}, filepath.Join(root, "target"))
	if err := c.Send(context.Background(), "fictional task", adapter.ModeAuto); err != nil {
		t.Fatalf("the send after an abandoned first turn was refused: %v", err)
	}
	waitFor(t, c.dir, Finished)
	if _, err := os.Stat(capture); err != nil {
		t.Fatal("the agent did not run")
	}
	// A turn that did run and left no session is still refused.
	if err := os.WriteFile(c.dir.path("stdout"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(c.dir.path("session"))
	c = NewCursor(config.Cursor{CLI: self, Workdir: root, Env: env}, filepath.Join(root, "target"))
	if err := c.Send(context.Background(), "fictional task", adapter.ModeAuto); err == nil {
		t.Fatal("a turn that ran and left no session was followed by a new conversation")
	}
}

// A finishing marker dated in the future, after a clock step, is waited for
// no longer than the usual limit.
func TestAFinishingMarkerFromTheFutureIsWaitedForOnlyTheLimit(t *testing.T) {
	previous := finishingWait
	finishingWait = 300 * time.Millisecond
	defer func() { finishingWait = previous }()
	d := writeRecord(t, Record{StartedAt: time.Now(), Argv0: "stand-in", Liveness: livenessLock})
	if err := os.WriteFile(d.path(finishingName), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(d.path(finishingName), later, later); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	st, err := d.Status()
	if err != nil || st.Phase != Crashed {
		t.Fatalf("%+v %v", st, err)
	}
	if took := time.Since(began); took < 250*time.Millisecond || took > 3*time.Second {
		t.Fatalf("a read waited %s on a marker dated an hour ahead, with a %s limit", took, finishingWait)
	}
}
