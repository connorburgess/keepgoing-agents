//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// status on a loop with a damaged control.json or state.json names the
// file, says what to do, and prints what it can, and exits 1. The damaged
// file is not touched.
func TestStatusNamesAnUnreadableRecord(t *testing.T) {
	for _, tc := range []struct {
		file, remedy, line string
	}{
		{"control.json", "restore control.json from a copy", "control:   unavailable"},
		{"state.json", "restore state.json from a copy", "target:    unavailable"},
	} {
		for name, damage := range map[string]string{"empty": "", "truncated": "{\"enabled\": tr", "garbage": "\x00not json"} {
			t.Run(tc.file+"/"+name, func(t *testing.T) {
				f := newFixture(t)
				if code, _, errs := f.run("enable"); code != 0 {
					t.Fatal(errs)
				}
				f.tick()
				path := filepath.Join(f.dir, "state", tc.file)
				if err := os.WriteFile(path, []byte(damage), 0o600); err != nil {
					t.Fatal(err)
				}
				code, out, errs := f.run("status")
				if code != exitFailed {
					t.Fatalf("exit %d, want %d", code, exitFailed)
				}
				for _, want := range []string{tc.file + " could not be read", tc.remedy, "Stop the daemon, service or scheduler"} {
					if !strings.Contains(errs, want) {
						t.Errorf("stderr does not say %q:\n%s", want, errs)
					}
				}
				for _, want := range []string{"loop:      example-loop", tc.line, "unreadable records: 1"} {
					if !strings.Contains(out, want) {
						t.Errorf("stdout does not show %q:\n%s", want, out)
					}
				}
				// What could be read is still shown: the other record.
				if tc.file == "state.json" && !strings.Contains(out, "control:   enabled") {
					t.Errorf("the readable control record is not shown:\n%s", out)
				}
				if tc.file == "control.json" && !strings.Contains(out, "last check:") {
					t.Errorf("the readable state record is not shown:\n%s", out)
				}
				if tc.file == "control.json" && (strings.Contains(out, "disabled") || !strings.Contains(out, "activity:  unavailable")) {
					t.Errorf("an unreadable control record is shown as a state:\n%s", out)
				}
				if b, _ := os.ReadFile(path); string(b) != damage {
					t.Errorf("status changed %s", tc.file)
				}

				code, out, _ = f.run("status", "--json")
				var v struct {
					ControlState string
					Unreadable   []struct{ File, Problem, Remedy string }
				}
				if err := json.Unmarshal([]byte(out), &v); err != nil || code != exitFailed {
					t.Fatalf("status --json: exit %d %v\n%s", code, err, out)
				}
				if len(v.Unreadable) != 1 || v.Unreadable[0].File != tc.file || v.Unreadable[0].Problem == "" || !strings.Contains(v.Unreadable[0].Remedy, tc.remedy) {
					t.Errorf("unreadable: %+v", v.Unreadable)
				}
				if tc.file == "control.json" && v.ControlState != "unavailable" {
					t.Errorf("controlState %q, want unavailable", v.ControlState)
				}
			})
		}
	}
}

// Both records unreadable: both are named, and status still prints the loop.
func TestStatusNamesEveryUnreadableRecord(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	for _, name := range []string{"control.json", "state.json"} {
		os.WriteFile(filepath.Join(f.dir, "state", name), []byte("{"), 0o600)
	}
	code, out, errs := f.run("status")
	if code != exitFailed || !strings.Contains(errs, "control.json could not be read") || !strings.Contains(errs, "state.json could not be read") || !strings.Contains(out, "unreadable records: 2") || !strings.Contains(out, "loop:      example-loop") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
}

// A loop whose records are readable reports none and exits 0, as before.
func TestStatusWithReadableRecordsReportsNone(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	code, out, errs := f.run("status", "--json")
	if code != 0 || strings.Contains(out, `"unreadable"`) || errs != "" {
		t.Fatalf("exit %d %s\n%s", code, errs, out)
	}
}
