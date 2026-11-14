package config

import (
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
	if s.Collecting != "timing: collecting usage readings for the first route (example-large); it does not change routes yet" {
		t.Errorf("collecting: %q", s.Collecting)
	}
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
