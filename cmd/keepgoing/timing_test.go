//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
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

// status says what timing would do at the next turn boundary, from the same
// decision U5 will act on, and that nothing acts on it yet.
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
	write(t, f.cfg, string(cfgText)+timingYAML+"    steps: [primary, example-large]\n", 0o600)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := probe.UsageIdentity(cfg.Loop.PrimaryCandidate())
	if err := os.MkdirAll(filepath.Join(f.dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A preview needs enabled supervision and a legitimate recorded observation.
	write(t, filepath.Join(f.dir, "state", "control.json"), `{"enabled":true}`, 0o600)
	write(t, filepath.Join(f.dir, "state", "state.json"), `{"lastCheck":{"observedState":"idle"}}`, 0o600)
	// Read 20 minutes before 12:00 at 60%, resetting at 14:00: 40% unused.
	write(t, filepath.Join(f.dir, "state", "health.json"), `{"candidates":{"primary":{"usage":{"at":"2026-11-09T11:40:00Z","source":"claude_rate_limit_event","identity":"`+id+`","windows":[
   {"key":"seven_day","used":0.6,"resetsAt":"2026-11-09T14:00:00Z"}]}}}}`, 0o600)
	want := "timing would, at the next turn boundary: step up to example-large: the seven_day allowance resets in 2h 0m with 40% unused (reading 20m old) (not acted on yet)\n"
	code, out, errs := f.run("status", "-now", "2026-11-09T12:00:00Z")
	if code != exitOK || !strings.Contains(out, want) {
		t.Fatalf("%d %s %s\nwant %s", code, out, errs, want)
	}
	var st statusOut
	_, out, _ = f.run("status", "--json", "-now", "2026-11-09T12:00:00Z")
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.TimingWould == nil {
		t.Fatalf("%v %s", err, out)
	}
	if w := st.TimingWould; w.Action != "up" || w.To != "example-large" || w.Rule != "step_up" || w.Reason != "near_reset_unused" || w.Window != "seven_day" || w.ActedOn {
		t.Fatalf("%+v", w)
	}
	for _, tc := range []struct{ name, controls, observation, reason string }{
		{"disabled", `{}`, `{"lastCheck":{"observedState":"idle"}}`, "held"},
		{"missing controls", "", `{"lastCheck":{"observedState":"idle"}}`, "held"},
		{"missing state", `{"enabled":true}`, "", "refusal_or_unknown"},
		{"nil last check", `{"enabled":true}`, `{}`, "refusal_or_unknown"},
		{"empty observed state", `{"enabled":true}`, `{"lastCheck":{}}`, "refusal_or_unknown"},
		{"unrecognized observed state", `{"enabled":true}`, `{"lastCheck":{"observedState":"example-new-state"}}`, "refusal_or_unknown"},
		{"known error", `{"enabled":true}`, `{"lastCheck":{"observedState":"error"}}`, "in_error"},
		{"known pending", `{"enabled":true}`, `{"lastCheck":{"observedState":"pending"}}`, "turn_not_ended"},
		{"known stopping", `{"enabled":true}`, `{"lastCheck":{"observedState":"stopping"}}`, "turn_not_ended"},
		{"waiting on input", `{"enabled":true}`, `{"lastCheck":{"observedState":"active","awaitingInput":true}}`, "held"},
		{"known unknown", `{"enabled":true}`, `{"lastCheck":{"observedState":"unknown"}}`, "refusal_or_unknown"},
		{"known idle", `{"enabled":true}`, `{"lastCheck":{"observedState":"idle"}}`, "near_reset_unused"},
		{"known active at next boundary", `{"enabled":true}`, `{"lastCheck":{"observedState":"active"}}`, "near_reset_unused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, body := range map[string]string{"control.json": tc.controls, "state.json": tc.observation} {
				path := filepath.Join(f.dir, "state", name)
				if body == "" {
					if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				} else {
					write(t, path, body, 0o600)
				}
			}
			before := snapshotRuntime(t, f.dir)
			_, out, errs := f.run("status", "--json", "-now", "2026-11-09T12:00:00Z")
			var result statusOut
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err, errs)
			}
			w := result.TimingWould
			if w == nil || w.Reason != tc.reason || (w.Action == "up") != (tc.reason == "near_reset_unused") {
				t.Errorf("preview: %+v, want %s", w, tc.reason)
			}
			_, text, _ := f.run("status", "-now", "2026-11-09T12:00:00Z")
			if w != nil && !strings.Contains(text, w.Text+"\n") {
				t.Errorf("text and JSON preview differ: %s", text)
			}
			if after := snapshotRuntime(t, f.dir); !reflect.DeepEqual(before, after) || f.read("calls") != "" || f.read("probes") != "" {
				t.Fatal("preview changed records or called provider")
			}
		})
	}
	write(t, filepath.Join(f.dir, "state", "control.json"), `{"enabled":true}`, 0o600)
	write(t, filepath.Join(f.dir, "state", "state.json"), `{"lastCheck":{"observedState":"idle"}}`, 0o600)
	// A held loop: the gate decides, before any reading.
	if code, _, errs := f.run("pause"); code != exitOK {
		t.Fatalf("pause: %d %s", code, errs)
	}
	_, out, _ = f.run("status", "--json", "-now", "2026-11-09T12:00:00Z")
	st = statusOut{}
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.TimingWould == nil || st.TimingWould.Action != "stay" || st.TimingWould.Rule != "gate" || st.TimingWould.Reason != "held" {
		t.Fatalf("held: %v %+v", err, st.TimingWould)
	}
	// Without steps it only collects readings, and says so.
	write(t, filepath.Join(f.dir, "state", "control.json"), `{}`, 0o600)
	write(t, filepath.Join(f.dir, "state", "state.json"), `{}`, 0o600)
	write(t, f.cfg, string(cfgText)+timingYAML, 0o600)
	if _, out, _ := f.run("status", "-now", "2026-11-09T12:00:00Z"); !strings.Contains(out, "timing would, at the next turn boundary: stay: no steps are configured, so timing only collects readings (not acted on yet)\n") {
		t.Fatalf("no steps: %s", out)
	}
}

// Previewing never calls a provider or writes route/control records, including
// when its inputs disappear and later become readable again.
func TestTimingPreviewWithUnavailableInputs(t *testing.T) {
	f := newFixture(t)
	fbPath := filepath.Join(f.dir, "fallbacks.yaml")
	fb, _ := os.ReadFile(fbPath)
	write(t, fbPath, string(fb)+`  - id: example-large
    adapter: bb
    provider: claude-code
    group: claude-account-a
    bb: {thread: thr_primary, model: example-large}
`, 0o600)
	original, _ := os.ReadFile(f.cfg)
	own := string(original) + timingYAML + "    steps: [primary, example-large]\n"
	write(t, f.cfg, own, 0o600)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := probe.UsageIdentity(cfg.Loop.PrimaryCandidate())
	dir := filepath.Join(f.dir, "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "health.json"), `{"candidates":{"primary":{"usage":{"at":"2026-11-09T11:40:00Z","source":"claude_rate_limit_event","identity":"`+id+`","windows":[{"key":"seven_day","used":0.6,"resetsAt":"2026-11-09T14:00:00Z"}]}}}}`, 0o600)
	write(t, filepath.Join(dir, "control.json"), `{"enabled":true}`, 0o600)
	write(t, filepath.Join(dir, "state.json"), `{"lastCheck":{"observedState":"idle"}}`, 0o600)
	standardPath := filepath.Join(t.TempDir(), "routes.yaml")
	check := func(wantPreview bool, warning string, wantCode int) {
		t.Helper()
		before := snapshotRuntime(t, f.dir)
		standardBefore, beforeErr := os.ReadFile(standardPath)
		for _, args := range [][]string{{"status", "-now", "2026-11-09T12:00:00Z"}, {"status", "--json", "-now", "2026-11-09T12:00:00Z"}} {
			code, out, errs := f.run(args...)
			if code != wantCode || warning != "" && !strings.Contains(out+errs, warning) {
				t.Fatalf("%v: %d %s %s", args, code, out, errs)
			}
			if args[1] == "--json" {
				var result statusOut
				if err := json.Unmarshal([]byte(out), &result); err != nil {
					t.Fatal(err)
				}
				if (result.TimingWould != nil) != wantPreview {
					t.Fatalf("preview: %+v %s", result.TimingWould, out)
				}
			} else if strings.Contains(out, "timing would, at the next turn boundary:") != wantPreview {
				t.Fatalf("preview text: %s", out)
			}
		}
		standardAfter, afterErr := os.ReadFile(standardPath)
		if string(standardBefore) != string(standardAfter) || (beforeErr == nil) != (afterErr == nil) {
			t.Fatal("status wrote standard routes")
		}
		if after := snapshotRuntime(t, f.dir); !reflect.DeepEqual(before, after) {
			t.Fatal("status wrote records")
		}
		if f.read("calls") != "" || f.read("probes") != "" {
			t.Fatal("status called a provider")
		}
	}
	check(true, "", exitOK)
	for _, name := range []string{"control.json", "state.json"} {
		path := filepath.Join(dir, name)
		write(t, path, "broken", 0o600)
		check(false, name+" could not be read", exitFailed)
		restored := `{"enabled":true}`
		if name == "state.json" {
			restored = `{"lastCheck":{"observedState":"idle"}}`
		}
		write(t, path, restored, 0o600)
		check(true, "", exitOK)
	}
	t.Setenv(config.RoutesFileEnv, standardPath)
	standard := strings.Replace(own, "  fallbacks: fallbacks.yaml", "  routes: standard", 1)
	write(t, f.cfg, standard, 0o600)
	// Missing externally followed list, then a readable list without the
	// referenced step. Neither may become a stay decision or fail config load.
	check(false, "timing preview unavailable", exitOK)
	list := `version: 1
routes:
  - id: other-route
    provider: claude-code
    group: other-allowance
    model: example-other
    enter: same_conversation
`
	write(t, standardPath, list, 0o600)
	check(false, "timing preview unavailable", exitOK)
	// A same-allowance entry is intentionally filtered by main's standard
	// resolver; U4 must not expand it to make the timing reference work.
	write(t, standardPath, list+`  - id: example-large
    provider: claude-code
    group: claude-account-a
    model: example-large
    enter: same_conversation
`, 0o600)
	check(false, "timing preview unavailable", exitOK)
	// A valid primary-only followed configuration may preview. Missing the
	// list withholds it again; restoring the list restores that preview.
	write(t, f.cfg, strings.Replace(standard, "steps: [primary, example-large]", "steps: [primary]", 1), 0o600)
	check(true, "", exitOK)
	write(t, f.cfg, strings.Replace(standard, "    steps: [primary, example-large]\n", "", 1), 0o600)
	check(true, "", exitOK)
	if err := os.Remove(standardPath); err != nil {
		t.Fatal(err)
	}
	check(false, "timing preview unavailable", exitOK)
	write(t, standardPath, list, 0o600)
	check(true, "", exitOK)
	// A valid list with no applicable fallbacks still supports a primary-only
	// preview; no redundancy authority is inferred from that preview.
	write(t, standardPath, "version: 1\nroutes: []\n", 0o600)
	check(true, "", exitOK)
	write(t, f.cfg, own, 0o600)
	check(true, "", exitOK)
}
