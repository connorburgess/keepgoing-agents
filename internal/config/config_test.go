package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const valid = `
version: 1
loop:
  name: example-loop
  state_dir: state
  adapter: bb
  bb:
    thread: thr_example123
  prompt: Continue the loop.
`

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.BB.CLI != "bb" {
		t.Errorf("cli default = %q", cfg.Loop.BB.CLI)
	}
	if cfg.Loop.Progress.StallAfter.Duration != 30*time.Minute {
		t.Errorf("stall_after default = %v", cfg.Loop.Progress.StallAfter)
	}
	if len(cfg.Loop.Errors.FailoverOn) != 1 || cfg.Loop.Errors.FailoverOn[0] != "rate_limit" {
		t.Errorf("failover_on default = %v", cfg.Loop.Errors.FailoverOn)
	}
	if cfg.Loop.Group != "primary" {
		t.Errorf("group default = %q", cfg.Loop.Group)
	}
	if cfg.Loop.Errors.MaxReprompts != 3 {
		t.Errorf("max_reprompts default = %d", cfg.Loop.Errors.MaxReprompts)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"wrong version":       strings.Replace(valid, "version: 1", "version: 2", 1),
		"bad name":            strings.Replace(valid, "example-loop", "Example Loop", 1),
		"no prompt":           strings.Replace(valid, "  prompt: Continue the loop.\n", "", 1),
		"unknown field":       valid + "  surprise: true\n",
		"bad adapter":         strings.Replace(valid, "adapter: bb", "adapter: magic", 1),
		"process w/o cmds":    strings.Replace(strings.Replace(valid, "adapter: bb", "adapter: process", 1), "  bb:\n    thread: thr_example123\n", "  process: {}\n", 1),
		"claude w/o workdir":  strings.Replace(strings.Replace(valid, "adapter: bb", "adapter: claude", 1), "  bb:\n    thread: thr_example123\n", "  claude: {model: sonnet}\n", 1),
		"failover on unknown": valid + "  errors:\n    failover_on: [unknown]\n",
		"bad report event":    valid + "  report:\n    events: [everything]\n",
		"yield w/o command":   valid + "  yield: {}\n",
		"bad window day":      valid + "  window:\n    tz: UTC\n    days: {Funday: [\"09:00\", \"17:00\"]}\n",
		"bad window time":     valid + "  window:\n    tz: UTC\n    days: {Mon: [\"9am\", \"17:00\"]}\n",
		"bad exit class": strings.Replace(strings.Replace(valid, "adapter: bb", "adapter: process", 1),
			"  bb:\n    thread: thr_example123\n",
			"  process:\n    status_command: [st]\n    resume_command: [re]\n    exit_codes: {3: nonsense}\n", 1),
	}
	for name, src := range cases {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDurationParsing(t *testing.T) {
	src := valid + "  progress:\n    stall_after: 45m\n    min_wake_interval: 2h\n"
	cfg, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.Progress.StallAfter.Duration != 45*time.Minute || cfg.Loop.Progress.MinWakeInterval.Duration != 2*time.Hour {
		t.Errorf("durations = %v %v", cfg.Loop.Progress.StallAfter, cfg.Loop.Progress.MinWakeInterval)
	}
	if _, err := Parse([]byte(valid + "  progress:\n    stall_after: soon\n")); err == nil {
		t.Error("expected an error for a non-duration")
	}
}

func TestLoadResolvesPathsAgainstTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("fallbacks.yaml", `version: 1
candidates:
  - id: custom
    adapter: process
    provider: example-cli
    process: {status_command: [./st.sh], resume_command: [re]}
    probe: {kind: command, command: [./probe.sh], cli: ./bin/claude}
`)
	write("keepgoing.yaml", `version: 1
loop:
  name: example-loop
  state_dir: state
  adapter: process
  process: {status_command: [./status.sh, --json], resume_command: [resume-on-path]}
  prompt: go
  fallbacks: fallbacks.yaml
  yield: {busy_command: [../busy.sh]}
`)
	// Load through a relative path, as `keepgoing tick` does by default.
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	os.Chdir(dir)
	cfg, err := Load("keepgoing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	same := func(got, want string) bool {
		g, _ := filepath.EvalSymlinks(filepath.Dir(got))
		return filepath.Join(g, filepath.Base(got)) == want
	}
	l := cfg.Loop
	if !filepath.IsAbs(l.StateDir) || !same(l.StateDir, filepath.Join(real, "state")) {
		t.Errorf("state_dir = %q", l.StateDir)
	}
	if !same(l.Process.StatusCommand[0], filepath.Join(real, "status.sh")) || l.Process.StatusCommand[1] != "--json" {
		t.Errorf("status_command = %q", l.Process.StatusCommand)
	}
	if l.Process.ResumeCommand[0] != "resume-on-path" {
		t.Errorf("bare names must stay for PATH lookup: %q", l.Process.ResumeCommand[0])
	}
	if !filepath.IsAbs(l.Yield.BusyCommand[0]) {
		t.Errorf("busy_command = %q", l.Yield.BusyCommand[0])
	}
	c, _ := cfg.FallbackSet.Find("custom")
	if !filepath.IsAbs(c.Probe.Command[0]) || !filepath.IsAbs(c.Probe.CLI) || !filepath.IsAbs(c.Process.StatusCommand[0]) {
		t.Errorf("candidate paths = %q %q %q", c.Probe.Command[0], c.Probe.CLI, c.Process.StatusCommand[0])
	}
}

func TestWindowStartEqualsEndIsRejected(t *testing.T) {
	if _, err := Parse([]byte(valid + "  window:\n    tz: UTC\n    days: {Mon: [\"08:00\", \"08:00\"]}\n")); err == nil {
		t.Error("equal start and end should be rejected")
	}
}

func TestSwitchBackValidation(t *testing.T) {
	on := valid + "  switch_back: {enabled: true}\n"
	if _, err := Parse([]byte(on)); err == nil {
		t.Error("switch-back without a probe or fallbacks should be rejected")
	}
	ok := strings.Replace(valid, "  adapter: bb\n", "  adapter: bb\n  provider: claude-code\n", 1) + "  fallbacks: fallbacks.yaml\n  switch_back: {enabled: true}\n"
	cfg, err := Parse([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.Probe.Kind != "claude" || cfg.Loop.SwitchBack.MinAfter.Duration != time.Hour || cfg.Loop.SwitchBack.RetryEvery.Duration != 30*time.Minute {
		t.Errorf("defaults: %+v %+v", cfg.Loop.Probe, cfg.Loop.SwitchBack)
	}
}

func TestLimitWaitReportDefaultsAndSubscription(t *testing.T) {
	cfg, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.Errors.ReportWaitAfter.Duration != 6*time.Hour {
		t.Errorf("report_wait_after default = %v", cfg.Loop.Errors.ReportWaitAfter)
	}
	// No report section: nothing is subscribed.
	if cfg.Loop.Report.ReportsEvent("limit_wait") {
		t.Error("limit_wait subscribed without a report section")
	}
	for src, want := range map[string]bool{
		"  report: {url_env: KG_URL, secret_env: KG_SECRET}\n":                              true, // default events
		"  report: {url_env: KG_URL, secret_env: KG_SECRET, events: [all]}\n":               true,
		"  report: {url_env: KG_URL, secret_env: KG_SECRET, events: [limit_wait]}\n":        true,
		"  report: {url_env: KG_URL, secret_env: KG_SECRET, events: [await_user, stop]}\n":  false, // an explicit list must name it
		"  report: {url_env: KG_URL, secret_env: KG_SECRET, events: [handoff, complete]}\n": false,
	} {
		cfg, err := Parse([]byte(valid + src))
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if got := cfg.Loop.Report.ReportsEvent("limit_wait"); got != want {
			t.Errorf("%s: subscribed = %t, want %t", strings.TrimSpace(src), got, want)
		}
	}
	cfg, err = Parse([]byte(valid + "  errors:\n    report_wait_after: 90m\n"))
	if err != nil || cfg.Loop.Errors.ReportWaitAfter.Duration != 90*time.Minute {
		t.Fatalf("%v %v", cfg, err)
	}
	if _, err := Parse([]byte(valid + "  errors:\n    report_wait_after: -1h\n")); err == nil {
		t.Error("negative report_wait_after accepted")
	}
}
