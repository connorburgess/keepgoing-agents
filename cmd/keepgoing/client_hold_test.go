package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
)

func TestClientHoldPreservesStateDuringTickAndTransfer(t *testing.T) {
	for _, op := range []string{"pause", "stop"} {
		t.Run(op, func(t *testing.T) {
			dir, entry := clientFixture(t)
			t.Setenv("BB_THREAD_ID", "thr_other_caller")
			original := control.File{Enabled: false, AwaitingUser: true, Completed: true, Note: "PRIVATE-NOTE", BBTarget: "thr_other_target", Ownership: &control.Ownership{ThreadID: "thr_example_owner", Generation: 3}, PendingTransfer: &control.PendingBBTransfer{}}
			if err := control.Save(entry.StateDir, original); err != nil {
				t.Fatal(err)
			}
			original, _ = control.Load(entry.StateDir)
			held, err := lock.Acquire(entry.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			for i := 0; i < 2; i++ {
				r, code := applyClientHold(context.Background(), dir, entry.ID, op)
				if code != 0 || r.ControlSave != "confirmed" {
					t.Fatal(code, r)
				}
				raw, _ := json.Marshal(r)
				for _, secret := range []string{"PRIVATE", entry.ConfigPath, entry.StateDir, "thr_example_owner", "thr_other_target"} {
					if strings.Contains(string(raw), secret) {
						t.Fatal("private output", string(raw))
					}
				}
			}
			got, err := control.Load(entry.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			if op == "pause" {
				original.Paused = true
			} else {
				original.Stop = true
			}
			original.UpdatedAt = got.UpdatedAt
			if !reflect.DeepEqual(original, got) {
				t.Fatalf("hold changed other control fields: %+v", got)
			}
			j, err := incident.Load(entry.StateDir)
			if err != nil || len(j.Records) != 2 || j.Records[0].ControlChange.Operation != op {
				t.Fatal(j, err)
			}
			// The fixture provider and task path do not exist. A hold needs neither.
		})
	}
}

func TestClientHoldMissingControlStaysDisabled(t *testing.T) {
	dir, entry := clientFixture(t)
	r, code := applyClientHold(context.Background(), dir, entry.ID, "stop")
	if code != 0 || r.Controls.Enabled || !r.Controls.Stop {
		t.Fatal(code, r)
	}
	f, err := control.Load(entry.StateDir)
	if err != nil || f.Enabled || !f.Stop || f.Ownership != nil {
		t.Fatal(f, err)
	}
}

func TestClientHoldRefusalsDoNotChangeControls(t *testing.T) {
	for _, scenario := range []string{"removed", "renamed", "runtime_changed", "config_missing", "control_null", "control_corrupt", "control_large", "cancelled", "registry_busy", "control_busy"} {
		t.Run(scenario, func(t *testing.T) {
			dir, entry := clientFixture(t)
			if err := control.Save(entry.StateDir, control.File{Enabled: true, Note: "PRIVATE"}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := filepath.Join(entry.StateDir, control.FileName)
			wantCode := exitFailed
			switch scenario {
			case "removed":
				if err := registry.Remove(dir, entry.ID); err != nil {
					t.Fatal(err)
				}
			case "renamed", "runtime_changed":
				b, err := os.ReadFile(entry.ConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				old, next := "name: example-loop", "name: different"
				if scenario == "runtime_changed" {
					old, next = "state_dir: state", "state_dir: other"
				}
				if err := os.WriteFile(entry.ConfigPath, []byte(strings.Replace(string(b), old, next, 1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "config_missing":
				if err := os.Remove(entry.ConfigPath); err != nil {
					t.Fatal(err)
				}
			case "control_null", "control_corrupt", "control_large":
				body := "null"
				if scenario == "control_corrupt" {
					body = `{"note":"PRIVATE"`
				}
				if scenario == "control_large" {
					body = strings.Repeat(" ", (1<<20)+1)
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			case "registry_busy", "control_busy":
				lockDir, name := dir, registry.LockName
				if scenario == "control_busy" {
					lockDir, name = entry.StateDir, lock.Control
				}
				held, err := lock.AcquireNamed(lockDir, name)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Release()
				wantCode = exitSkipped
			}
			before, _ := os.ReadFile(path)
			r, code := applyClientHold(ctx, dir, entry.ID, "pause")
			if code != wantCode || r.ControlSave != "not_attempted" || r.Controls != nil {
				t.Fatal(code, r)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("refused hold changed controls")
			}
			if _, err := os.Stat(filepath.Join(entry.StateDir, incident.FileName)); !os.IsNotExist(err) {
				t.Fatal("refused hold wrote history")
			}
		})
	}
}

func TestClientHoldDiagnosticFailuresKeepConfirmedSave(t *testing.T) {
	for _, failure := range []string{"incident", "runs"} {
		t.Run(failure, func(t *testing.T) {
			dir, entry := clientFixture(t)
			if err := os.Mkdir(entry.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			if failure == "incident" {
				if err := os.WriteFile(filepath.Join(entry.StateDir, incident.FileName), []byte("null"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(filepath.Join(entry.StateDir, "runs.jsonl"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			r, code := applyClientHold(context.Background(), dir, entry.ID, "pause")
			if code != exitFailed || r.ControlSave != "confirmed" || !r.Controls.Paused || len(r.Issues) != 1 {
				t.Fatal(code, r)
			}
			f, err := control.Load(entry.StateDir)
			if err != nil || !f.Paused {
				t.Fatal(f, err)
			}
		})
	}
}

func TestClientHoldCLI(t *testing.T) {
	dir, entry := clientFixture(t)
	var out, errs bytes.Buffer
	code := run(context.Background(), []string{"client", "stop", "--id", entry.ID, "--directory", dir, "--json"}, &out, &errs)
	var r clientHoldReport
	if code != 0 || errs.Len() != 0 || json.Unmarshal(out.Bytes(), &r) != nil || r.SchemaVersion != 1 || r.ControlSave != "confirmed" {
		t.Fatal(code, out.String(), errs.String())
	}
	for _, args := range [][]string{{"client", "resume"}, {"client", "pause"}, {"client", "stop", "--id", "PRIVATE"}, {"client", "pause", "--PRIVATE"}, {"client", "pause", "--id", entry.ID, "--take-over"}} {
		out.Reset()
		errs.Reset()
		code = run(context.Background(), args, &out, &errs)
		if code != exitUsage || out.Len() != 0 || strings.Contains(errs.String(), "PRIVATE") {
			t.Fatal(code, out.String(), errs.String())
		}
	}
}

type lostClientHoldOutput struct{}

func (lostClientHoldOutput) Write([]byte) (int, error) {
	return 0, errors.New("private output failure")
}

func TestClientHoldLostResponseKeepsSave(t *testing.T) {
	dir, entry := clientFixture(t)
	var errs bytes.Buffer
	code := run(context.Background(), []string{"client", "pause", "--id", entry.ID, "--directory", dir, "--json"}, lostClientHoldOutput{}, &errs)
	if code != exitFailed || errs.Len() != 0 {
		t.Fatal(code, errs.String())
	}
	ctl, err := control.Load(entry.StateDir)
	if err != nil || !ctl.Paused || ctl.Enabled {
		t.Fatal(ctl, err)
	}
	// A fresh status is how a caller reconciles the lost response; it does not
	// replay the mutation or append another incident.
	r := clientRead(t, dir, entry, false)
	if r.Outcome != "available" || !r.Snapshot.Controls.Paused {
		t.Fatal(r)
	}
	j, err := incident.Load(entry.StateDir)
	if err != nil || len(j.Records) != 1 {
		t.Fatal(j, err)
	}
}

// A hold from the keepgoing panel is the same save as the command's, recorded
// as coming from the panel; the command's own record is unchanged.
func TestClientHoldFromThePanelIsRecordedAsSuch(t *testing.T) {
	dir, entry := clientFixture(t)
	for _, c := range []struct{ origin, op, recorded, phrase string }{
		{"panel", "pause", "panel_pause", "Paused from the panel"},
		{"", "stop", "stop", "Stop requested by a command"},
		// Already stopped: the save is recorded, and nothing changed to word.
		{"panel", "stop", "panel_stop", "A control change was saved; no hold changed"},
	} {
		r, code := applyClientHoldFrom(context.Background(), dir, entry.ID, c.op, c.origin, "")
		if code != 0 || r.ControlSave != "confirmed" {
			t.Fatalf("%+v: %d %+v", c, code, r)
		}
		j, err := incident.Load(entry.StateDir)
		if err != nil || len(j.Records) == 0 {
			t.Fatal(err)
		}
		last := j.Records[len(j.Records)-1]
		if last.ControlChange == nil || last.ControlChange.Operation != c.recorded {
			t.Errorf("%+v: recorded %+v", c, last.ControlChange)
		}
		if got := dockControlPhrase(last); got != c.phrase {
			t.Errorf("%+v: %q", c, got)
		}
	}
}
