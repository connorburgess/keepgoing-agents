//go:build unix

package main

import (
	"encoding/json"
	"fmt"
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
				for _, want := range []string{"loop:      example-loop", tc.line, "unreadable records: " + tc.file + ";"} {
					if !strings.Contains(out, want) {
						t.Errorf("stdout does not show %q:\n%s", want, out)
					}
				}
				// What could be read is still shown: the other record.
				if tc.file == "state.json" && !strings.Contains(out, "control:   enabled") {
					t.Errorf("the readable control record is not shown:\n%s", out)
				}
				if tc.file == "control.json" && (!strings.Contains(out, "last check:") || strings.Contains(out, "last check: unavailable")) {
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
					Target       string
					ControlState string
					Unreadable   []struct{ File, Problem, Remedy string }
				}
				if err := json.Unmarshal([]byte(out), &v); err != nil || code != exitFailed {
					t.Fatalf("status --json: exit %d %v\n%s", code, err, out)
				}
				if len(v.Unreadable) != 1 || v.Unreadable[0].File != tc.file || v.Unreadable[0].Problem != "not_json" || !strings.Contains(v.Unreadable[0].Remedy, tc.remedy) {
					t.Errorf("unreadable: %+v", v.Unreadable)
				}
				if tc.file == "control.json" && v.ControlState != "unavailable" {
					t.Errorf("controlState %q, want unavailable", v.ControlState)
				}
				if tc.file == "state.json" && v.Target != "unavailable" {
					t.Errorf("target %q, want unavailable", v.Target)
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
	if code != exitFailed || !strings.Contains(errs, "control.json could not be read") || !strings.Contains(errs, "state.json could not be read") || !strings.Contains(out, "unreadable records: control.json, state.json;") || !strings.Contains(out, "loop:      example-loop") {
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

// A record keepgoing cannot open may be intact: status says so, and does
// not tell anyone to move it aside.
func TestStatusOnARecordItCannotOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a file whatever its permissions")
	}
	for _, file := range []string{"control.json", "state.json"} {
		t.Run(file, func(t *testing.T) {
			f := newFixture(t)
			f.run("enable")
			f.tick()
			path := filepath.Join(f.dir, "state", file)
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(path, 0o600) })
			code, _, errs := f.run("status")
			if code != exitFailed || !strings.Contains(errs, file+" could not be read (it could not be opened)") || !strings.Contains(errs, "may be intact") || !strings.Contains(errs, "do not move it aside") || strings.Contains(errs, "from a copy") {
				t.Fatalf("exit %d: %s", code, errs)
			}
		})
	}
}

// A stopped and paused loop whose control.json is cut short: neither the
// text nor the JSON shows its empty record as the loop's controls.
func TestStatusDoesNotShowAnEmptyControlRecord(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.run("pause")
	f.run("stop")
	f.tick()
	path := filepath.Join(f.dir, "state", "control.json")
	b, _ := os.ReadFile(path)
	os.WriteFile(path, b[:len(b)/2], 0o600)
	_, out, _ := f.run("status", "--json")
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["control"]; ok {
		t.Fatalf("control is shown: %s", m["control"])
	}
	_, text, _ := f.run("status")
	if strings.Contains(text, "disabled") || !strings.Contains(text, "control:   unavailable") || !strings.Contains(text, "progress flag: unavailable: control.json could not be read") {
		t.Fatalf("status:\n%s", text)
	}
}

// With state.json cut short, nothing from it is shown as the loop's: not the
// primary's thread as the target, not "never" for the last check and send.
func TestStatusDoesNotShowAnEmptyStateRecord(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	path := filepath.Join(f.dir, "state", "state.json")
	b, _ := os.ReadFile(path)
	os.WriteFile(path, b[:len(b)/2], 0o600)
	_, text, _ := f.run("status")
	for _, bad := range []string{"thread:    thr_primary", "last check: never", "last sent: never", "account:   quota group", "target configuration: missing"} {
		if strings.Contains(text, bad) {
			t.Errorf("status shows %q:\n%s", bad, text)
		}
	}
	for _, want := range []string{"state:     unavailable", "last check: unavailable", "activity:  unavailable", "progress flag: unavailable: state.json could not be read"} {
		if !strings.Contains(text, want) {
			t.Errorf("status does not show %q:\n%s", want, text)
		}
	}
}

// A loop with a progress repository and a lane stop that was carried out:
// with either record unreadable, the no-progress flag and the lane-stop
// line say they are unavailable instead of computing from an empty record.
func TestStatusProgressFlagAndLaneStopWithAnUnreadableRecord(t *testing.T) {
	for _, file := range []string{"control.json", "state.json"} {
		t.Run(file, func(t *testing.T) {
			f := laneFixture(t)
			project := t.TempDir()
			original, _ := os.ReadFile(f.cfg)
			write(t, f.cfg, string(original)+fmt.Sprintf("  progress: {repositories: [%q]}\n", project), 0o600)
			if code, out, errs := f.run("stop", "--lanes", "--immediately"); code != exitOK {
				t.Fatalf("%d %s%s", code, out, errs)
			}
			if _, text, _ := f.run("status"); !strings.Contains(text, "lane stop: at the check of") {
				t.Fatalf("the lane stop was not carried out:\n%s", text)
			}
			path := filepath.Join(f.dir, "state", file)
			b, _ := os.ReadFile(path)
			os.WriteFile(path, b[:len(b)/2], 0o600)
			_, text, _ := f.run("status")
			want := map[string]string{
				"control.json": "lane stop: unavailable (control.json could not be read)",
				"state.json":   "whether it was carried out is unavailable (state.json could not be read)",
			}[file]
			if !strings.Contains(text, want) || strings.Contains(text, "the check that carries out the stop also stops the lanes") || strings.Contains(text, "lane stop: at the check of") {
				t.Errorf("lane stop line:\n%s", text)
			}
			if !strings.Contains(text, "progress flag: unavailable: "+file+" could not be read") || strings.Contains(text, "the loop is off") || strings.Contains(text, "no check has recorded") {
				t.Errorf("progress flag line:\n%s", text)
			}
		})
	}
}

// A damaged sends.json is the send record, not an unreadable record: status
// says so and exits 0. Beside an unreadable control.json, the last line
// names both.
func TestStatusReportsAnUnreadableSendRecord(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	dir := filepath.Join(f.dir, "state")
	os.WriteFile(filepath.Join(dir, "sends.json"), []byte("{"), 0o600)
	code, out, _ := f.run("status", "--json")
	var v struct {
		SendRecord string
		Unreadable []struct{ File string }
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || !strings.HasPrefix(v.SendRecord, "sends.json cannot be read") || len(v.Unreadable) != 0 {
		t.Fatalf("exit %d %v %+v", code, err, v)
	}
	if code, text, _ := f.run("status"); code != 0 || !strings.Contains(text, "send record: sends.json cannot be read") || strings.Contains(text, "unreadable records:") {
		t.Fatalf("exit %d:\n%s", code, text)
	}
	os.WriteFile(filepath.Join(dir, "control.json"), []byte("{"), 0o600)
	if code, text, _ := f.run("status"); code != exitFailed || !strings.Contains(text, "unreadable records: control.json, sends.json;") {
		t.Fatalf("exit %d:\n%s", code, text)
	}
}
