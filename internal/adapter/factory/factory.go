// Package factory builds the adapter for an agent definition.
package factory

import (
	"fmt"
	"path/filepath"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/adapter/bb"
	"github.com/looptender/looptender/internal/adapter/process"
	"github.com/looptender/looptender/internal/adapter/runner"
	"github.com/looptender/looptender/internal/config"
)

// New returns the adapter for agent a. Headless runners keep their turn
// files under stateDir/targets/<target>.
func New(a config.Agent, stateDir, target string) (adapter.Adapter, error) {
	dir := filepath.Join(stateDir, "targets", target)
	switch a.Adapter {
	case "bb":
		return bb.New(*a.BB).WithStateDir(dir), nil
	case "process":
		return process.New(*a.Process), nil
	case "cursor":
		return runner.NewCursor(*a.Cursor, dir), nil
	case "antigravity":
		cli := a.Antigravity.CLI
		if cli == "" {
			cli = "agy"
		}
		cmd := []string{cli, "acp", "--stdio"}
		if a.Antigravity.Sandbox {
			cmd = append(cmd, "--sandbox")
		}
		if a.Antigravity.Agent != "" {
			cmd = append(cmd, "--agent", a.Antigravity.Agent)
		}
		for _, addDir := range a.Antigravity.AddDirs {
			cmd = append(cmd, "--add-dir", addDir)
		}
		return runner.NewAntigravity(runner.AntigravityConfig{
			Command: cmd,
			Workdir: a.Antigravity.Workdir,
			Env:     a.Antigravity.Env,
		}, dir), nil
	}
	// Agents on the runner contract come from its registry.
	if h, ok := runner.New(a, dir); ok {
		return h, nil
	}
	if _, registered := runner.SpecFor(a.Adapter); registered || a.Adapter == config.CustomAdapter {
		return nil, fmt.Errorf("the %s adapter needs its %s settings, and the agent definition has none", a.Adapter, a.Adapter)
	}
	return nil, fmt.Errorf("unknown adapter %q", a.Adapter)
}

// Func is the signature supervise uses, so tests can substitute fakes.
type Func func(a config.Agent, stateDir, target string) (adapter.Adapter, error)
