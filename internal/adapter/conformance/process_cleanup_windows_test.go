//go:build windows

package conformance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
)

type fixtureProcess struct {
	pid    int
	handle syscall.Handle
	role   string
}
type fixtureProcesses struct {
	sync.Mutex
	processes []fixtureProcess
	errors    []error
	logf      func(string, ...any)
}

func (owned *fixtureProcesses) capture(process *os.Process) error {
	return owned.captureRole(process, "wrapper")
}

func (owned *fixtureProcesses) captureRole(process *os.Process, role string) error {
	// At wrapper spawn, cmd.Start owns the process until this callback
	// returns. At crash staging, the still-live wrapper owns its blocked
	// stand-in child until the fixture deliberately kills that wrapper. Windows reserves the PID until that handle closes, even
	// if the child already exited. This lookup is bound to that owned lifetime;
	// it never consults a saved record or reopens a PID during cleanup.
	h, err := syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, uint32(process.Pid))
	owned.Lock()
	defer owned.Unlock()
	if err != nil {
		owned.errors = append(owned.errors, fmt.Errorf("capture owned fixture process pid=%d: %w", process.Pid, err))
		return err
	}
	owned.processes = append(owned.processes, fixtureProcess{pid: process.Pid, handle: h, role: role})
	if owned.logf != nil {
		owned.logf("captured %s pid=%d handle=%d", role, process.Pid, h)
	}
	return nil
}

func (owned *fixtureProcesses) join() []error {
	owned.Lock()
	processes, problems := owned.processes, owned.errors
	owned.processes, owned.errors = nil, nil
	owned.Unlock()
	deadline := time.Now().Add(30 * time.Second)
	for _, p := range processes {
		remaining := time.Until(deadline)
		var ms uint32
		if remaining > 0 {
			ms = uint32((remaining + time.Millisecond - 1) / time.Millisecond)
		}
		wait, err := syscall.WaitForSingleObject(p.handle, ms)
		var code uint32
		queryErr := syscall.GetExitCodeProcess(p.handle, &code)
		if owned.logf != nil {
			owned.logf("joined %s pid=%d wait=%d error=%v exit=%d query=%v", p.role, p.pid, wait, err, code, queryErr)
		}
		if err != nil || wait != syscall.WAIT_OBJECT_0 {
			problems = append(problems, fmt.Errorf("owned fixture process pid=%d wait=%d error=%v exit=%d query=%v", p.pid, wait, err, code, queryErr))
		}
		if wait == syscall.WAIT_OBJECT_0 && queryErr != nil {
			problems = append(problems, fmt.Errorf("query joined fixture process pid=%d: %w", p.pid, queryErr))
		}
		if err := syscall.CloseHandle(p.handle); err != nil {
			problems = append(problems, fmt.Errorf("close owned fixture process pid=%d: %w", p.pid, err))
		}
	}
	return problems
}

func trackFixtureWrappers(t *testing.T, project string) func(int) error {
	t.Helper()
	owned := &fixtureProcesses{logf: func(format string, args ...any) {
		t.Logf("fixture project=%s: "+format, append([]any{project}, args...)...)
	}}
	remove := runner.ObserveSpawnsForTest(func(process *os.Process, workdir string) {
		if workdir == project {
			owned.capture(process)
		}
	})
	// Registered after TempDir: this runs first. Removal drains captures before
	// joining, so no delayed callback can add a handle after cleanup concludes.
	t.Cleanup(func() {
		remove()
		for _, err := range owned.join() {
			t.Error(err)
		}
	})
	return func(pid int) error { return owned.captureRole(&os.Process{Pid: pid}, "crash-agent") }
}

// The original process reference remains owned even after OS exit and before
// cmd.Wait. Capturing at that boundary must work, and every handle must close.
func TestFixtureProcessCaptureAfterExitAndMultipleHandles(t *testing.T) {
	owned := &fixtureProcesses{}
	var handles []syscall.Handle
	for i := 0; i < 2; i++ {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), envPart+"=unknown-fixture-part")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// This temporary owned handle establishes process exit without releasing
		// cmd.Process (the callback's real ownership window).
		h, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		wait, err := syscall.WaitForSingleObject(h, 30000)
		syscall.CloseHandle(h)
		if err != nil || wait != syscall.WAIT_OBJECT_0 {
			t.Fatalf("exit: %d %v", wait, err)
		}
		owned.capture(cmd.Process)
		_ = cmd.Wait()
	}
	for _, p := range owned.processes {
		handles = append(handles, p.handle)
	}
	if len(handles) != 2 {
		t.Fatalf("captured %d handles, errors=%v", len(handles), owned.errors)
	}
	if problems := owned.join(); len(problems) != 0 {
		t.Fatal(problems)
	}
	for _, h := range handles {
		var code uint32
		if err := syscall.GetExitCodeProcess(h, &code); err != syscall.Errno(6) {
			t.Fatalf("handle not closed: %v", err)
		}
	}
	if problems := owned.join(); len(problems) != 0 {
		t.Fatalf("second cleanup: %v", problems)
	}
}

func TestFixtureProcessCaptureErrorIsEvidence(t *testing.T) {
	owned := &fixtureProcesses{}
	owned.capture(&os.Process{Pid: 0})
	if len(owned.processes) != 0 {
		t.Fatal("failed capture retained a handle")
	}
	if problems := owned.join(); len(problems) != 1 {
		t.Fatalf("capture failure lost: %v", problems)
	}
}

func TestFixtureProcessWaitErrorStillClosesRemainingHandles(t *testing.T) {
	owned := &fixtureProcesses{processes: []fixtureProcess{{pid: 0, handle: 0}}}
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), envPart+"=unknown-fixture-part")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	owned.capture(cmd.Process)
	_ = cmd.Wait()
	if len(owned.processes) != 2 {
		t.Fatalf("capture failed: %v", owned.errors)
	}
	h := owned.processes[1].handle
	problems := owned.join()
	if len(problems) != 2 {
		t.Fatalf("wait and close errors must be evidence: %v", problems)
	}
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != syscall.Errno(6) {
		t.Fatalf("later handle leaked after earlier error: %v", err)
	}
}

// Crash staging intentionally orphans its blocked stand-in. Finished markers
// and wrapper death do not join that agent; retain its handle before the kill.
func TestCrashedFixtureRetainsAgentUntilActualExit(t *testing.T) {
	s := subject(t, "claude")
	r := s.Rig.(turnRig).rig
	project := s.Protected[0]
	if err := os.Remove(filepath.Join(project, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "exit-delay"), []byte("2000"), 0600); err != nil {
		t.Fatal(err)
	}
	original := r.retainAgent
	var agent syscall.Handle
	var agentPID, wrapperPID int
	defer func() {
		if agent != 0 {
			syscall.WaitForSingleObject(agent, 30000)
			syscall.CloseHandle(agent)
		}
	}()
	r.retainAgent = func(pid int) error {
		if err := original(pid); err != nil {
			return err
		}
		var err error
		agent, err = syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, uint32(pid))
		agentPID = pid
		st, statusErr := runner.Dir(filepath.Join(r.stateDir, "targets", "primary")).Status()
		wrapperPID = st.Record.PID
		if statusErr != nil || st.Phase != runner.Running {
			return fmt.Errorf("ownership lost before capture: %s %v", st.Phase, statusErr)
		}
		return err
	}
	a, err := s.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Stage(context.Background(), a, Crashed); err != nil {
		t.Fatal(err)
	}
	st, err := runner.Dir(filepath.Join(r.stateDir, "targets", "primary")).Status()
	if err != nil || st.Phase != runner.Crashed {
		t.Fatalf("crash semantics changed: %s %v", st.Phase, err)
	}
	wait, err := syscall.WaitForSingleObject(agent, 0)
	if err != nil || wait != syscall.WAIT_TIMEOUT {
		t.Fatalf("orphaned agent already gone: %d %v", wait, err)
	}
	var code uint32
	if err := syscall.GetExitCodeProcess(agent, &code); err != nil {
		t.Fatal(err)
	}
	err = os.Remove(project)
	var errno syscall.Errno
	errors.As(err, &errno)
	t.Logf("crashed wrapper=%d blocked-agent=%d wait=%d exit=%d removal errno=%d", wrapperPID, agentPID, wait, code, errno)
	if errno != syscall.Errno(32) {
		t.Fatalf("agent cwd sharing probe: %v", err)
	}
	wait, err = syscall.WaitForSingleObject(agent, 30000)
	if err != nil || wait != syscall.WAIT_OBJECT_0 {
		t.Fatalf("agent join=%d %v", wait, err)
	}
	if err := syscall.GetExitCodeProcess(agent, &code); err != nil {
		t.Fatal(err)
	}
	t.Logf("same agent joined=%d exit=%d; wrapper cmd.Wait completion is not exposed", wait, code)
	if err := os.Remove(project); err != nil {
		t.Fatalf("single cleanup after exact agent exit: %v", err)
	}
}
