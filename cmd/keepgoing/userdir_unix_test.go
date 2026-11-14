//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Doctor through the command, for this platform's real places: a new
// install, and then directories of both names.
func TestDoctorUserDirectoryThroughTheCommand(t *testing.T) {
	f := newFixture(t)
	config, state := userHome(t)
	found := func() doctorCheck {
		t.Helper()
		_, out, _ := f.run("doctor", "--json")
		var v struct {
			Checks []doctorCheck `json:"checks"`
		}
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		for _, c := range v.Checks {
			if c.ID == "user.directory" {
				return c
			}
		}
		t.Fatalf("no user.directory check: %s", out)
		return doctorCheck{}
	}
	if c := found(); c.Status != "ok" || !strings.Contains(c.Message, "named looptender") {
		t.Fatalf("%+v", c)
	}
	if err := os.MkdirAll(filepath.Join(state, "looptender", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(config, "keepgoing", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	if c := found(); c.Status != "warning" || !strings.Contains(c.Message, filepath.Join(config, "keepgoing")) || !strings.Contains(c.Message, filepath.Join(state, "looptender")) {
		t.Fatalf("%+v", c)
	}
}

// The panel's default directory follows the same rule, decided from both
// places: an earlier directory under the configuration directory keeps the
// panel's files under the earlier name in the state directory.
func TestPanelDefaultDirectoryFollowsTheEarlierName(t *testing.T) {
	configBase, stateBase := panelShortDir(t), panelShortDir(t)
	prevState, prevLegacy := panelStateBase, panelLegacyBase
	panelStateBase = func() (string, error) { return stateBase, nil }
	panelLegacyBase = func() (string, error) { return configBase, nil }
	t.Cleanup(func() { panelStateBase, panelLegacyBase = prevState, prevLegacy })
	want := func(name string) string {
		t.Helper()
		p, err := canonicalFuturePath(filepath.Join(stateBase, name, "panel"))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if dir, err := panelDirectory(""); err != nil || dir != want("looptender") {
		t.Fatalf("a new install: %q %v", dir, err)
	}
	if _, found := panelLegacy(); found {
		t.Fatal("a new install has no earlier build's panel")
	}
	// The registry of an install from before the rename, and nothing else.
	if err := os.MkdirAll(filepath.Join(configBase, "keepgoing", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	if dir, err := panelDirectory(""); err != nil || dir != want("keepgoing") {
		t.Fatalf("an earlier directory: %q %v", dir, err)
	}
}
