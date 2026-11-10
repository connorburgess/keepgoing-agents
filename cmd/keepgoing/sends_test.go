//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestSendLimitCLIResumeAndStatus(t *testing.T) {
	f := newFixture(t)
	original, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.cfg, string(original)+"  limits: {max_sends: 1}\n", 0600)
	if code, _, err := f.run("enable"); code != 0 {
		t.Fatal(err)
	}
	if got := f.tick(); got["action"] != "wake" {
		t.Fatalf("%v", got)
	}
	calls := f.read("calls")
	probes := f.read("probes")
	for _, cmd := range []string{"resume", "disable", "enable", "resume"} {
		if code, _, err := f.run(cmd); code != 0 {
			t.Fatal(err)
		}
		f.tick()
	}
	if f.read("calls") != calls || f.read("probes") != probes {
		t.Fatal("exhausted loop called provider")
	}
	code, out, errs := f.run("status", "--json")
	var st statusOut
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || st.SendAllowance == nil || *st.SendAllowance.Used != 1 || *st.SendAllowance.Remaining != 0 || !st.SendAllowance.Reached || !st.Control.AwaitingUser {
		t.Fatalf("%s %s", out, errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(cfg.Loop.StateDir, state.SendsName)
	write(t, ledger, "private broken data", 0600)
	code, out, errs = f.run("status", "--json")
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || st.SendAllowance.Error == "" || strings.Contains(out, "private broken data") {
		t.Fatalf("%s %s", out, errs)
	}
	// Unavailable counters are omitted, not advertised as an unused allowance.
	var raw map[string]any
	json.Unmarshal([]byte(out), &raw)
	if _, ok := raw["sendAllowance"].(map[string]any)["used"]; ok {
		t.Fatal("unknown shown as known")
	}
}

func TestInitMaxSendsPreviewAndValidation(t *testing.T) {
	f := newSetupFixture(t)
	args := append(f.args(), "--max-sends", "8", "--json")
	for _, preview := range []bool{true, false} {
		invocation := append([]string{}, args...)
		if preview {
			invocation = append(invocation, "--dry-run")
		}
		code, out, errs := f.run(t, invocation, "")
		if code != 0 {
			t.Fatal(errs)
		}
		report := decodeInitReport(t, out)
		if report.Limits.MaxSends != 8 {
			t.Fatalf("%+v", report.Limits)
		}
		if !preview {
			cfg, err := config.Load(report.ConfigPath)
			if err != nil || cfg.Loop.Limits.MaxSends != 8 {
				t.Fatalf("%+v %v", cfg, err)
			}
		}
	}
	bad := newSetupFixture(t)
	code, _, errs := bad.run(t, append(bad.args(), "--max-sends", "-1"), "")
	if code == 0 || !strings.Contains(errs, "max_sends") {
		t.Fatal(errs)
	}
	if _, err := os.Stat(bad.destination); !os.IsNotExist(err) {
		t.Fatal("invalid setup wrote files")
	}
}
