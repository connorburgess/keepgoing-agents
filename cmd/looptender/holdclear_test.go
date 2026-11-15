package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/looptender/looptender/internal/control"
)

func TestMarkSupervisedNamesOnlyItsLoopAndRestores(t *testing.T) {
	t.Setenv(control.SupervisedLoopVar, "")
	os.Unsetenv(control.SupervisedLoopVar)
	dir := t.TempDir()
	loop, other := filepath.Join(dir, "loop"), filepath.Join(dir, "other")
	os.Mkdir(loop, 0o700)
	os.Mkdir(other, 0o700)
	restore := markSupervised(loop)
	if !supervisedBy(loop) || !supervisedBy(loop+string(filepath.Separator)+".") || supervisedBy(other) {
		t.Fatal("marker does not name exactly its own loop")
	}
	restore()
	if _, set := os.LookupEnv(control.SupervisedLoopVar); set || supervisedBy(loop) {
		t.Fatal("marker left behind")
	}
	t.Setenv(control.SupervisedLoopVar, other)
	markSupervised(loop)()
	if os.Getenv(control.SupervisedLoopVar) != other {
		t.Fatal("earlier marker not put back")
	}
}

func TestClientResumeRefusesTheSupervisedSession(t *testing.T) {
	f := newResumeFixture(t)
	path := filepath.Join(f.entry.StateDir, control.FileName)
	before := resumeBytes(t, path)
	t.Setenv(control.SupervisedLoopVar, f.entry.StateDir)
	for _, op := range []string{"prepare-resume", "resume"} {
		r, c := f.call(op, "resume_00000000000000000000000000000000", false, nil)
		if c != exitFailed || r.Ticket != "" || r.ControlSave != "not_attempted" || !slices.Contains(r.Issues, "supervised_session") {
			t.Fatal(op, c, r)
		}
	}
	if string(resumeBytes(t, path)) != string(before) {
		t.Fatal("a refused resume changed the holds")
	}
	if res := panelOutcome("resume", clientHoldReport{Issues: []string{"supervised_session"}}); res.Outcome != "refused" || res.Text != panelSupervisedSession+" Nothing was changed." {
		t.Fatal(res)
	}
	// Another loop's marker is not this loop's supervised session.
	t.Setenv(control.SupervisedLoopVar, t.TempDir())
	f.prepare(t)
}
