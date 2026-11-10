//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func TestGoalCLIPrivateMetadataAndRevisions(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "spec.json")
	write(t, input, `{"title":"PRIVATE-TITLE","objectiveRefs":["PRIVATE-OBJECTIVE"],"acceptanceRefs":["PRIVATE-ACCEPTANCE"],"authorizationRefs":["PRIVATE-AUTHORITY"],"nextAction":{"summary":"PRIVATE-NEXT"},"evidenceRefs":["PRIVATE-EVIDENCE"]}`, 0600)
	for _, cmd := range []string{"enable", "pause", "stop", "complete", "await-user"} {
		if code, _, err := f.run(cmd); code != 0 {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
	code, out, errs := f.run("goal", "create", "--file", input, "--json")
	var first control.GoalSummary
	if code != 0 || json.Unmarshal([]byte(out), &first) != nil || first.Revision != 1 {
		t.Fatalf("%s %s", out, errs)
	}
	for _, args := range [][]string{
		{"goal", "show"}, {"goal", "show", "--json"}, {"status"}, {"status", "--json"},
		{"goal", "revise", "--id", first.ID, "--revision", "1", "--file", input, "--json"},
	} {
		code, out, errs := f.run(args...)
		if code != 0 || strings.Contains(out+errs, "PRIVATE-") {
			t.Fatalf("%v: %s %s", args, out, errs)
		}
	}
	code, out, errs = f.run("goal", "show", "--private")
	var full control.Goal
	if code != 0 || json.Unmarshal([]byte(out), &full) != nil || full.ID != first.ID || len(full.Revisions) != 2 || !strings.Contains(out, "PRIVATE-TITLE") {
		t.Fatal("private view unavailable", errs)
	}
	if code, _, _ := f.run("goal", "revise", "--id", first.ID, "--revision", "1", "--file", input); code == 0 {
		t.Fatal("stale revision accepted")
	}
	if code, _, _ := f.run("goal", "create", "--file", input); code == 0 {
		t.Fatal("goal replaced")
	}
	after, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
	if string(before) != string(after) {
		t.Fatal("goal edit changed holds")
	}
	if f.read("calls") != "" || f.read("probes") != "" {
		t.Fatal("metadata commands touched provider")
	}
	history, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, "runs.jsonl"))
	if strings.Contains(string(history), "PRIVATE-") {
		t.Fatal("private goal leaked to run history")
	}
	// Completion and resume retain metadata; they do not imply goal acceptance.
	if code, _, errs := f.run("resume"); code != 0 {
		t.Fatal(errs)
	}
	code, out, errs = f.run("goal", "show", "--json")
	var current control.GoalSummary
	if code != 0 || json.Unmarshal([]byte(out), &current) != nil || current.ID != first.ID || current.Revision != 2 || current.State != "recorded" {
		t.Fatal(out, errs)
	}
}

func TestGoalCLILocksOwnershipAndCorruption(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_fixture_owner")
	cfg, _ := config.Load(f.cfg)
	input := filepath.Join(t.TempDir(), "spec.json")
	write(t, input, `{"title":"PRIVATE-TITLE","objectiveRefs":["private"],"acceptanceRefs":["private"]}`, 0600)
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	held, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := f.run("goal", "create", "--file", input)
	held.Release()
	if code != exitSkipped {
		t.Fatalf("tick lock: %d", code)
	}
	t.Setenv("BB_THREAD_ID", "thr_fixture_other")
	if code, _, _ := f.run("goal", "create", "--file", input); code == 0 {
		t.Fatal("conflicting owner accepted")
	}
	if _, err := os.Stat(filepath.Join(cfg.Loop.StateDir, control.GoalName)); !os.IsNotExist(err) {
		t.Fatal("failed create wrote goal")
	}
	t.Setenv("BB_THREAD_ID", "thr_fixture_owner")
	write(t, filepath.Join(cfg.Loop.StateDir, control.GoalName), "PRIVATE-CORRUPTION", 0600)
	for _, args := range [][]string{{"goal", "show"}, {"goal", "create", "--file", input}, {"status", "--json"}, {"status"}} {
		code, out, errs := f.run(args...)
		if strings.Contains(out+errs, "PRIVATE-") {
			t.Fatal("private error leaked")
		}
		if args[0] == "goal" && code == 0 {
			t.Fatal("corrupt goal accepted")
		}
		if args[0] == "status" && (code != 0 || !strings.Contains(out, "goal record unavailable")) {
			t.Fatal("missing status error", out, errs)
		}
	}
	// Metadata corruption does not stop the ordinary control path.
	if code, _, errs := f.run("stop"); code != 0 {
		t.Fatal(errs)
	}
}

func TestGoalCLIInvalidInputIsRedacted(t *testing.T) {
	f := newFixture(t)
	input := filepath.Join(t.TempDir(), "spec.json")
	write(t, input, `{"PRIVATE-UNKNOWN":"secret"}`, 0600)
	for _, args := range [][]string{
		{"goal", "create", "--file", input},
		{"goal", "create", "--file", input, "--private"},
		{"goal", "revise", "--file", input},
		{"goal", "show", "--file", input},
		{"goal", "create", "--file", filepath.Dir(input)},
	} {
		code, out, errs := f.run(args...)
		if code != exitUsage || strings.Contains(out+errs, "PRIVATE-") || strings.Contains(out+errs, "secret") {
			t.Fatalf("%d %s %s", code, out, errs)
		}
	}
	if f.read("calls") != "" || f.read("probes") != "" {
		t.Fatal("invalid goal called provider")
	}
}
