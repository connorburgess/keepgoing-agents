//go:build unix

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

func TestDeadlineCLIHoldResumeAndStatus(t *testing.T) {
	f := newFixture(t)
	original, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.cfg, string(original)+"  limits: {deadline: '2000-01-01T00:00:00Z'}\n", 0600)
	if code, _, err := f.run("enable"); code != 0 {
		t.Fatal(err)
	}
	if got := f.tick(); got["action"] != "await_user" {
		t.Fatalf("deadline did not hold: %v", got)
	}
	for _, cmd := range []string{"enable", "resume"} {
		if code, _, err := f.run(cmd); code != 0 {
			t.Fatal(err)
		}
		f.tick()
	}
	code, out, errs := f.run("status", "--json")
	var status statusOut
	if code != 0 || json.Unmarshal([]byte(out), &status) != nil || !status.DeadlineReached || status.Deadline != "2000-01-01T00:00:00Z" || !status.Control.AwaitingUser {
		t.Fatalf("status: %s %s", out, errs)
	}
	if f.read("calls") != "" || f.read("probes") != "" {
		t.Fatal("expired loop called providers")
	}
	// A config edit alone retains the hold; explicit resume is still needed.
	write(t, f.cfg, string(original), 0600)
	if got := f.tick(); got["reason"] != "awaiting user" {
		t.Fatalf("edit reopened: %v", got)
	}
	if code, _, err := f.run("resume"); code != 0 {
		t.Fatal(err)
	}
	if got := f.tick(); got["action"] != "wake" {
		t.Fatalf("resume did not reopen: %v", got)
	}
}

func TestInitDeadlinePreviewAndValidation(t *testing.T) {
	f := newSetupFixture(t)
	args := append(f.args(), "--deadline", "2030-01-01T00:00:00Z", "--json")
	for _, preview := range []bool{true, false} {
		invocation := append([]string{}, args...)
		if preview {
			invocation = append(invocation, "--dry-run")
		}
		code, out, errs := f.run(t, invocation, "")
		if code != 0 {
			t.Fatalf("init: %s", errs)
		}
		report := decodeInitReport(t, out)
		if report.Limits.Deadline != "2030-01-01T00:00:00Z" {
			t.Fatalf("deadline missing: %+v", report)
		}
		if !preview {
			cfg, err := config.Load(report.ConfigPath)
			if err != nil || cfg.Loop.Limits.Deadline != report.Limits.Deadline {
				t.Fatalf("config: %v %v", cfg, err)
			}
		}
	}
	bad := newSetupFixture(t)
	code, _, errs := bad.run(t, append(bad.args(), "--deadline", "tomorrow"), "")
	if code == 0 || !strings.Contains(errs, "deadline") {
		t.Fatalf("invalid deadline accepted: %s", errs)
	}
	if _, err := os.Stat(bad.destination); !os.IsNotExist(err) {
		t.Fatal("invalid setup wrote directory")
	}
}
