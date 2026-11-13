// Package factory builds the adapter for an agent definition.
package factory

import (
	"fmt"
	"path/filepath"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/bb"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/process"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
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
	case "acp": // config.ACPAdapter, written out so the capability table's check sees it
		if a.ACP == nil {
			return nil, fmt.Errorf("the acp adapter needs its acp settings, and the agent definition has none")
		}
		// Any agent that speaks ACP: classes from typed ends only, a saved
		// session continued by whatever the agent advertises, and a clean
		// cancel on stop.
		return runner.NewACP(runner.ACPConfig{Name: config.ACPAdapter, Classify: true, AllowWithoutResume: true, CancelOnStop: true,
			Command: a.ACP.Command, Workdir: a.ACP.Workdir, Env: a.ACP.Env,
			HandshakeTimeout: a.ACP.HandshakeTimeout.Duration, ExitTimeout: a.ACP.ExitTimeout.Duration}, dir), nil
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
