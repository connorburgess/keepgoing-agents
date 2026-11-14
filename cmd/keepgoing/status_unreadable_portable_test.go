package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// noDaemon answers status's daemon query without looking for one.
func noDaemon(context.Context, string, string, bool) daemonStatus {
	return daemonStatus{State: "not_running"}
}

// unreadableStatus damages one record of a fresh loop in the way named and
// returns status's result and its JSON.
func unreadableStatus(t *testing.T, file, how string) (statusOut, map[string]json.RawMessage) {
	t.Helper()
	cfg, err := config.Load(registryConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := cfg.Loop.StateDir
	if err := control.Save(dir, control.File{Enabled: true, Stop: true, Paused: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(dir, state.File{Reprompts: 2, ActiveTarget: "fallback-example"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, file)
	switch how {
	case "not_json":
		err = os.WriteFile(path, []byte("{\"enabled\": tr"), 0o600)
	case "wrong_shape":
		err = os.WriteFile(path, []byte(`{"enabled":"yes","reprompts":"two"}`), 0o600)
	case "not_a_file":
		if err = os.Remove(path); err == nil {
			err = os.Mkdir(path, 0o700)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	out, err := readStatus(context.Background(), cfg, time.Now(), time.Minute, noDaemon)
	if err == nil || len(out.Unreadable) != 1 || out.Unreadable[0].File != file {
		t.Fatalf("%s %s: err %v, unreadable %+v", file, how, err, out.Unreadable)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return out, m
}

// Each kind of unreadable record gets a fixed reason, never the error text,
// and the remedy that fits it: restore from a copy only when the content is
// damaged, and never "move it aside" when the file may be intact.
func TestStatusClassifiesAnUnreadableRecord(t *testing.T) {
	for _, file := range []string{control.FileName, state.FileName} {
		for _, how := range []string{"not_json", "wrong_shape", "not_a_file"} {
			t.Run(file+"/"+how, func(t *testing.T) {
				out, _ := unreadableStatus(t, file, how)
				u := out.Unreadable[0]
				if u.Problem != how {
					t.Fatalf("problem %q, want %q", u.Problem, how)
				}
				if (u.Offset != nil) != (how != "not_a_file") {
					t.Errorf("offset %v for %s", u.Offset, how)
				}
				damaged := how != "not_a_file"
				if strings.Contains(u.Remedy, "restore "+file+" from a copy") != damaged || strings.Contains(u.Remedy, "do not move it aside") == damaged {
					t.Errorf("remedy for %s: %s", how, u.Remedy)
				}
			})
		}
	}
}

// What the damaged-content remedies must warn about moving the record aside.
func TestStatusRemedySaysWhatMovingARecordAsideLoses(t *testing.T) {
	out, _ := unreadableStatus(t, control.FileName, "not_json")
	for _, want := range []string{"a stop or pause (issue it again)", "a pending transfer", "the bbTarget binding", "read as disabled"} {
		if !strings.Contains(out.Unreadable[0].Remedy, want) {
			t.Errorf("control.json remedy does not say %q: %s", want, out.Unreadable[0].Remedy)
		}
	}
	out, _ = unreadableStatus(t, state.FileName, "not_json")
	for _, want := range []string{"the new-thread count", "the spacing between created threads", "whose outcome is unknown"} {
		if !strings.Contains(out.Unreadable[0].Remedy, want) {
			t.Errorf("state.json remedy does not say %q: %s", want, out.Unreadable[0].Remedy)
		}
	}
}

// status --json never shows an empty record as the loop's: with control.json
// unreadable there is no "control" (a stopped loop would read as not
// stopped); with state.json unreadable nothing taken from it is there, and
// the target, schedule and no-progress flag say why.
func TestStatusJSONLeavesOutAnUnreadableRecord(t *testing.T) {
	out, m := unreadableStatus(t, control.FileName, "not_json")
	if _, ok := m["control"]; ok {
		t.Errorf("control is shown for an unreadable control.json: %s", m["control"])
	}
	if out.ControlState != "unavailable" || out.Schedule.NotJudged != "control_unreadable" || out.NoProgress.Reason != "record_unreadable" {
		t.Errorf("controlState %q, schedule %+v, noProgress %+v", out.ControlState, out.Schedule, out.NoProgress)
	}
	if _, ok := m["state"]; !ok {
		t.Error("the readable state.json is not shown")
	}

	out, m = unreadableStatus(t, state.FileName, "not_json")
	for _, k := range stateFields {
		if _, ok := m[k]; ok {
			t.Errorf("%s is shown for an unreadable state.json: %s", k, m[k])
		}
	}
	var target string
	_ = json.Unmarshal(m["target"], &target)
	var configured bool
	_ = json.Unmarshal(m["targetConfigured"], &configured)
	if target != "unavailable" || configured || out.Schedule.NotJudged != "state_unreadable" || out.Runtime.Freshness != "unavailable" || out.NoProgress.Reason != "record_unreadable" {
		t.Errorf("target %q, targetConfigured %t, schedule %+v, runtime %+v, noProgress %+v", target, configured, out.Schedule, out.Runtime, out.NoProgress)
	}
	if _, ok := m["control"]; !ok {
		t.Error("the readable control.json is not shown")
	}
}

// The dock needs both records: a loop status returns an error for is shown
// without its status, never with the partial one.
func TestDockDropsAPartialStatus(t *testing.T) {
	dir, entry := clientFixture(t)
	if err := control.Save(entry.StateDir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(entry.StateDir, state.File{}); err != nil {
		t.Fatal(err)
	}
	cfg, ok := clientConfig(entry)
	if !ok {
		t.Fatal("fixture config")
	}
	r := &dockReader{directory: dir, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: noDaemon}
	validated := map[string]*config.Config{entry.ID: cfg}
	if in := r.readLoop(context.Background(), dir, entry, []registry.Entry{entry}, validated, time.Now()); in.Status == nil {
		t.Fatal("the dock did not read status for a loop whose records are readable")
	}
	if err := os.WriteFile(filepath.Join(entry.StateDir, control.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if in := r.readLoop(context.Background(), dir, entry, []registry.Entry{entry}, validated, time.Now()); in.Status != nil {
		t.Fatalf("the dock kept a partial status: controlState %q", in.Status.ControlState)
	}
}
