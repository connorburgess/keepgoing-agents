//go:build unix

package runner

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

func configureAgentProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// agentTree owns the process group created for the ACP agent child so worker
// timeouts and stops end descendants together without signalling a recycled ID.
type agentTree struct {
	mu   sync.Mutex
	pid  int
	proc *os.Process
	done bool
}

func newAgentTree(cmd *exec.Cmd) *agentTree {
	return &agentTree{pid: cmd.Process.Pid, proc: cmd.Process}
}

func (t *agentTree) Kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done || t.pid <= 0 {
		return
	}
	if syscall.Kill(-t.pid, syscall.SIGKILL) != nil && t.proc != nil {
		_ = t.proc.Kill()
	}
}

func (t *agentTree) MarkReaped() {
	t.mu.Lock()
	t.done = true
	t.mu.Unlock()
}

func (t *agentTree) Close() {}
