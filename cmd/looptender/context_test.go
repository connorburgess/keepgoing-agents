//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/loopcontext"
)

// context reads the loop's records and nothing else: it starts no provider
// command, changes no file, and says what it could not read.
func TestContextCommandReadsOnly(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_owner1")
	if code, out, errs := f.run("enable"); code != exitOK {
		t.Fatalf("enable: %d %s %s", code, out, errs)
	}
	f.tick()
	if code, out, errs := f.run("pause", "--note", "Example review"); code != exitOK {
		t.Fatalf("pause: %d %s %s", code, out, errs)
	}
	t.Setenv("BB_THREAD_ID", "")
	before := snapshotTree(t, f.dir)

	code, out, errs := f.run("context", "--json")
	if code != exitOK || errs != "" {
		t.Fatalf("context --json: %d %s %s", code, out, errs)
	}
	var v loopcontext.View
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if v.SchemaVersion != 1 || v.Loop != "example-loop" || v.Adapter != "bb" || len(v.Unavailable) != 0 {
		t.Fatalf("%+v", v)
	}
	if v.Target == nil || v.Target.Route != "primary" || v.Target.BBThread != "thr_primary" || v.Target.IdentityEvidence != "configuration_only" {
		t.Fatalf("target %+v", v.Target)
	}
	if v.Owner.BBThread != "thr_owner1" || v.Owner.Generation != 1 {
		t.Fatalf("owner %+v", v.Owner)
	}
	if v.ControlState != "paused" || strings.Join(v.Holds, ",") != "paused" || v.Note != "Example review" {
		t.Fatalf("control %q %v %q", v.ControlState, v.Holds, v.Note)
	}
	if !v.Bounds.Unbounded || v.Permissions.PermissionMode != loopcontext.NotSetByLooptender {
		t.Fatalf("bounds %+v permissions %+v", v.Bounds, v.Permissions)
	}
	abs, _ := filepath.Abs(f.cfg)
	for _, c := range v.SessionCommands {
		if !strings.Contains(c.Command, " "+c.Name+" -c '"+abs+"'") {
			t.Fatalf("command %q does not name the configuration", c.Command)
		}
	}

	code, text, errs := f.run("context")
	if code != exitOK || errs != "" || !strings.Contains(text, "owner:     BB thread thr_owner1, generation 1\n") || !strings.Contains(text, "authority:\n") {
		t.Fatalf("context: %d %s %s", code, text, errs)
	}
	if after := snapshotTree(t, f.dir); after != before {
		t.Fatalf("context changed files:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestContextCommandWithUnreadableState(t *testing.T) {
	f := newFixture(t)
	if code, out, errs := f.run("enable"); code != exitOK {
		t.Fatalf("enable: %d %s %s", code, out, errs)
	}
	write(t, filepath.Join(f.dir, "state", "state.json"), "{not json", 0o600)
	calls, _ := os.ReadFile(filepath.Join(f.bbDir, "calls"))
	before := snapshotTree(t, f.dir)

	code, out, _ := f.run("context", "--json")
	var v loopcontext.View
	if code != exitFailed || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("context --json: %d %s", code, out)
	}
	if strings.Join(v.Unavailable, ",") != "state.json" || v.Target != nil || v.TargetUnavailable == "" || v.Owner.Text != loopcontext.NoOwnerRecorded {
		t.Fatalf("%+v", v)
	}
	code, text, _ := f.run("context")
	if code != exitFailed || !strings.Contains(text, "unreadable: state.json\n") || !strings.Contains(text, "target:    unavailable") {
		t.Fatalf("context: %d %s", code, text)
	}
	if after := snapshotTree(t, f.dir); after != before {
		t.Fatal("context changed files")
	}
	if now, _ := os.ReadFile(filepath.Join(f.bbDir, "calls")); string(now) != string(calls) {
		t.Fatalf("context called bb: %s", now)
	}
}

func TestContextCommandRefusesArguments(t *testing.T) {
	f := newFixture(t)
	if code, _, errs := f.run("context", "extra"); code != exitUsage || !strings.Contains(errs, "usage: keepgoing context") {
		t.Fatalf("%d %s", code, errs)
	}
}
