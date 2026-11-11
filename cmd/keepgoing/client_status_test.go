package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func clientFixture(t *testing.T) (string, registry.Entry) {
	t.Helper()
	cfg := registryConfig(t)
	dir := filepath.Join(t.TempDir(), "registry")
	if code, out, errs := registryRun(t, dir, "add", "-c", cfg); code != 0 {
		t.Fatal(out, errs)
	}
	f, err := registry.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, f.Entries[0]
}
func clientRead(t *testing.T, dir string, entry registry.Entry, private bool) clientStatusReport {
	t.Helper()
	return readClientStatus(context.Background(), dir, entry.ID, private, true, time.Minute, time.Now().UTC(), inspectDaemon)
}
func TestClientStatusMissingRecordsDoNotCreateOrContact(t *testing.T) {
	dir, entry := clientFixture(t)
	r := clientRead(t, dir, entry, false)
	if r.Outcome != "available" || r.Snapshot == nil || r.Snapshot.Controls.Enabled || r.Snapshot.Runtime.Freshness != "never" || r.Snapshot.GoalState != "not_recorded" || r.Snapshot.Daemon.State != "skipped" {
		t.Fatalf("%+v %+v", r, r.Snapshot)
	}
	if _, err := os.Stat(entry.StateDir); !os.IsNotExist(err) {
		t.Fatal("read created runtime directory")
	}
	// Config points to a nonexistent provider CLI and task file. Neither is needed.
	if r.Snapshot.Target.BBThread != "thr_example" || r.Snapshot.Target.IdentityEvidence != "configuration_only" {
		t.Fatal(r.Snapshot.Target)
	}
	before, _ := os.ReadFile(filepath.Join(dir, registry.FileName))
	clientRead(t, dir, entry, false)
	after, _ := os.ReadFile(filepath.Join(dir, registry.FileName))
	if string(before) != string(after) {
		t.Fatal("read changed registry")
	}
}
func TestClientStatusPrivacyHoldsAndGoalDisplay(t *testing.T) {
	dir, entry := clientFixture(t)
	ctl := control.File{Enabled: true, Paused: true, Completed: true, Note: "PRIVATE-NOTE", Ownership: &control.Ownership{ThreadID: "thr_private_owner", Generation: 1}}
	if err := control.Save(entry.StateDir, ctl); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	st := state.File{CheckedAt: now.Add(-time.Hour), LastCheck: &state.Check{At: now.Add(-time.Hour), Target: "primary", ObservedTarget: "primary", ObservedState: "idle", Action: "wake", Outcome: "failed", Reason: "PRIVATE-REASON", Version: "PRIVATE-BUILD"}}
	if err := state.Save(entry.StateDir, st); err != nil {
		t.Fatal(err)
	}
	spec := control.GoalSpec{Title: "PRIVATE-TITLE", ObjectiveRefs: []string{"PRIVATE-OBJECTIVE"}, AcceptanceRefs: []string{"PRIVATE-ACCEPTANCE"}, NextAction: &control.GoalNextAction{Summary: "PRIVATE-NEXT", Refs: []string{"PRIVATE-NEXT-REF"}}}
	if _, err := control.WriteGoal(entry.StateDir, entry.Loop, "", spec, "", 0); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(entry.StateDir, control.FileName))
	r := clientRead(t, dir, entry, false)
	raw, _ := json.Marshal(r)
	for _, secret := range []string{"PRIVATE", entry.ConfigPath, entry.StateDir, "thr_private_owner", "stateDir", "configPath"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private metadata escaped", secret)
		}
	}
	if r.Outcome != "available" || !r.Snapshot.Controls.Completed || r.Snapshot.ControlState != "paused" || r.Snapshot.Runtime.Freshness != "stale" || r.Snapshot.LastCheck.Outcome != "failed" || r.Snapshot.Goal == nil {
		t.Fatalf("%+v", r.Snapshot)
	}
	private := clientRead(t, dir, entry, true)
	raw, _ = json.Marshal(private)
	if private.Snapshot.PrivateGoal.Title != "PRIVATE-TITLE" || private.Snapshot.PrivateGoal.NextAction != "PRIVATE-NEXT" {
		t.Fatal("display omitted")
	}
	for _, secret := range []string{"PRIVATE-OBJECTIVE", "PRIVATE-ACCEPTANCE", "PRIVATE-NEXT-REF", "PRIVATE-NOTE"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("extra private fields escaped", secret)
		}
	}
	after, _ := os.ReadFile(filepath.Join(entry.StateDir, control.FileName))
	if string(before) != string(after) {
		t.Fatal("holds changed")
	}
}
func TestClientStatusRejectsBindingsAndKeepsPartialEvidence(t *testing.T) {
	for _, scenario := range []string{"config_missing", "renamed", "runtime_changed", "control_corrupt", "state_corrupt", "goal_corrupt", "goal_other_loop", "pending", "bound_other_target", "unknown_history", "target_missing"} {
		t.Run(scenario, func(t *testing.T) {
			dir, entry := clientFixture(t)
			os.MkdirAll(entry.StateDir, 0700)
			writeRecord := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(entry.StateDir, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "config_missing":
				os.Remove(entry.ConfigPath)
			case "renamed", "runtime_changed":
				b, _ := os.ReadFile(entry.ConfigPath)
				old, new := "name: example-loop", "name: different"
				if scenario == "runtime_changed" {
					old, new = "state_dir: state", "state_dir: other"
				}
				os.WriteFile(entry.ConfigPath, []byte(strings.Replace(string(b), old, new, 1)), 0600)
			case "control_corrupt":
				writeRecord(control.FileName, `{"note":"PRIVATE"`)
			case "state_corrupt":
				writeRecord(state.FileName, `null`)
			case "goal_corrupt":
				writeRecord(control.GoalName, `{"PRIVATE":true}`)
			case "goal_other_loop":
				_, err := control.WriteGoal(entry.StateDir, "different", "", control.GoalSpec{Title: "PRIVATE-TITLE", ObjectiveRefs: []string{"ref"}, AcceptanceRefs: []string{"ref"}}, "", 0)
				if err != nil {
					t.Fatal(err)
				}
			case "pending":
				writeRecord(control.FileName, `{"pendingTransfer":{}}`)
			case "bound_other_target":
				writeRecord(control.FileName, `{"bbTarget":"thr_other"}`)
			case "target_missing":
				writeRecord(state.FileName, `{"activeTarget":"PRIVATE-TARGET"}`)
			case "unknown_history":
				writeRecord(state.FileName, `{"lastCheck":{"target":"PRIVATE-TARGET","action":"PRIVATE-ACTION","outcome":"PRIVATE-OUTCOME","reason":"PRIVATE-REASON"}}`)
			}
			r := clientRead(t, dir, entry, true)
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "PRIVATE") {
				t.Fatal("private raw record escaped", string(b))
			}
			switch scenario {
			case "config_missing", "renamed", "runtime_changed", "pending", "bound_other_target":
				if r.Outcome != "unavailable" || r.Snapshot != nil {
					t.Fatal(r)
				}
			case "unknown_history":
				if r.Snapshot.LastCheck.Action != "unknown" || r.Snapshot.LastCheck.Outcome != "unknown" || r.Snapshot.LastCheck.Target != "unavailable" {
					t.Fatal(r.Snapshot.LastCheck)
				}
			default:
				if r.Outcome != "partial" || r.Snapshot == nil {
					t.Fatal(r)
				}
			}
			if scenario == "control_corrupt" && (r.Snapshot.Controls != nil || r.Snapshot.Runtime != nil) {
				t.Fatal("invalid controls rendered healthy")
			}
			if scenario == "state_corrupt" && (r.Snapshot.Target != nil || r.Snapshot.Runtime != nil) {
				t.Fatal("invalid history defaulted to primary")
			}
		})
	}
}
func TestClientStatusRechecksAfterDaemonRead(t *testing.T) {
	for _, change := range []string{"registration_removed", "config_replaced", "state_rebound", "cancel"} {
		t.Run(change, func(t *testing.T) {
			dir, entry := clientFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			query := func(queryCtx context.Context, path, build string, offline bool) daemonStatus {
				if path != entry.StateDir || offline {
					t.Fatal("wrong query binding")
				}
				if deadline, ok := queryCtx.Deadline(); !ok || time.Until(deadline) > time.Second {
					t.Fatal("query not bounded")
				}
				switch change {
				case "registration_removed":
					if err := registry.Remove(dir, entry.ID); err != nil {
						t.Fatal(err)
					}
				case "config_replaced", "state_rebound":
					b, _ := os.ReadFile(entry.ConfigPath)
					old, new := "thr_example", "thr_replaced"
					if change == "state_rebound" {
						old, new = "state_dir: state", "state_dir: other"
					}
					os.WriteFile(entry.ConfigPath, []byte(strings.Replace(string(b), old, new, 1)), 0600)
				case "cancel":
					cancel()
				}
				return daemonStatus{State: "responsive"}
			}
			r := readClientStatus(ctx, dir, entry.ID, false, false, time.Minute, time.Now(), query)
			if r.Outcome != "unavailable" || r.Snapshot != nil {
				t.Fatal("stale snapshot published", r)
			}
		})
	}
}

func TestClientStatusCLIEnvelopeAndArgumentErrors(t *testing.T) {
	dir, entry := clientFixture(t)
	call := func(args ...string) (int, string, string) {
		var out, errs bytes.Buffer
		code := run(context.Background(), args, &out, &errs)
		return code, out.String(), errs.String()
	}
	code, out, errs := call("client", "status", "--directory", dir, "--id", entry.ID, "--json", "--offline")
	var report clientStatusReport
	if code != 0 || errs != "" || json.Unmarshal([]byte(out), &report) != nil || report.SchemaVersion != 1 || report.RegistrationID != entry.ID {
		t.Fatal(code, out, errs)
	}
	if err := registry.Remove(dir, entry.ID); err != nil {
		t.Fatal(err)
	}
	code, out, errs = call("client", "status", "--directory", dir, "--id", entry.ID, "--json", "--offline")
	if code != exitFailed || errs != "" || json.Unmarshal([]byte(out), &report) != nil || report.Outcome != "unavailable" {
		t.Fatal(code, out, errs)
	}
	for _, args := range [][]string{
		{"client", "resume"}, {"client", "status"}, {"client", "status", "--id", "PRIVATE-ID"},
		{"client", "status", "--id", entry.ID, "--stale-after", "0s"}, {"client", "status", "--PRIVATE-ARG"},
	} {
		code, out, errs = call(args...)
		if code != exitUsage || out != "" || strings.Contains(errs, "PRIVATE") {
			t.Fatal(code, out, errs)
		}
	}
}

func TestClientStatusLocalDaemonChallengeAndOfflineSkip(t *testing.T) {
	dir, entry := clientFixture(t)
	if err := os.Mkdir(entry.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	server, err := daemoninfo.Start(entry.StateDir, version, true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	next := time.Now().UTC().Add(time.Minute)
	server.Update("waiting", &next)
	r := readClientStatus(context.Background(), dir, entry.ID, false, false, time.Minute, time.Now().UTC(), inspectDaemon)
	if r.Outcome != "available" || r.Snapshot.Daemon.State != "responsive" || !r.Snapshot.Daemon.DryRun || r.Snapshot.Daemon.NextCheckAt == nil || r.Snapshot.Daemon.ObservedAt == nil {
		t.Fatalf("%+v %+v", r, r.Snapshot)
	}
	if r.Snapshot.Runtime.Freshness != "never" || r.Snapshot.Runtime.State != "disabled" {
		t.Fatal("daemon presence implied agent activity", r.Snapshot.Runtime)
	}
	r = clientRead(t, dir, entry, false)
	if r.Snapshot.Daemon.State != "skipped" || r.Snapshot.Daemon.ObservedAt != nil {
		t.Fatal("offline queried responder")
	}
}
