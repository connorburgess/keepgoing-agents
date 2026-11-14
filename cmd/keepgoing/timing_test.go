//go:build unix

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const timingYAML = "  timing:\n    enabled: true\n    windows: [seven_day]\n"

// With timing on, validate, doctor, status and the dry-run probe plan each
// say what it does, what it costs and on which model, and warn that a BB
// loop's readings come from this computer's sign-in. With it off, none does.
func TestTimingIsSaidWhereItIsTurnedOn(t *testing.T) {
	f := newFixture(t)
	collecting := "timing: collecting usage readings for the first route; it does not change routes yet\n"
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
