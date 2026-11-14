package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doctor on an agent over ACP resolves its command without running it,
// checks the working directory, and says what keepgoing can and cannot do
// over the protocol.
func TestDoctorOnAnACPAgent(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "")
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keepgoing.yaml")
	writeCustom(t, path, `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: Continue the fictional loop.
  adapter: acp
  acp:
    name: example-agent
    command: [./bin/example-agent, acp]
    workdir: project
`, 0o600)
	doctor := func() map[string]doctorCheck {
		t.Helper()
		var out, errOut bytes.Buffer
		run(context.Background(), []string{"doctor", "-c", path, "--offline", "--json"}, &out, &errOut)
		var r struct {
			Checks []doctorCheck `json:"checks"`
		}
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("%v\n%s\n%s", err, out.String(), errOut.String())
		}
		checks := map[string]doctorCheck{}
		for _, c := range r.Checks {
			checks[c.ID] = c
		}
		return checks
	}
	c := doctor()
	if got := c["agent.primary.cli"]; got.Status != "error" {
		t.Errorf("a missing command: %+v", got)
	}
	if got := c["agent.primary.workdir"]; got.Status != "ok" {
		t.Errorf("workdir: %+v", got)
	}
	if got := c["agent.primary.capabilities"]; !strings.Contains(got.Message, "never grants") || !strings.Contains(got.Message, "Agent Client Protocol") {
		t.Errorf("capabilities: %+v", got)
	}
	if got := c["agent.primary.limits"]; got.Status != "unavailable" || !strings.Contains(got.Message, "never moves work to a fallback") {
		t.Errorf("limits: %+v", got)
	}
	// The command is found beside the configuration file and is not run.
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCustom(t, filepath.Join(dir, "bin", "example-agent"), "#!/bin/sh\necho ran >> "+filepath.Join(dir, "ran")+"\n", 0o755)
	t.Chdir(t.TempDir())
	if got := doctor()["agent.primary.cli"]; got.Status != "ok" {
		t.Errorf("a command beside the configuration: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
		t.Error("doctor ran the agent")
	}
}
