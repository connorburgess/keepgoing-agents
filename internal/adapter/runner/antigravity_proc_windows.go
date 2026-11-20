//go:build windows

package runner

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

const (
	agentProcessTerminate = 0x0001
	agentProcessSetQuota  = 0x0100
)

func configureAgentProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// agentTree holds an unnamed nested Job Object for the ACP agent and its
// descendants. The process handle is opened while the worker still owns the
// unwaited child, so the PID cannot be recycled before assignment.
type agentTree struct {
	mu   sync.Mutex
	job  syscall.Handle
	proc *os.Process
	done bool
}

func newAgentTree(cmd *exec.Cmd) *agentTree {
	t := &agentTree{proc: cmd.Process}
	kernel := syscall.NewLazyDLL("kernel32.dll")
	job, _, _ := kernel.NewProc("CreateJobObjectW").Call(0, 0)
	if job == 0 {
		return t
	}
	h, err := syscall.OpenProcess(agentProcessTerminate|agentProcessSetQuota, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return t
	}
	defer syscall.CloseHandle(h)
	ok, _, _ := kernel.NewProc("AssignProcessToJobObject").Call(job, uintptr(h))
	if ok == 0 {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return t
	}
	t.job = syscall.Handle(job)
	return t
}

func (t *agentTree) Kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	if t.job != 0 {
		kernel := syscall.NewLazyDLL("kernel32.dll")
		ok, _, _ := kernel.NewProc("TerminateJobObject").Call(uintptr(t.job), 1)
		if ok != 0 {
			return
		}
	}
	if t.proc != nil {
		_ = t.proc.Kill()
	}
}

func (t *agentTree) MarkReaped() {
	t.mu.Lock()
	t.done = true
	t.mu.Unlock()
}

func (t *agentTree) Close() {
	t.mu.Lock()
	job := t.job
	t.job = 0
	t.done = true
	t.mu.Unlock()
	if job != 0 {
		_ = syscall.CloseHandle(job)
	}
}
