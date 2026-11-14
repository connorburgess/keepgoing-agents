package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

const timingValid = valid + `  provider: claude-code
  fallbacks: fallbacks.yaml
  timing:
    enabled: true
    windows: [seven_day]
`

func TestTimingDefaultsAndOffByDefault(t *testing.T) {
	cfg, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.TimingOn() || cfg.TimingSummary() != nil {
		t.Fatal("timing is on without being configured")
	}
	cfg, err = Parse([]byte(timingValid))
	if err != nil {
		t.Fatal(err)
	}
	tm := cfg.Loop.Timing
	if !cfg.Loop.TimingOn() || tm.NearReset.Duration != 6*time.Hour || tm.ProbeEvery.Duration != 12*time.Hour {
		t.Fatalf("defaults: %+v", tm)
	}
	// Present but not enabled needs nothing else and does nothing.
	off := valid + "  timing:\n    enabled: false\n"
	if cfg, err := Parse([]byte(off)); err != nil || cfg.Loop.TimingOn() {
		t.Fatalf("%v %+v", err, cfg)
	}
}

// Timing refuses what it cannot run, naming what is missing.
func TestTimingRejects(t *testing.T) {
	for name, c := range map[string]struct{ yaml, want string }{
		"no fallbacks":          {strings.Replace(timingValid, "  fallbacks: fallbacks.yaml\n", "", 1), "loop.timing needs loop.fallbacks"},
		"no way to probe":       {strings.Replace(timingValid, "  provider: claude-code\n", "", 1), "loop.timing needs a way to probe the first route"},
		"no windows":            {strings.Replace(timingValid, "    windows: [seven_day]\n", "", 1), "loop.timing.windows must name at least one usage window"},
		"bad window key":        {strings.Replace(timingValid, "[seven_day]", "[\"seven day\"]", 1), "is not a window key"},
		"window listed twice":   {strings.Replace(timingValid, "[seven_day]", "[seven_day, seven_day]", 1), "is listed twice"},
		"near_reset too short":  {timingValid + "    near_reset: 30s\n", "loop.timing.near_reset must be between 1m and 168h"},
		"near_reset too long":   {timingValid + "    near_reset: 200h\n", "loop.timing.near_reset must be between 1m and 168h"},
		"probe_every too short": {timingValid + "    probe_every: 30m\n", "loop.timing.probe_every must be at least 1h"},
		"unknown field":         {timingValid + "    ceiling: high\n", "ceiling"},
	} {
		_, err := Parse([]byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// What validate, doctor, status and the probe plan say, word for word.
func TestTimingSummaryWords(t *testing.T) {
	cfg, err := Parse([]byte(strings.Replace(timingValid, "    thread: thr_example123\n", "    thread: thr_example123\n    model: example-large\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	cfg.FallbackSet = &Fallbacks{Probe: ProbePolicy{DailyCap: 6}}
	cfg.Loop.Probe.Model = "example-small"
	s := cfg.TimingSummary()
	if s.Collecting != "timing: collecting usage readings for the first route (example-large); with no steps configured it does not change routes" {
		t.Errorf("collecting: %q", s.Collecting)
	}
	cfg.Loop.Timing.Steps, cfg.Loop.Timing.Ceiling = []string{"primary", "example-xl"}, "example-xl"
	if s := cfg.TimingSummary(); s.Collecting != "timing: collecting usage readings for the first route (example-large) and moving between its steps (primary, example-xl) up to example-xl at turn boundaries" {
		t.Errorf("with steps: %q", s.Collecting)
	}
	cfg.Loop.Timing.Steps, cfg.Loop.Timing.Ceiling = nil, ""
	want := "timing: about 14 small paid probes a week for this allowance at a 12h cadence, plus one as each named window (seven_day) nears its reset, never above probe.daily_cap (6 a day); on example-small, the probe model: readings cover the windows that model reports, and a window for example-large alone is read only when the probe model is the route's own model"
	if s.Cost != want {
		t.Errorf("cost:\n got  %q\n want %q", s.Cost, want)
	}
	if len(s.Warnings) != 1 || s.Warnings[0] != "timing: readings come from this computer's CLI sign-in, which may not be the account BB uses for this thread" {
		t.Errorf("warnings: %q", s.Warnings)
	}
	// The probe on the route's own model reads its model's window too.
	cfg.Loop.Probe.Model = ""
	if s := cfg.TimingSummary(); !strings.HasSuffix(s.Cost, "; on example-large, the route's own model, so a window for that model alone is read too") || s.ProbesPerWeek != 14 {
		t.Errorf("own model: %q", s.Cost)
	}
	// Every 6 h is 28 a week; scheduled probes switched off is said.
	cfg.Loop.Timing.ProbeEvery = Duration{Duration: 6 * time.Hour}
	cfg.FallbackSet.Probe.Disabled = true
	if s := cfg.TimingSummary(); !strings.Contains(s.Cost, "about 28 small paid probes a week") || !strings.Contains(s.Cost, "at a 6h cadence") || len(s.Warnings) != 2 ||
		s.Warnings[1] != "timing: scheduled probes are off in the fallbacks file (probe.scheduled_disabled), so no timing probe runs" {
		t.Errorf("6h, disabled: %q %q", s.Cost, s.Warnings)
	}
}

// Steps are routes on the first route's allowance, checked against the
// fallbacks once both files are read.
func TestTimingSteps(t *testing.T) {
	dir := t.TempDir()
	fallbacks := `version: 1
candidates:
  - id: example-large
    adapter: bb
    provider: claude-code
    group: claude-account-a
    bb: {thread: thr_example123, model: example-large}
    probe: {model: example-small}
  - id: example-off
    adapter: bb
    provider: claude-code
    group: claude-account-a
    disabled: true
    bb: {thread: thr_example123, model: example-medium}
  - id: other-account
    adapter: bb
    provider: claude-code
    group: claude-account-a
    bb: {thread: thr_example456, model: example-large}
  - id: other-group
    adapter: bb
    provider: claude-code
    group: claude-account-b
    bb: {thread: thr_example123, model: example-large}
  - id: new-conversation
    adapter: bb
    provider: claude-code
    group: claude-account-a
    bb: {new_thread: true, model: example-large}
  - id: no-change
    adapter: bb
    provider: claude-code
    group: claude-account-a
    bb: {thread: thr_example123}
  - id: higher-effort
    adapter: bb
    provider: claude-code
    group: claude-account-a
    bb: {thread: thr_example123, model: example-large, reasoning: high}
`
	write := func(name, body string) string {
		p := dir + "/" + name
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("fallbacks.yaml", fallbacks)
	loop := strings.Replace(timingValid, "  provider: claude-code\n", "  provider: claude-code\n  group: claude-account-a\n", 1)
	loop = strings.Replace(loop, "    thread: thr_example123\n", "    thread: thr_example123\n    model: example-medium\n", 1)
	load := func(extra string) (*Config, error) { return Load(write("keepgoing.yaml", loop+extra)) }

	cfg, err := load("    steps: [primary, example-large]\n")
	if err != nil {
		t.Fatal(err)
	}
	tm := cfg.Loop.Timing
	if tm.Ceiling != "example-large" || tm.UnusedAtLeast != 0.25 || tm.DownAt != 0.95 || tm.MinOnStep.Duration != 2*time.Hour {
		t.Fatalf("defaults: %+v", tm)
	}
	for name, c := range map[string]struct{ extra, want string }{
		"another account":   {"    steps: [primary, other-group]\n", `"other-group" is on account group "claude-account-b", not the first route's "claude-account-a"`},
		"another thread":    {"    steps: [primary, other-account]\n", `"other-account" is not on the loop's own BB thread; a step must never start a new conversation`},
		"new conversation":  {"    steps: [primary, new-conversation]\n", `"new-conversation" is not on the loop's own BB thread`},
		"changes nothing":   {"    steps: [primary, no-change]\n", `"no-change" sets neither bb.model nor bb.reasoning`},
		"effort to restore": {"    steps: [primary, higher-effort]\n", "loop.bb.reasoning is needed too"},
		"switched off":      {"    steps: [primary, example-off]\n", `"example-off" is switched off`},
		"unknown":           {"    steps: [primary, example-missing]\n", `"example-missing" is not a fallback`},
		"no primary":        {"    steps: [example-large]\n", "must include primary"},
		"listed twice":      {"    steps: [primary, primary]\n", "is listed twice"},
		"ceiling elsewhere": {"    steps: [primary, example-large]\n    ceiling: other-account\n", "is not one of loop.timing.steps"},
		"ceiling below":     {"    steps: [example-large, primary]\n    ceiling: example-large\n", "is below primary"},
		"ceiling, no steps": {"    ceiling: example-large\n", "needs loop.timing.steps"},
		"unused 0":          {"    unused_at_least: 1\n", "unused_at_least must be above 0 and below 1"},
		"down_at over 1":    {"    down_at: 1.5\n", "down_at must be above 0 and at most 1"},
		"min_on_step short": {"    min_on_step: 30s\n", "min_on_step must be between 1m and 168h"},
	} {
		if _, err := load(c.extra); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A step must never start a new conversation, and primary must name what a
// return sets the thread back to.
func TestTimingStepsNeedTheLoopsOwnBBThread(t *testing.T) {
	dir := t.TempDir()
	fb := "version: 1\ncandidates:\n  - id: example-large\n    adapter: bb\n    provider: claude-code\n    group: claude-account-a\n    bb: {thread: thr_example123, model: example-large}\n"
	if err := os.WriteFile(dir+"/fallbacks.yaml", []byte(fb), 0o600); err != nil {
		t.Fatal(err)
	}
	base := strings.Replace(timingValid, "  provider: claude-code\n", "  provider: claude-code\n  group: claude-account-a\n", 1) + "    steps: [primary, example-large]\n"
	headless := strings.Replace(strings.Replace(base, "adapter: bb", "adapter: claude", 1), "  bb:\n    thread: thr_example123\n", "  claude: {workdir: "+dir+", model: example-medium}\n", 1)
	for name, c := range map[string]struct{ yaml, want string }{
		"headless loop":     {headless, "loop.timing.steps needs a loop on a BB thread for now: a step must never start a new conversation"},
		"no model on first": {base, "loop.timing.steps needs loop.bb.model"},
	} {
		if err := os.WriteFile(dir+"/keepgoing.yaml", []byte(c.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir + "/keepgoing.yaml"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
