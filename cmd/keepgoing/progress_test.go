//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestNoProgressCLIHoldResumeStatus(t *testing.T) {
	project := newSetupFixture(t).project
	f := newFixture(t)
	original, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(original) + fmt.Sprintf("  progress: {repositories: [%q]}\n  limits: {no_progress_timeout: 1h}\n", project)
	write(t, f.cfg, text, 0600)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errs := f.run("status", "--json")
	var st statusOut
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || st.ProgressAllowance == nil || !st.ProgressAllowance.BaselinePending {
		t.Fatalf("%s %s", out, errs)
	}
	// A historical fixture baseline lets the real CLI prove expiry without sleeping.
	repos := state.ProgressRepositories(cfg.Loop.Progress.Repositories)
	fp, err := state.Fingerprint(repos)
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.ObserveProgress(cfg.Loop.StateDir, nil, state.ProgressSources(repos), fp, time.Now().Add(-2*time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"enable", "resume"} {
		if code, _, errs := f.run(cmd); code != 0 {
			t.Fatal(errs)
		}
		if got := f.tick(); got["action"] != "await_user" {
			t.Fatalf("%v", got)
		}
	}
	if f.read("calls") != "" || f.read("probes") != "" {
		t.Fatal("expired loop touched provider")
	}
	before, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, state.ProgressName))
	code, out, errs = f.run("status", "--json")
	st = statusOut{}
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || !st.ProgressAllowance.Due || st.ProgressAllowance.ChangedAt == nil || !st.Control.AwaitingUser {
		t.Fatalf("%s %s", out, errs)
	}
	after, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, state.ProgressName))
	if string(before) != string(after) {
		t.Fatal("status wrote evidence")
	}
	// Increasing the duration retains the hold until explicit resume.
	write(t, f.cfg, strings.Replace(text, "1h", "4h", 1), 0600)
	if got := f.tick(); got["reason"] != "awaiting user" {
		t.Fatalf("%v", got)
	}
	if code, _, errs := f.run("resume"); code != 0 {
		t.Fatal(errs)
	}
	if got := f.tick(); got["action"] != "wake" {
		t.Fatalf("%v", got)
	}
	write(t, filepath.Join(cfg.Loop.StateDir, state.ProgressName), "private broken evidence", 0600)
	code, out, errs = f.run("status", "--json")
	st = statusOut{}
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || st.ProgressAllowance.Error == "" || st.ProgressAllowance.ChangedAt != nil || strings.Contains(out, "private broken evidence") {
		t.Fatalf("%s %s", out, errs)
	}
}
func TestInitNoProgressTimeout(t *testing.T) {
	f := newSetupFixture(t)
	for _, preview := range []bool{true, false} {
		args := append(f.args(), "--no-progress-timeout", "2h", "--json")
		if preview {
			args = append(args, "--dry-run")
		}
		code, out, errs := f.run(t, args, "")
		if code != 0 {
			t.Fatal(errs)
		}
		report := decodeInitReport(t, out)
		if report.Limits.NoProgressTimeout != "2h0m0s" {
			t.Fatalf("%+v", report.Limits)
		}
		if !preview {
			cfg, err := config.Load(report.ConfigPath)
			if err != nil || cfg.Loop.Limits.NoProgressTimeout.Duration != 2*time.Hour {
				t.Fatalf("%v %v", cfg, err)
			}
		}
	}
	bad := newSetupFixture(t)
	code, _, errs := bad.run(t, append(bad.args(), "--no-progress-timeout", "-1h"), "")
	if code == 0 || !strings.Contains(errs, "no_progress_timeout") {
		t.Fatal(errs)
	}
	if _, err := os.Stat(bad.destination); !os.IsNotExist(err) {
		t.Fatal("invalid setup wrote files")
	}
}
