package control

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func testGoalSpec() GoalSpec {
	return GoalSpec{Title: "Private project milestone", ObjectiveRefs: []string{"private/objective.md"}, AcceptanceRefs: []string{"private/acceptance.md"}, ScopeRefs: []string{"private/scope.md"}, AuthorizationRefs: []string{"private/authorization.md"}, NextAction: &GoalNextAction{Summary: "Check the milestone", Refs: []string{"private/next.md"}}, EvidenceRefs: []string{"private/evidence.md"}}
}

func TestGoalRecordLifecycleAndHolds(t *testing.T) {
	dir := t.TempDir()
	ctl := File{Ownership: &Ownership{ThreadID: "thr_fixture", Generation: 1}, Enabled: true, Paused: true, Stop: true, Completed: true, AwaitingUser: true, Note: "private hold"}
	if err := Save(dir, ctl); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	spec := testGoalSpec()
	g, err := WriteGoal(dir, "fixture", "thr_fixture", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	id := g.ID
	if !goalID.MatchString(id) || g.Summary().Revision != 1 || g.State != "recorded" {
		t.Fatalf("%+v", g)
	}
	if _, err := WriteGoal(dir, "fixture", "thr_fixture", spec, "", 0); err == nil {
		t.Fatal("duplicate create succeeded")
	}
	spec.Title = "Revised private milestone"
	g, err = WriteGoal(dir, "fixture", "thr_fixture", spec, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != id || len(g.Revisions) != 2 || g.Revisions[0].Spec.Title == spec.Title {
		t.Fatal("identity/history lost")
	}
	stored, _ := os.ReadFile(filepath.Join(dir, GoalName))
	for _, trial := range []struct {
		loop, caller, id string
		revision         uint64
	}{
		{"fixture", "thr_fixture", id, 1},
		{"fixture", "thr_fixture", "goal_00000000000000000000000000000000", 2},
		{"other", "thr_fixture", id, 2},
		{"fixture", "thr_other", id, 2},
	} {
		if _, err := WriteGoal(dir, trial.loop, trial.caller, spec, trial.id, trial.revision); err == nil {
			t.Fatal("invalid write accepted")
		}
		got, _ := os.ReadFile(filepath.Join(dir, GoalName))
		if !bytes.Equal(got, stored) {
			t.Fatal("rejected write changed record")
		}
	}
	loaded, err := LoadGoal(dir)
	if err != nil || loaded.Summary() != g.Summary() {
		t.Fatalf("reload: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("goal edit rewrote controls")
	}
	info, _ := os.Stat(filepath.Join(dir, GoalName))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("goal is not private")
	}
	// A scheduler without a BB identity can use the existing claim, as controls do.
	if _, err := WriteGoal(dir, "fixture", "", spec, id, 2); err != nil {
		t.Fatal(err)
	}
}

func TestGoalRecordRejectsUnavailableAndMalformed(t *testing.T) {
	for name, data := range map[string]string{
		"corrupt": "private secret broken JSON", "null": "null", "unknownVersion": `{"version":2}`, "oversized": strings.Repeat("x", maxGoalBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, GoalName)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadGoal(dir); err != ErrGoal {
				t.Fatal(err)
			}
			if _, err := WriteGoal(dir, "fixture", "", testGoalSpec(), "", 0); err != ErrGoal {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != data {
				t.Fatal("bad history overwritten")
			}
		})
	}
	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		os.Mkdir(filepath.Join(dir, GoalName), 0700)
		if _, err := LoadGoal(dir); err != ErrGoal {
			t.Fatal(err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "private.json")
		os.WriteFile(target, []byte("private"), 0600)
		if err := os.Symlink(target, filepath.Join(dir, GoalName)); err != nil {
			t.Skip(err)
		}
		if _, err := LoadGoal(dir); err != ErrGoal {
			t.Fatal(err)
		}
	})
}

func TestGoalRecordValidationAndHistoryLimit(t *testing.T) {
	dir := t.TempDir()
	g, err := WriteGoal(dir, "fixture", "", testGoalSpec(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	original := *g
	for _, mutate := range []func(*Goal){
		func(g *Goal) { g.Revisions = nil }, func(g *Goal) { g.Version = 2 }, func(g *Goal) { g.State = "completed" }, func(g *Goal) { g.ID = "private text" }, func(g *Goal) { g.OwningLoop = "" },
	} {
		bad := original
		mutate(&bad)
		b, _ := json.Marshal(bad)
		os.WriteFile(filepath.Join(dir, GoalName), b, 0600)
		if _, err := LoadGoal(dir); err != ErrGoal {
			t.Fatal("accepted invalid record")
		}
	}
	// Keep history bounded without silently deleting prior revisions.
	g = &original
	for len(g.Revisions) < maxGoalRevisions {
		r := g.Revisions[0]
		r.Revision = uint64(len(g.Revisions) + 1)
		g.Revisions = append(g.Revisions, r)
	}
	b, _ := json.Marshal(g)
	os.WriteFile(filepath.Join(dir, GoalName), b, 0600)
	if _, err := WriteGoal(dir, "fixture", "", testGoalSpec(), g.ID, uint64(maxGoalRevisions)); err == nil {
		t.Fatal("history cap ignored")
	}
	after, _ := os.ReadFile(filepath.Join(dir, GoalName))
	if !bytes.Equal(b, after) {
		t.Fatal("history was discarded")
	}
}

func TestGoalRecordConcurrentRevision(t *testing.T) {
	dir := t.TempDir()
	g, err := WriteGoal(dir, "fixture", "", testGoalSpec(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	successes := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := WriteGoal(dir, "fixture", "", testGoalSpec(), g.ID, 1)
			successes <- err == nil
		}()
	}
	wg.Wait()
	close(successes)
	n := 0
	for ok := range successes {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d competing revisions accepted", n)
	}
	loaded, err := LoadGoal(dir)
	if err != nil || loaded.Summary().Revision != 2 {
		t.Fatal("lost revision")
	}
	held, err := lock.AcquireNamed(dir, lock.Control)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if _, err := WriteGoal(dir, "fixture", "", testGoalSpec(), g.ID, 2); err != lock.ErrHeld {
		t.Fatal(err)
	}
}

func TestGoalInputPrivacyAndValidation(t *testing.T) {
	good, _ := json.Marshal(testGoalSpec())
	if _, err := ParseGoalSpec(good); err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{[]byte("private error content"), []byte("null"), []byte(`{"title":"private"}`), append(good, []byte(" {}")...), []byte(strings.Repeat("s", 32769)), []byte(`{"title":"private","objectiveRefs":["x"],"acceptanceRefs":["y"],"secret":"never echo"}`)} {
		if _, err := ParseGoalSpec(b); err == nil || strings.Contains(err.Error(), "never echo") || strings.Contains(err.Error(), "private error") {
			t.Fatalf("%v", err)
		}
	}
}

func TestGoalRecordCreationKeepsMissingControlDisabled(t *testing.T) {
	dir := t.TempDir()
	if g, err := LoadGoal(dir); err != nil || g != nil {
		t.Fatal("missing goal is not optional")
	}
	if _, err := WriteGoal(dir, "fixture", "", testGoalSpec(), "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatal("metadata creation created controls")
	}
	ctl, err := Load(dir)
	if err != nil || ctl.Enabled {
		t.Fatal("creation enabled loop")
	}
}

func TestGoalRecordByteLimitPreservesHistory(t *testing.T) {
	dir := t.TempDir()
	spec := testGoalSpec()
	spec.ObjectiveRefs = []string{strings.Repeat("x", 2048)}
	// About 30 KiB per accepted specification, below the input limit.
	for range 13 {
		spec.ObjectiveRefs = append(spec.ObjectiveRefs, strings.Repeat("y", 2048))
	}
	g, err := WriteGoal(dir, "fixture", "", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for range maxGoalRevisions {
		before, _ := os.ReadFile(filepath.Join(dir, GoalName))
		next, err := WriteGoal(dir, "fixture", "", spec, g.ID, g.Summary().Revision)
		if err != nil {
			if !strings.Contains(err.Error(), "maximum size") {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(filepath.Join(dir, GoalName))
			if !bytes.Equal(before, after) {
				t.Fatal("size rejection changed history")
			}
			return
		}
		g = next
	}
	t.Fatal("byte limit was not enforced")
}
