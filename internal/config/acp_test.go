package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const acpLoop = `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: Continue the loop.
  adapter: acp
  acp:
    name: example-agent
    command: [example-agent, acp]
    workdir: /example/project
`

func TestACPAgentConfiguration(t *testing.T) {
	cfg, err := Parse([]byte(acpLoop))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.Provider != "example-agent" || cfg.Loop.Describe() != "acp example-agent /example/project" || cfg.Loop.ACP.Command[1] != "acp" {
		t.Fatalf("%+v %q", cfg.Loop.ACP, cfg.Loop.Describe())
	}
	for name, tc := range map[string]struct{ old, new, want string }{
		"no command":        {"    command: [example-agent, acp]\n", "", "acp.command is the agent's ACP server command"},
		"a command line":    {"command: [example-agent, acp]", "command: example-agent acp", "cannot unmarshal"},
		"an empty argument": {"command: [example-agent, acp]", "command: [example-agent, \"\"]", "each argument is one non-empty line"},
		"a shell command":   {"command: [example-agent, acp]", "command: [sh, -c, example-agent acp]", "a shell given a command string"},
		"no workdir":        {"    workdir: /example/project\n", "", "acp.workdir is required"},
		"a name with caps":  {"name: example-agent", "name: Example", "acp.name must be"},
		"a built-in name":   {"name: example-agent", "name: codex", "name of a built-in adapter"},
		"a long timeout":    {"    workdir: /example/project\n", "    workdir: /example/project\n    exit_timeout: 1h\n", "acp.exit_timeout must be between"},
		"an unknown key":    {"    workdir: /example/project\n", "    workdir: /example/project\n    auto_approve: true\n", "field auto_approve not found"},
	} {
		if !strings.Contains(acpLoop, tc.old) {
			t.Fatalf("%s: no %q", name, tc.old)
		}
		if _, err := Parse([]byte(strings.Replace(acpLoop, tc.old, tc.new, 1))); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A script file run by a shell is allowed; the guard is for a command string.
	if _, err := Parse([]byte(strings.Replace(acpLoop, "command: [example-agent, acp]", "command: [sh, ./agent.sh]", 1))); err != nil {
		t.Errorf("a shell running a script was refused: %v", err)
	}
	// The executable resolves against the configuration file; a bare name is left for PATH.
	dir := t.TempDir()
	path := filepath.Join(dir, "keepgoing.yaml")
	text := strings.NewReplacer("command: [example-agent, acp]", "command: [./bin/example-agent, acp]", "workdir: /example/project", "workdir: project").Replace(acpLoop)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.Loop.ACP.Command[0] != filepath.Join(dir, "bin", "example-agent") || cfg.Loop.ACP.Command[1] != "acp" || cfg.Loop.ACP.Workdir != filepath.Join(dir, "project") {
		t.Fatalf("%+v %v", cfg.Loop.ACP, err)
	}
}
