//go:build !unix && !windows

package runner

import (
	"os"
	"os/exec"
	"sync"
)

func configureAgentProcess(*exec.Cmd) {}

type agentTree struct {
	mu   sync.Mutex
	proc *os.Process
	done bool
}

func newAgentTree(cmd *exec.Cmd) *agentTree {
	return &agentTree{proc: cmd.Process}
}

func (t *agentTree) Kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done && t.proc != nil {
		_ = t.proc.Kill()
	}
}

func (t *agentTree) MarkReaped() {
	t.mu.Lock()
	t.done = true
	t.mu.Unlock()
}

func (t *agentTree) Close() {}
