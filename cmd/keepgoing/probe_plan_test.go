package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"gopkg.in/yaml.v3"
)

// This helper is the only executable used by probe-preview execution tests.
func TestProbePreviewHelper(t *testing.T) {
	marker := os.Getenv("EXAMPLE_PROBE_MARKER")
	if marker == "" {
		t.Skip("subprocess helper")
	}
	plan, err := os.ReadFile(os.Getenv("EXAMPLE_PLAN_FILE"))
	if err != nil || !bytes.Contains(plan, []byte(`"type": "probe_plan"`)) {
		os.Exit(8)
	}
	if err := os.WriteFile(marker, []byte(os.Getenv("KEEPGOING_PROBE_TIER")+":"+os.Getenv("KEEPGOING_PROBE_MODEL")), 0o600); err != nil {
		os.Exit(9)
	}
	os.Exit(0)
}

func probePreviewFixture(t *testing.T) (*doctorFixture, string, string) {
	t.Helper()
	f := newDoctorFixture(t)
	marker, plan := filepath.Join(f.root, "invoked"), filepath.Join(f.root, "plan.json")
	var candidates []map[string]any
	for _, id := range []string{"candidate-a", "disabled-a"} {
		candidates = append(candidates, map[string]any{
			"id": id, "adapter": "bb", "provider": "example-provider", "group": "example-group", "disabled": id == "disabled-a",
			"bb":    map[string]any{"thread": "thr_example_candidate", "cli": f.executable, "model": "example-target"},
			"probe": map[string]any{"kind": "command", "model": "example-cheap", "command": []string{f.executable, "-test.run=TestProbePreviewHelper"}, "env": map[string]string{"EXAMPLE_PROBE_MARKER": marker, "EXAMPLE_PLAN_FILE": plan, "EXAMPLE_SECRET": doctorPrivate}},
		})
	}
	b, err := yaml.Marshal(map[string]any{"version": 1, "probe": map[string]any{"daily_cap": 2, "scheduled_disabled": true, "timeout": "5s"}, "candidates": candidates})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, "fallbacks.yaml")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	f.loop["fallbacks"] = path
	f.save(t)
	return f, marker, plan
}

func TestProbeDryRunNeverExecutesOrCreatesState(t *testing.T) {
	for _, exact := range []bool{false, true} {
		f, marker, _ := probePreviewFixture(t)
		args := []string{"fallbacks", "check", "-c", f.config, "--dry-run", "--json"}
		if exact {
			args = append(args, "--exact", "--id", "disabled-a")
		}
		var out, errOut bytes.Buffer
		code := run(context.Background(), args, &out, &errOut)
		var p probePlan
		if code != 0 || json.Unmarshal(out.Bytes(), &p) != nil {
			t.Fatalf("%d %s %s", code, &out, &errOut)
		}
		if p.Type != "probe_plan" || p.InvocationCount != 1 || p.ScheduledDailyCap != 2 || !p.ScheduledDisabled || p.ScheduledCapApplies || p.EstimatedCostUSD != nil || p.CostStatus != "unknown" {
			t.Fatalf("%+v", p)
		}
		wantID, wantModel := "candidate-a", "example-cheap"
		if exact {
			wantID, wantModel = "disabled-a", "example-target"
		}
		c := p.Candidates[0]
		if c.ID != wantID || c.Model != wantModel || c.Disabled != exact || c.Timeout != "5s" || c.Kind != "command" {
			t.Fatalf("%+v", c)
		}
		for _, private := range []string{doctorPrivate, f.executable, marker, "EXAMPLE_SECRET", "thr_example_candidate"} {
			if strings.Contains(out.String()+errOut.String(), private) {
				t.Fatal("private execution data leaked")
			}
		}
		for _, path := range []string{marker, f.state} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("preview created %s", path)
			}
		}
	}
}

func TestProbePreviewPreservesHoldsAndCorruptHealth(t *testing.T) {
	f, marker, _ := probePreviewFixture(t)
	if err := control.Save(f.state, control.File{Enabled: true, Completed: true, Paused: true, Stop: true, Note: doctorPrivate}); err != nil {
		t.Fatal(err)
	}
	healthPath := filepath.Join(f.state, "health.json")
	if err := os.WriteFile(healthPath, []byte(doctorPrivate), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(f.state, control.FileName))
	var out bytes.Buffer
	if code := run(context.Background(), []string{"fallbacks", "check", "-c", f.config, "--dry-run"}, &out, &out); code != 0 {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), "Expected cost: unknown") || !strings.Contains(out.String(), "outside the scheduled daily cap") || strings.Contains(out.String(), doctorPrivate) {
		t.Fatal(out.String())
	}
	after, _ := os.ReadFile(filepath.Join(f.state, control.FileName))
	healthAfter, _ := os.ReadFile(healthPath)
	if !bytes.Equal(before, after) || string(healthAfter) != doctorPrivate {
		t.Fatal("preview changed runtime state")
	}
	for _, path := range []string{marker, filepath.Join(f.state, "tick.lock")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("preview executed or acquired lock")
		}
	}
}

func TestProbeExecutionPrintsPlanBeforeFakeCommand(t *testing.T) {
	f, marker, planPath := probePreviewFixture(t)
	planFile, err := os.Create(planPath)
	if err != nil {
		t.Fatal(err)
	}
	defer planFile.Close()
	var out bytes.Buffer
	code := run(context.Background(), []string{"fallbacks", "check", "-c", f.config, "--json", "--id", "candidate-a", "--exact"}, &out, planFile)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, &out)
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "exact:example-target" {
		t.Fatalf("%q %v", got, err)
	}
	var result fallbacksOut
	if json.Unmarshal(out.Bytes(), &result) != nil || !result.Redundancy.Meets {
		t.Fatalf("result contract changed: %s", &out)
	}
	planBytes, _ := os.ReadFile(planPath)
	var p probePlan
	if json.Unmarshal(planBytes, &p) != nil || p.InvocationCount != 1 || p.Candidates[0].ID != "candidate-a" {
		t.Fatal(string(planBytes))
	}
}

func TestProbeSelectionErrorsDoNotExecute(t *testing.T) {
	for _, extra := range [][]string{{"--id", "missing"}, {"candidate-a"}, {"candidate-a", "--dry-run"}} {
		f, marker, _ := probePreviewFixture(t)
		args := append([]string{"fallbacks", "check", "-c", f.config}, extra...)
		if code := run(context.Background(), args, io.Discard, io.Discard); code != exitUsage {
			t.Fatalf("%v: %d", extra, code)
		}
		for _, p := range []string{marker, f.state} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatal("invalid selection caused side effects")
			}
		}
	}
}

type failingPlanWriter struct{}

func TestProbePlanUnknownCostAndEmptySelection(t *testing.T) {
	for _, kind := range []string{"claude", "codex", "command"} {
		t.Run(kind, func(t *testing.T) {
			fb := &config.Fallbacks{Candidates: []config.Candidate{{ID: "candidate-a", Probe: config.Probe{Kind: kind}}}}
			p, selected, err := planProbes(fb, "", false)
			if err != nil || len(selected) != 1 || p.EstimatedCostUSD != nil || p.CostStatus != "unknown" {
				t.Fatalf("%+v %v", p, err)
			}
			var out bytes.Buffer
			if err := printProbePlan(&out, p, false); err != nil || !strings.Contains(out.String(), "provider default (unresolved)") {
				t.Fatalf("%s %v", &out, err)
			}
			if kind != "command" && !strings.Contains(out.String(), "subscription quota or API credit") {
				t.Fatal(out.String())
			}
			fb.Candidates[0].Disabled = true
			p, selected, err = planProbes(fb, "", false)
			if err != nil || len(selected) != 0 || p.InvocationCount != 0 || p.EstimatedCostUSD == nil || *p.EstimatedCostUSD != 0 || p.CostStatus != "no_probes" {
				t.Fatalf("%+v %v", p, err)
			}
		})
	}
}

func (failingPlanWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestProbeDoesNotRunWhenPreviewCannotBeWritten(t *testing.T) {
	f, marker, _ := probePreviewFixture(t)
	if code := run(context.Background(), []string{"fallbacks", "check", "-c", f.config}, io.Discard, failingPlanWriter{}); code != exitFailed {
		t.Fatal(code)
	}
	for _, p := range []string{marker, f.state} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("unshown plan executed")
		}
	}
}
