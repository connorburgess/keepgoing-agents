package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	operatorA = "thr_example_operator_a"
	operatorB = "thr_example_operator_b"
)

// gateShow holds every `bb thread show` until n have arrived, or until
// openGate when n is 0. See the stand-in.
func (w *World) gateShow(n int) {
	w.t.Helper()
	for _, name := range []string{"show.open", "show.arrived"} {
		os.Remove(filepath.Join(w.world, name))
	}
	if err := os.WriteFile(filepath.Join(w.world, "show.gate"), []byte(fmt.Sprint(n)), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func (w *World) openGate() {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.world, "show.open"), nil, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func (w *World) removeGate() {
	for _, name := range []string{"show.gate", "show.open", "show.arrived"} {
		os.Remove(filepath.Join(w.world, name))
	}
}

// waitArrivals waits until n `bb thread show` calls are held at the gate.
func (w *World) waitArrivals(n int) {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if info, err := os.Stat(filepath.Join(w.world, "show.arrived")); err == nil && info.Size() >= int64(n) {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("%d checks never reached bb thread show", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// tickInBackground starts one check and returns its result when it ends.
func (w *World) tickInBackground(config, thread string) <-chan Result {
	done := make(chan Result, 1)
	args := []string{"tick", "-c", config, "--now", w.stamp()}
	go func() {
		cmd := exec.Command(keepgoingBin, args...)
		cmd.Dir = w.Root
		cmd.Env = append(append([]string{}, w.env...), "BB_THREAD_ID="+thread)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		code := 0
		if x, ok := err.(*exec.ExitError); ok {
			code = x.ExitCode()
		} else if err != nil {
			code = -1
			errb.WriteString(err.Error())
		}
		done <- Result{Args: args, Code: code, Stdout: out.String(), Stderr: errb.String()}
	}()
	return done
}

func tickLine(t *testing.T, r Result) map[string]any {
	t.Helper()
	var line map[string]any
	if r.Code == 0 {
		if err := json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &line); err != nil {
			t.Fatalf("tick output: %v\n%s", err, r.Stdout)
		}
	}
	return line
}

// daemon is a running `keepgoing daemon`.
type daemon struct {
	cmd  *exec.Cmd
	out  bytes.Buffer
	done chan error
}

// startDaemon starts `keepgoing daemon` as thread and waits until it has
// published its endpoint, which it does once it holds the daemon lock.
func (w *World) startDaemon(thread string) *daemon {
	w.t.Helper()
	endpoint := filepath.Join(filepath.Dir(w.Config), "state", "daemon-endpoint.json")
	os.Remove(endpoint)
	d := &daemon{done: make(chan error, 1)}
	d.cmd = exec.Command(keepgoingBin, "daemon", "-c", w.Config)
	d.cmd.Dir = w.Root
	d.cmd.Env = append(append([]string{}, w.env...), "BB_THREAD_ID="+thread)
	d.cmd.Stdout, d.cmd.Stderr = &d.out, &d.out
	if err := d.cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	go func() { d.done <- d.cmd.Wait() }()
	w.t.Cleanup(func() {
		d.cmd.Process.Kill()
		<-d.done
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(endpoint); err == nil {
			return d
		}
		select {
		case err := <-d.done:
			w.t.Fatalf("the daemon exited before it was running: %v\n%s", err, d.out.String())
		default:
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("the daemon did not start\n%s", d.out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// exited waits for the daemon to end by itself, and reports how.
func (d *daemon) exited(within time.Duration) (code int, ok bool) {
	select {
	case err := <-d.done:
		d.done <- err // for the cleanup
		if x, isExit := err.(*exec.ExitError); isExit {
			return x.ExitCode(), true
		}
		return 0, err == nil
	case <-time.After(within):
		return 0, false
	}
}

// TestScenarioTwoSupervisorsOnOneLoop: two callers for one loop at once. A
// check that starts while another is in the middle of its work is skipped
// (exit 75, which a scheduler treats as success) and the session is sent one
// thing. A second daemon refuses to start. When another thread takes the
// loop over, the running daemon stops by itself before doing any work as the
// old owner, and the new owner can start one.
func TestScenarioTwoSupervisorsOnOneLoop(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init("await_user")
	s := w.Run("enable", "-c", w.Config)
	w.Check(s.asStep(w, "enable"), "enable", exit(s.asStep(w, "enable"), 0))

	// A check is held in the middle of observing the session; a second one
	// starts meanwhile.
	w.Advance(time.Minute)
	w.gateShow(0)
	first := w.tickInBackground(w.Config, "")
	w.waitArrivals(1)
	w.record("first check held while observing", Result{}, nil) // its bb call so far
	second := w.Run("tick", "-c", w.Config, "--now", w.stamp())
	step := w.record("second check during the first", second, nil)
	w.Check(step, "the second check is skipped", all(
		exit(step, 75), check(strings.Contains(second.Stderr, "another tick holds the lock; skipping"), "stderr %q", second.Stderr),
		nothingSent(step), noCalls(step), noReports(step),
	))
	w.openGate()
	r := <-first
	w.removeGate()
	step = w.record("tick", r, tickLine(t, r))
	w.Check(step, "the first check wakes the session once", all(
		action(step, "wake"), sentExactly(step, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}),
	))

	// The loop is paused so the daemons below do no work, and is owned by
	// operator A, as it is when A starts it.
	w.Run("pause", "-c", w.Config)
	if r := w.RunAs(operatorA, "enable", "-c", w.Config); r.Code != 0 {
		t.Fatalf("enable as operator A: %d %s", r.Code, r.Stderr)
	}
	dA := w.startDaemon(operatorA)

	// A second daemon for the same loop refuses to start. It is given 20
	// seconds: a daemon that does start runs until it is stopped.
	r = w.runBounded(20*time.Second, operatorA, "daemon", "-c", w.Config)
	step = w.record("second daemon", r, nil)
	w.Check(step, "a second daemon refuses", all(
		exit(step, 75), check(strings.Contains(r.Stderr, "another daemon is running for example-loop"), "stderr %q", r.Stderr),
		nothingSent(step),
	))
	// So does a check from another thread, before any takeover.
	r = w.RunAs(operatorB, "tick", "-c", w.Config, "--now", w.stamp())
	step = w.record("check from operator B", r, nil)
	w.Check(step, "another thread's check is refused", all(exit(step, 1), check(strings.Contains(r.Stderr, "owned by BB thread "+operatorA), "stderr %q", r.Stderr), nothingSent(step), noCalls(step)))

	// Operator B takes the loop over. The daemon operator A started notices
	// at its next control check and stops by itself.
	r = w.RunAs(operatorB, "enable", "-c", w.Config, "--take-over")
	step = w.record("take over", r, nil)
	w.Check(step, "takeover", all(exit(step, 0), holds(step, "paused"), nothingSent(step)))
	code, ended := dA.exited(20 * time.Second)
	if !ended {
		// Nothing after this can be trusted: two daemons may now be running.
		t.Fatalf("the old owner's daemon was still running 20 s after the takeover\n%s", dA.out.String())
	}
	w.Check(step, "the old owner's daemon stops by itself", check(code == 0 || code == 1 || code == 75, "it exited %d", code))
	w.Check(step, "it sent nothing on the way out", nothingSent(w.record("after the old daemon", Result{}, nil)))

	// Operator B starts its own daemon.
	dB := w.startDaemon(operatorB)
	_ = dB
	r = w.RunAs(operatorA, "tick", "-c", w.Config, "--now", w.stamp())
	step = w.record("check from operator A", r, nil)
	// Refused for its owner (exit 1), or skipped because operator B's daemon
	// is in the middle of its own check (exit 75): the tick lock is taken
	// before ownership is checked. Either way it does no work.
	w.Check(step, "the old owner is now refused", all(
		check(r.Code == 1 && strings.Contains(r.Stderr, "owned by BB thread "+operatorB) ||
			r.Code == 75 && strings.Contains(r.Stderr, "another tick holds the lock"), "exit %d: %s", r.Code, r.Stderr),
		nothingSent(step), noCalls(step)))
}

// runBounded runs keepgoing as thread, and stops it if it is still running
// after limit. A stopped command has exit code -1.
func (w *World) runBounded(limit time.Duration, thread string, args ...string) Result {
	w.t.Helper()
	cmd := exec.Command(keepgoingBin, args...)
	cmd.Dir = w.Root
	cmd.Env = append(append([]string{}, w.env...), "BB_THREAD_ID="+thread)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	code := 0
	select {
	case err := <-done:
		if x, ok := err.(*exec.ExitError); ok {
			code = x.ExitCode()
		}
	case <-time.After(limit):
		cmd.Process.Kill()
		<-done
		code = -1
		errb.WriteString(fmt.Sprintf("still running after %s; stopped by the test", limit))
	}
	return Result{Args: args, Code: code, Stdout: out.String(), Stderr: errb.String()}
}

// asStep is a step for a command whose result is checked on its own.
func (r Result) asStep(w *World, label string) *Step {
	return &Step{Label: label, At: w.Now, Result: r}
}

// TestScenarioTwoLoopsOnOneSession: two separately configured loops, each with
// its own state directory, are set up on the same BB thread. Enabling the
// second is refused, naming the first, until it is asked for explicitly.
// Checked one after the other, the second sees the session working and
// leaves it alone. Checked at the same moment, only one acts: the other finds
// the session claimed, sends nothing and exits 75 as a busy check does.
func TestScenarioTwoLoopsOnOneSession(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init("await_user")
	w.otherLoops = true
	primary := w.Thread(primaryThread)
	other := filepath.Join(w.Root, "other", "keepgoing.yaml")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(w.Config)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(cfg), "name: example-loop", "name: example-loop-b", 1)
	body = strings.Replace(body, "state_dir: "+filepath.Join(filepath.Dir(w.Config), "state"), "state_dir: "+filepath.Join(filepath.Dir(other), "state"), 1)
	if !strings.Contains(body, "example-loop-b") || !strings.Contains(body, filepath.Join(filepath.Dir(other), "state")) {
		t.Fatalf("second configuration:\n%s", body)
	}
	if err := os.WriteFile(other, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// The same task, beside the second configuration.
	prompt, err := os.ReadFile(filepath.Join(filepath.Dir(w.Config), "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(other), "prompt.md"), prompt, 0o600); err != nil {
		t.Fatal(err)
	}
	w.Run("enable", "-c", w.Config)
	// Enabling a second loop on a session another loop supervises is
	// refused, naming that loop and the session.
	r := w.Run("enable", "-c", other)
	step := w.record("enable the second loop", r, nil)
	w.Check(step, "enabling a second loop on a supervised session is refused and says so", all(exit(step, 1),
		check(strings.Contains(r.Stderr, "loop example-loop ") && strings.Contains(r.Stderr, primaryThread) && strings.Contains(r.Stderr, "--allow-second-loop"), "stderr %q", r.Stderr),
		nothingSent(step)))
	// Asked for explicitly, it is enabled, with a warning naming the first.
	r = w.Run("enable", "-c", other, "--allow-second-loop")
	step = w.record("enable the second loop anyway", r, nil)
	w.Check(step, "second loop enabled when asked, with a warning", all(exit(step, 0),
		check(strings.Contains(r.Stderr, "warning: loop example-loop ") && strings.Contains(r.Stderr, primaryThread), "stderr %q", r.Stderr)))

	// One after the other: the first wakes the session; the second sees it
	// working and sends nothing.
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "the first loop wakes the session", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"})))
	r = w.Run("tick", "-c", other, "--now", w.stamp())
	step = w.record("the second loop", r, tickLine(t, r))
	w.Check(step, "the second loop leaves a working session alone", all(exit(step, 0), action(step, "none"), reasonIs(step, "active"), nothingSent(step)))

	// The turn ends. Both loops check at the same moment. The first to claim
	// the session observes and wakes it; the other finds it claimed before it
	// observes anything.
	primary.FinishTurn()
	w.Advance(30 * time.Minute)
	// The gate holds the first observation until the other check has run
	// to its end, so the two checks overlap.
	w.gateShow(0)
	a := w.tickInBackground(w.Config, "")
	w.waitArrivals(1)
	rb := <-w.tickInBackground(other, "")
	w.openGate()
	ra := <-a
	w.removeGate()
	step = w.record("both loops at once", ra, nil)
	w.Check(step, "a session checked by two loops at once is woken once", all(
		check(ra.Code == 0, "the first loop exited %d: %s", ra.Code, ra.Stderr),
		check(rb.Code == 75 && strings.Contains(rb.Stderr, primaryThread) && strings.Contains(rb.Stderr, "being checked by loop example-loop ") && strings.Contains(rb.Stderr, "nothing sent"), "the second loop exited %d: %s", rb.Code, rb.Stderr),
		check(len(step.Sent) == 1, "the session received %s", describeSends(step.Sent)),
	))
	// The other order: the second loop claims first and the first is skipped.
	primary.FinishTurn()
	w.Advance(30 * time.Minute)
	w.gateShow(0)
	b := w.tickInBackground(other, "")
	w.waitArrivals(1)
	ra = <-w.tickInBackground(w.Config, "")
	w.openGate()
	rb = <-b
	w.removeGate()
	step = w.record("both loops at once, the other way round", rb, nil)
	w.Check(step, "the first loop is skipped while the second acts", all(
		check(rb.Code == 0, "the second loop exited %d: %s", rb.Code, rb.Stderr),
		check(ra.Code == 75 && strings.Contains(ra.Stderr, "being checked by loop example-loop-b "), "the first loop exited %d: %s", ra.Code, ra.Stderr),
		check(len(step.Sent) == 1, "the session received %s", describeSends(step.Sent)),
	))
}
