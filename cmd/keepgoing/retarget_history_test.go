//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Uses the shell stand-in BB fixture, which is not built on Windows.
func TestRetargetIsRetainedWithoutItsNoteOrAliases(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.dir, "state")
	if code, out, errs := f.run("retarget", "claude-other-account", "--note", "PRIVATE_RETARGET_NOTE"); code != exitOK {
		t.Fatalf("retarget: %d %s %s", code, out, errs)
	}
	j, err := incident.Load(dir)
	if err != nil || len(j.Records) != 1 {
		t.Fatal(j, err)
	}
	r := j.Records[0]
	if r.Kind != "retarget" || r.TargetKey != incident.TargetKey(state.Primary) || r.TargetKeyAfter != incident.TargetKey("claude-other-account") || r.Outcome != "target_saved" || r.Build != version {
		t.Fatalf("%+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(dir, incident.FileName))
	for _, private := range []string{"PRIVATE_RETARGET_NOTE", "claude-other-account", "thr_"} {
		if strings.Contains(string(b), private) {
			t.Fatalf("%s leaked into incident history", private)
		}
	}
	// The existing private log keeps its note; the bounded history does not replace it.
	if log, _ := os.ReadFile(filepath.Join(dir, state.LogName)); !strings.Contains(string(log), "PRIVATE_RETARGET_NOTE") {
		t.Fatal("runs.jsonl lost the retarget note")
	}
	code, out, errs := f.run("timeline", "--kind", "retarget")
	if code != exitOK || errs != "" || !strings.Contains(out, "manual retarget saved: target primary -> fallback ") || strings.Contains(out, "PRIVATE_RETARGET_NOTE") || strings.Contains(out, "claude-other-account") {
		t.Fatalf("%d %s\n%s", code, errs, out)
	}
	// A rejected retarget is not a recorded move.
	if code, _, _ := f.run("retarget", "nope"); code != exitUsage {
		t.Fatal(code)
	}
	if j, err = incident.Load(dir); err != nil || len(j.Records) != 1 {
		t.Fatal(j, err)
	}
	// Unwritable history fails the command after the target is saved, without undoing it.
	write(t, filepath.Join(dir, incident.FileName), "{", 0o600)
	code, out, errs = f.run("retarget", "primary")
	if code != exitFailed || !strings.Contains(out, "target primary (was claude-other-account)") || !strings.Contains(errs, "target saved; incident history write failed") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	st, err := state.Load(dir)
	if err != nil || st.Target() != state.Primary {
		t.Fatal(st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, incident.FileName)); string(b) != "{" {
		t.Fatal("corrupt history was replaced")
	}
}
