package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// customAgentFixture is a loop and one fallback, both agents defined in
// configuration. The definition maps codes to classes, some of which the
// loop fails over on. Nothing is run: the executables do not exist.
func customAgentFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	definition := `
      settings: [model]
      start: [./bin/example-agent, run, --json]
      output: {format: json}
      finished: {field: status, equals: [ok]}
      errors:
        field: error.code
        classes: {quota_exceeded: rate_limit, unauthorized: auth}
        exit_codes: {3: rate_limit}
      probe: {argv: [./bin/example-agent, run, --json]}
      limits:
        argv: [./bin/example-agent, limits]
        blocked: {field: blocked, equals: ["true"]}
      capabilities: {structured_errors: true}
`
	writeCustom(t, filepath.Join(dir, "keepgoing.yaml"), `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: Continue the fictional loop.
  fallbacks: fallbacks.yaml
  adapter: custom
  custom:
    name: example-agent
    workdir: project
    definition:`+definition, 0o600)
	writeCustom(t, filepath.Join(dir, "fallbacks.yaml"), `version: 1
candidates:
  - id: example-fallback
    adapter: custom
    provider: example-other
    custom:
      name: example-other
      workdir: project
      definition:`+strings.ReplaceAll(definition, "\n      ", "\n        "), 0o600)
	return filepath.Join(dir, "keepgoing.yaml")
}

// validate says, in words, every declared code that will move work to a
// fallback, for the primary and for each defined fallback.
func TestValidateSaysWhatADefinedAgentFailsOverOn(t *testing.T) {
	path := customAgentFixture(t)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"validate", "-c", path}, &out, &errOut); code != exitOK {
		t.Fatalf("%d %s", code, errOut.String())
	}
	for _, want := range []string{
		"failover (primary, defined agent example-agent): exit code 3 is treated as a rate limit and will move work to a fallback",
		`failover (primary, defined agent example-agent): a result whose error.code is "quota_exceeded" is treated as a rate limit and will move work to a fallback`,
		"failover (primary, defined agent example-agent): an unclassified failure for which the limits command reports blocked as true is treated as a rate limit and will move work to a fallback",
		"failover (example-fallback, defined agent example-other): exit code 3 is treated as a rate limit",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("validate does not say %q:\n%s", want, out.String())
		}
	}
	// A class the loop does not fail over on is not announced as a move.
	if strings.Contains(out.String(), "unauthorized") {
		t.Errorf("auth is not a failover class here:\n%s", out.String())
	}
}

// doctor checks a defined agent's working directory and executable against
// the configuration file's directory, the same place a turn runs them from,
// and discloses what its probe may cost and what it fails over on.
func TestDoctorOnADefinedAgent(t *testing.T) {
	path := customAgentFixture(t)
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
		t.Errorf("a missing executable: %+v", got)
	}
	if got := c["agent.primary.workdir"]; got.Status != "ok" {
		t.Errorf("workdir: %+v", got)
	}
	if got := c["agent.primary.probe"]; got.Status != "warning" || !strings.Contains(got.Message, "each probe is a full paid turn") {
		t.Errorf("probe cost: %+v", got)
	}
	if got := c["agent.primary.failover"]; got.Status != "warning" || !strings.Contains(got.Message, "exit code 3 is treated as a rate limit and will move work to a fallback") ||
		!strings.Contains(got.Fix, "Leave a refusal, a policy block or an account restriction unmapped") {
		t.Errorf("failover: %+v", got)
	}
	if got := c["agent.example-fallback.failover"]; got.Status != "warning" {
		t.Errorf("fallback failover: %+v", got)
	}

	if runtime.GOOS == "windows" {
		return // the stand-in below is a script
	}
	// The executable is found beside the configuration file, whatever
	// directory doctor is run from; it is not run.
	dir := filepath.Dir(path)
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCustom(t, filepath.Join(dir, "bin", "example-agent"), "#!/bin/sh\necho ran >> "+filepath.Join(dir, "ran")+"\n", 0o755)
	t.Chdir(t.TempDir())
	if got := doctor()["agent.primary.cli"]; got.Status != "ok" {
		t.Errorf("an executable beside the configuration: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
		t.Error("doctor ran the defined agent")
	}
	if err := os.Remove(filepath.Join(dir, "project")); err != nil {
		t.Fatal(err)
	}
	if got := doctor()["agent.primary.workdir"]; got.Status != "error" {
		t.Errorf("a missing workdir: %+v", got)
	}
}

// The probe plan says a defined agent's probe may be a full paid turn.
func TestProbePlanDisclosesADefinedAgentsProbe(t *testing.T) {
	path := customAgentFixture(t)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"fallbacks", "check", "--dry-run", "--json", "-c", path}, &out, &errOut); code != exitOK {
		t.Fatalf("%d %s", code, errOut.String())
	}
	var plan probePlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || len(plan.Candidates) != 1 {
		t.Fatalf("%v %s", err, out.String())
	}
	if c := plan.Candidates[0]; c.Kind != "custom" || !strings.Contains(c.CostNote, "it may be a full paid turn of the agent") {
		t.Fatalf("%+v", c)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "state", "health.json")); !os.IsNotExist(err) {
		t.Error("a dry run recorded a probe")
	}
}

func writeCustom(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
