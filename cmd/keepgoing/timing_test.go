//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

const timingYAML = "  timing:\n    enabled: true\n    windows: [seven_day]\n"

// With timing on, validate, doctor, status and the dry-run probe plan each
// say what it does, what it costs and on which model, and warn that a BB
// loop's readings come from this computer's sign-in. With it off, none does.
func TestTimingIsSaidWhereItIsTurnedOn(t *testing.T) {
	f := newFixture(t)
	collecting := "timing: collecting usage readings for the first route; with no steps configured it does not change routes\n"
	cost := "about 14 small paid probes a week for this allowance at a 12h cadence, plus one as each named window (seven_day) nears its reset, never above probe.daily_cap (6 a day); on the provider's default model"
	bb := "timing: readings come from this computer's CLI sign-in, which may not be the account BB uses for this thread"

	for _, cmd := range [][]string{{"validate"}, {"status"}, {"fallbacks", "check", "--dry-run"}} {
		if _, out, errs := f.run(cmd...); strings.Contains(out+errs, "timing") {
			t.Fatalf("%v says timing while it is off: %s %s", cmd, out, errs)
		}
	}
	cfg, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, string(cfg)+timingYAML, 0o600)

	code, out, errs := f.run("validate")
	if code != exitOK || !strings.Contains(out, collecting) || !strings.Contains(out, "timing: "+cost+"\n") || !strings.Contains(errs, "warning: "+bb) {
		t.Fatalf("validate: %d\n%s\n%s", code, out, errs)
	}
	code, out, errs = f.run("status")
	if code != exitOK || !strings.Contains(out, collecting) || !strings.Contains(out, "timing: "+cost+"\n") || !strings.Contains(out, "warning: "+bb) {
		t.Fatalf("status: %d\n%s\n%s", code, out, errs)
	}
	var st statusOut
	_, out, _ = f.run("status", "--json")
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.Timing == nil || st.Timing.ProbesPerWeek != 14 || st.Timing.Windows[0] != "seven_day" || st.Timing.DailyCap != 6 {
		t.Fatalf("status json: %v %s", err, out)
	}
	code, out, _ = f.run("fallbacks", "check", "--dry-run")
	if code != exitOK || !strings.Contains(out, "Not in this plan, scheduled by checks: "+cost+"\n") {
		t.Fatalf("plan: %d %s", code, out)
	}
	_, out, _ = f.run("fallbacks", "check", "--dry-run", "--json")
	var plan probePlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil || plan.Timing == nil || plan.Timing.ProbeEvery != "12h0m0s" || plan.InvocationCount != 2 {
		t.Fatalf("plan json: %v %s", err, out)
	}
	_, out, _ = f.run("doctor", "-c", f.cfg, "--offline", "--json")
	var doc doctorReport
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err, out)
	}
	found := map[string]doctorCheck{}
	for _, c := range doc.Checks {
		found[c.ID] = c
	}
	if c := found["timing"]; c.Status != "ok" || !strings.Contains(c.Message, strings.TrimSuffix(collecting, "\n")) || !strings.Contains(c.Message, cost) {
		t.Fatalf("doctor timing: %+v", c)
	}
	if c := found["timing.warning.1"]; c.Status != "warning" || !strings.HasPrefix(c.Message, bb) {
		t.Fatalf("doctor warning: %+v", c)
	}
	// A check that cannot run timing is refused, naming what is missing.
	write(t, f.cfg, strings.Replace(string(cfg), "  fallbacks: fallbacks.yaml\n", "", 1)+timingYAML, 0o600)
	if code, _, errs := f.run("validate"); code == exitOK || !strings.Contains(errs, "loop.timing needs loop.fallbacks") {
		t.Fatalf("without fallbacks: %d %s", code, errs)
	}
}

// status says what timing would do at the next turn boundary, from the same
// decision and the same records a check uses.
func TestStatusSaysWhatTimingWouldDo(t *testing.T) {
	f := newFixture(t)
	bin := filepath.Join(f.dir, "bin")
	fb, _ := os.ReadFile(filepath.Join(f.dir, "fallbacks.yaml"))
	write(t, filepath.Join(f.dir, "fallbacks.yaml"), string(fb)+`  - id: example-large
    adapter: bb
    provider: claude-code
    group: claude-account-a
    bb: {thread: thr_primary, cli: `+bin+`/bb, model: example-large}
    probe: {model: claude-haiku-5-5, cli: `+bin+`/claude}
`, 0o600)
	cfgText, _ := os.ReadFile(f.cfg)
	// A step needs the first route's model, to set the thread back to it.
	cfgText = []byte(strings.Replace(string(cfgText), "  bb: {thread: thr_primary, ", "  bb: {thread: thr_primary, model: example-medium, ", 1))
	write(t, f.cfg, string(cfgText)+timingYAML+"    steps: [primary, example-large]\n", 0o600)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := probe.UsageIdentity(cfg.Loop.PrimaryCandidate())
	if err := os.MkdirAll(filepath.Join(f.dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Read 20 minutes before 12:00 at 60%, resetting at 14:00: 40% unused.
	write(t, filepath.Join(f.dir, "state", "health.json"), `{"candidates":{"primary":{"usage":{"at":"2026-11-09T11:40:00Z","source":"claude_rate_limit_event","identity":"`+id+`","windows":[
   {"key":"seven_day","used":0.6,"resetsAt":"2026-11-09T14:00:00Z"}]}}}}`, 0o600)
	want := "timing at the next turn boundary: step up to example-large: the seven_day allowance resets in 2h 0m with 40% unused (reading 20m old)\n"
	code, out, errs := f.run("status", "-now", "2026-11-09T12:00:00Z")
	if code != exitOK || !strings.Contains(out, want) {
		t.Fatalf("%d %s %s\nwant %s", code, out, errs, want)
	}
	var st statusOut
	_, out, _ = f.run("status", "--json", "-now", "2026-11-09T12:00:00Z")
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.TimingWould == nil {
		t.Fatalf("%v %s", err, out)
	}
	if w := st.TimingWould; w.Action != "up" || w.To != "example-large" || w.Rule != "step_up" || w.Reason != "near_reset_unused" || w.Window != "seven_day" {
		t.Fatalf("%+v", w)
	}
	// A held loop: the gate decides, before any reading.
	if code, _, errs := f.run("pause"); code != exitOK {
		t.Fatalf("pause: %d %s", code, errs)
	}
	_, out, _ = f.run("status", "--json", "-now", "2026-11-09T12:00:00Z")
	st = statusOut{}
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.TimingWould == nil || st.TimingWould.Action != "stay" || st.TimingWould.Rule != "gate" || st.TimingWould.Reason != "held" {
		t.Fatalf("held: %v %+v", err, st.TimingWould)
	}
	// A person's retarget: timing waits until the window current then resets,
	// and status says so.
	if code, _, errs := f.run("resume"); code != exitOK {
		t.Fatalf("resume: %d %s", code, errs)
	}
	if code, _, errs := f.run("retarget", "example-large"); code != exitOK {
		t.Fatalf("retarget: %d %s", code, errs)
	}
	saved, err := state.Load(filepath.Join(f.dir, "state"))
	if err != nil || saved.TargetSetBy != state.SetByRetarget || saved.Timing == nil || saved.Timing.Retarget == nil {
		t.Fatalf("retarget record: %v %+v", err, saved.Timing)
	}
	_, out, _ = f.run("status", "--json", "-now", "2026-11-09T12:00:00Z")
	st = statusOut{}
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.TimingWould == nil || st.TimingWould.Reason != "retargeted" {
		t.Fatalf("after a retarget: %v %+v", err, st.TimingWould)
	}
	// Without steps it only collects readings, and says so.
	write(t, f.cfg, string(cfgText)+timingYAML, 0o600)
	if _, out, _ := f.run("status", "-now", "2026-11-09T12:00:00Z"); !strings.Contains(out, "timing at the next turn boundary: stay: no steps are configured, so timing only collects readings\n") {
		t.Fatalf("no steps: %s", out)
	}
}
