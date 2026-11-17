package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func migrationFixture(t *testing.T) (string, MigrationIdentity, MigrationIdentity, MigrationRevision) {
	t.Helper()
	root := t.TempDir()
	source := MigrationIdentity{Loop: "example-source", Thread: "thr_example_source", Config: filepath.Join(root, "source.yaml"), StateDir: filepath.Join(root, "source")}
	dest := MigrationIdentity{Loop: "example-successor", Thread: "thr_example_successor", Config: filepath.Join(root, "dest.yaml"), StateDir: filepath.Join(root, "dest")}
	if _, err := Update(dest.StateDir, func(c *File) { c.Paused = true; c.AwaitingUser = true }); err != nil {
		t.Fatal(err)
	}
	lane := func(thread string) MigrationLane {
		return MigrationLane{Thread: thread, Provider: "codex", Status: "idle", Project: "proj_example", Environment: "env_example", Host: "host_example", Path: root}
	}
	r := MigrationRevision{RecordedAt: time.Now().UTC(), Digest: strings.Repeat("a", 64), Plan: MigrationPlan{Assignments: []MigrationAssignment{{ID: "unfinished", Summary: "private assignment", DeclaredState: "unresolved"}}}, Observation: MigrationObservation{ObservedAt: time.Now().UTC(), Coverage: "direct_lanes_only", Source: lane(source.Thread), Successor: lane(dest.Thread), Lanes: []MigrationLane{}, Worktrees: []MigrationWorktree{}}}
	return dest.StateDir, source, dest, r
}
func TestMigrationReviewInterruptedRevisionRetainsSavedRecord(t *testing.T) {
	dir, source, dest, r := migrationFixture(t)
	g, err := WriteMigrationReview(dir, "", source, dest, r, "", 0, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, MigrationReviewName))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"interrupted", "omitted", "wrong-owner", "wrong-binding", "stale"} {
		next := r
		id, rev := g.ID, uint64(1)
		caller := ""
		to := dest
		verify := func() error { return nil }
		switch kind {
		case "interrupted":
			verify = func() error { return errors.New("cancelled") }
		case "omitted":
			next.Plan.Assignments = []MigrationAssignment{}
		case "wrong-owner":
			if _, err := Update(dir, func(c *File) { c.Ownership = &Ownership{ThreadID: "thr_example_owner", Generation: 1} }); err != nil {
				t.Fatal(err)
			}
			caller = "thr_example_other"
		case "wrong-binding":
			to.Thread = "thr_example_replaced"
		case "stale":
			rev = 2
		}
		if _, err := WriteMigrationReview(dir, caller, source, to, next, id, rev, verify); err == nil {
			t.Fatal(kind, "accepted")
		}
		after, err := os.ReadFile(filepath.Join(dir, MigrationReviewName))
		if err != nil || string(before) != string(after) {
			t.Fatal(kind, "changed record", err)
		}
	}
}
func TestMigrationReviewHistoryAndInputBounds(t *testing.T) {
	dir, source, dest, r := migrationFixture(t)
	var g *MigrationReview
	var err error
	for i := 0; i < migrationRevisionLimit; i++ {
		id := ""
		if g != nil {
			id = g.ID
		}
		g, err = WriteMigrationReview(dir, "", source, dest, r, id, uint64(i), func() error { return nil })
		if err != nil {
			t.Fatal(i, err)
		}
	}
	before, _ := os.ReadFile(filepath.Join(dir, MigrationReviewName))
	if _, err := WriteMigrationReview(dir, "", source, dest, r, g.ID, migrationRevisionLimit, func() error { return nil }); err == nil {
		t.Fatal("unbounded history")
	}
	after, _ := os.ReadFile(filepath.Join(dir, MigrationReviewName))
	if string(before) != string(after) {
		t.Fatal("history discarded")
	}
	if info, err := os.Stat(filepath.Join(dir, MigrationReviewName)); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		t.Fatal("review not private", err)
	}
	for _, input := range []string{`null`, `{"assignments":null}`, `{"assignments":[],"extra":true}`, `{"assignments":[{"id":"x","summary":"ok","declaredState":"running"}]}`, strings.Repeat(" ", MigrationPlanLimit+1)} {
		if _, err := ParseMigrationPlan([]byte(input)); err == nil {
			t.Fatal("invalid plan accepted")
		}
	}
	// The record limit applies before decoding, not only after a trusted write.
	if err := os.WriteFile(filepath.Join(dir, MigrationReviewName), []byte(strings.Repeat(" ", MigrationReviewLimit+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMigrationReview(dir); err == nil {
		t.Fatal("oversized record accepted")
	}
	// A truncated or unsupported history is unavailable, never a fresh create.
	for _, value := range []any{map[string]any{"version": 99}, nil} {
		b, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, MigrationReviewName), b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadMigrationReview(dir); err == nil {
			t.Fatal("damaged record accepted")
		}
	}
}

func TestMigrationReviewSizeRefusalDiscardsNothing(t *testing.T) {
	dir, source, dest, r := migrationFixture(t)
	r.Plan.Assignments = nil
	for i := 0; i < 200; i++ {
		r.Plan.Assignments = append(r.Plan.Assignments, MigrationAssignment{ID: fmt.Sprintf("work-%d", i), Summary: strings.Repeat("x", 1024), DeclaredState: "unresolved"})
	}
	var g *MigrationReview
	for i := 0; i < 32; i++ {
		var before []byte
		id := ""
		if g != nil {
			id = g.ID
			before, _ = os.ReadFile(filepath.Join(dir, MigrationReviewName))
		}
		next, err := WriteMigrationReview(dir, "", source, dest, r, id, uint64(i), func() error { return nil })
		if err != nil {
			after, _ := os.ReadFile(filepath.Join(dir, MigrationReviewName))
			if len(before) == 0 || string(before) != string(after) {
				t.Fatal("size refusal discarded record")
			}
			return
		}
		g = next
	}
	t.Fatal("size bound was not enforced")
}
