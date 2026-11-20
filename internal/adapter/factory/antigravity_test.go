package factory

import (
	"testing"

	"github.com/looptender/looptender/internal/config"
)

func TestFactoryBuildsAntigravityRunner(t *testing.T) {
	a, err := New(config.Agent{
		Adapter: "antigravity",
		Antigravity: &config.Antigravity{
			CLI:     "agy",
			Workdir: t.TempDir(),
			Sandbox: true,
			Agent:   "example-agent",
			AddDirs: []string{t.TempDir()},
		},
	}, t.TempDir(), "primary")
	if err != nil || a.Name() != "antigravity" {
		t.Fatalf("factory.New antigravity: adapter=%v err=%v", a, err)
	}
}
