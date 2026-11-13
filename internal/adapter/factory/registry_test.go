package factory

import (
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// Agents on the runner contract are built from its registry. A definition
// that names one without its settings is refused with an error that says so,
// not as an unknown adapter.
func TestFactoryBuildsRegisteredRunners(t *testing.T) {
	for name, agent := range map[string]config.Agent{
		"claude": {Adapter: "claude", Claude: &config.Claude{CLI: "claude", Workdir: t.TempDir()}},
		"codex":  {Adapter: "codex", Codex: &config.Codex{CLI: "codex", Workdir: t.TempDir()}},
	} {
		a, err := New(agent, t.TempDir(), "primary")
		if err != nil || a.Name() != name {
			t.Errorf("%s: %v %v", name, a, err)
		}
		if _, ok := a.(*runner.Headless); !ok {
			t.Errorf("%s is not the shared runner: %T", name, a)
		}
	}
	for _, spec := range runner.Specs() {
		a, err := New(config.Agent{Adapter: spec.Name}, t.TempDir(), "primary")
		if a != nil || err == nil || !strings.Contains(err.Error(), "the "+spec.Name+" adapter needs its "+spec.Name+" settings") || strings.Contains(err.Error(), "unknown adapter") {
			t.Errorf("%s without settings: %v %v", spec.Name, a, err)
		}
	}
	// An agent defined in configuration runs through the same shared runner.
	cfg, err := config.Parse([]byte("version: 1\nloop:\n  name: example-loop\n  state_dir: state\n  prompt: Continue.\n  adapter: custom\n  custom:\n    name: example-agent\n    workdir: /example/project\n    definition:\n      start: [example-agent, run]\n      output: {format: json}\n"))
	if err != nil {
		t.Fatal(err)
	}
	defined, err := New(cfg.Loop.Agent, t.TempDir(), "primary")
	if _, shared := defined.(*runner.Headless); err != nil || !shared || defined.Name() != "custom" {
		t.Errorf("defined agent: %T %v", defined, err)
	}
	if a, err := New(config.Agent{Adapter: "custom"}, t.TempDir(), "primary"); a != nil || err == nil || !strings.Contains(err.Error(), "the custom adapter needs its custom settings") {
		t.Errorf("custom without a definition: %v %v", a, err)
	}
	if a, err := New(config.Agent{Adapter: "example-agent"}, t.TempDir(), "primary"); a != nil || err == nil || !strings.Contains(err.Error(), `unknown adapter "example-agent"`) {
		t.Errorf("unknown adapter: %v %v", a, err)
	}
}
