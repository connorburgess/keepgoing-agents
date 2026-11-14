//go:build windows

package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
)

type fixtureProcess struct {
	pid    int
	handle syscall.Handle
}
type fixtureProcesses struct {
	sync.Mutex
	processes []fixtureProcess
	errors    []error
}

func (owned *fixtureProcesses) capture(process *os.Process) {
	// cmd.Start owns an open process handle until this callback returns and
	// cmd.Wait begins. Windows reserves the PID until that handle closes, even
	// if the child already exited. This lookup is bound to that owned lifetime;
	// it never consults a saved record or reopens a PID during cleanup.
	h, err := syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, uint32(process.Pid))
	owned.Lock()
	defer owned.Unlock()
	if err != nil {
		owned.errors = append(owned.errors, fmt.Errorf("capture owned wrapper pid=%d: %w", process.Pid, err))
		return
	}
	owned.processes = append(owned.processes, fixtureProcess{process.Pid, h})
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
		if err != nil || wait != syscall.WAIT_OBJECT_0 {
			var code uint32
			queryErr := syscall.GetExitCodeProcess(p.handle, &code)
			problems = append(problems, fmt.Errorf("owned wrapper pid=%d wait=%d error=%v exit=%d query=%v", p.pid, wait, err, code, queryErr))
		}
		if err := syscall.CloseHandle(p.handle); err != nil {
			problems = append(problems, fmt.Errorf("close owned wrapper pid=%d: %w", p.pid, err))
		}
	}
	return problems
}

func trackFixtureWrappers(t *testing.T, project string) {
	t.Helper()
	owned := &fixtureProcesses{}
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
