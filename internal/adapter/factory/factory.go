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
	}
	// Agents on the runner contract come from its registry.
	if h, ok := runner.New(a, dir); ok {
		return h, nil
	}
	return nil, fmt.Errorf("unknown adapter %q", a.Adapter)
}

// Func is the signature supervise uses, so tests can substitute fakes.
type Func func(a config.Agent, stateDir, target string) (adapter.Adapter, error)
