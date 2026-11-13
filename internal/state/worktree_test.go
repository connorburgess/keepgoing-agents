package state

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A worktree registered under a temporary directory disappears on reboot
// while Git still lists it. Before this was handled, the whole fingerprint
// failed ("could not fingerprint repositories"), which blocked every action
// of the check, wakes included, until someone ran `git worktree prune`.
func TestFingerprintSkipsRegisteredWorktreeMissingFromDisk(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	repos := []string{repo}
	fp := func() string {
		t.Helper()
		f, err := Fingerprint(repos)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	base := fp()
	if MissingWorktrees(base) != 0 {
		t.Fatal(base)
	}

	kept := filepath.Join(t.TempDir(), "kept")
	gone := filepath.Join(t.TempDir(), "gone")
	run("worktree", "add", "-q", "-b", "kept", kept)
	run("worktree", "add", "-q", "-b", "gone", gone)
	both := fp()
	// A newly registered worktree is not progress by itself.
	if both == base || !SameProgress(base, both) {
		t.Fatal("registering worktrees counted as progress")
	}
	if err := os.RemoveAll(gone); err != nil { // as after a reboot
		t.Fatal(err)
	}
	withMissing, err := Fingerprint(repos)
	if err != nil {
		t.Fatalf("a prunable registered worktree failed the fingerprint: %v", err)
	}
	// Losing the worktree is reported and is not progress, in either direction.
	if MissingWorktrees(withMissing) != 1 || !SameProgress(both, withMissing) || !SameProgress(withMissing, both) {
		t.Fatalf("missing=%d", MissingWorktrees(withMissing))
	}
	// The remaining worktree still counts as progress evidence.
	os.WriteFile(filepath.Join(kept, "a.txt"), []byte("one\ntwo\n"), 0o644)
	changed := fp()
	if SameProgress(withMissing, changed) || SameProgress(both, changed) {
		t.Fatal("progress in a remaining worktree was lost")
	}
	// Pruning the stale registration is not progress.
	run("worktree", "prune")
	pruned := fp()
	if !SameProgress(changed, pruned) || MissingWorktrees(pruned) != 0 {
		t.Fatal("prune counted as progress or left a missing worktree")
	}
	// A worktree that returns unchanged is not progress; its later change is.
	run("worktree", "add", "-q", "-B", "gone", gone)
	returned := fp()
	if !SameProgress(pruned, returned) || !SameProgress(changed, returned) {
		t.Fatal("a returning worktree counted as progress")
	}
	os.WriteFile(filepath.Join(gone, "a.txt"), []byte("changed\n"), 0o644)
	if SameProgress(returned, fp()) {
		t.Fatal("a change in the returned worktree was not progress")
	}

	// A commit made in a worktree before it is first observed is progress on
	// that first observation; a fresh checkout of an older commit is not.
	before := fp()
	fresh := filepath.Join(t.TempDir(), "fresh")
	run("worktree", "add", "-q", "--detach", fresh, "HEAD")
	if !SameProgress(before, fp()) {
		t.Fatal("a fresh checkout of an existing commit counted as progress")
	}
	run("worktree", "remove", "--force", fresh)
	worked := filepath.Join(t.TempDir(), "worked")
	run("worktree", "add", "-q", "-b", "worked", worked)
	os.WriteFile(filepath.Join(worked, "b.txt"), []byte("new\n"), 0o644)
	later := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-a", "--allow-empty", "-m", "work")
	later.Dir = worked
	later.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2038-01-01T00:00:00Z", "GIT_AUTHOR_DATE=2038-01-01T00:00:00Z")
	if out, err := later.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if SameProgress(before, fp()) {
		t.Fatal("a commit in a newly registered worktree was not progress on its first observation")
	}
	run("worktree", "remove", "--force", worked)
	edited := filepath.Join(t.TempDir(), "edited")
	run("worktree", "add", "-q", "--detach", edited, "HEAD")
	os.WriteFile(filepath.Join(edited, "a.txt"), []byte("uncommitted\n"), 0o644)
	if SameProgress(before, fp()) {
		t.Fatal("uncommitted work in a newly registered worktree was not progress")
	}
	run("worktree", "remove", "--force", edited)

	// A fingerprint saved by an older build cannot be compared: adopting the
	// new format is not progress. Unstructured values compare as they are.
	legacy := "5f70bf18a086007016e948b04aed3b82103a36bea41755b6cddfaf10ace3c6ef"
	previousFormat := "kg2|m=0|aaaaaaaaaaaaaaaa=1111"
	if !SameProgress(previousFormat, both) || !SameProgress(both, previousFormat) || MissingWorktrees(previousFormat) != 0 {
		t.Fatal("the previous structured format was compared instead of adopted")
	}
	if !SameProgress(legacy, both) || !SameProgress(both, legacy) || !SameProgress("same", "same") || SameProgress("same", "changed") || MissingWorktrees(legacy) != 0 {
		t.Fatal("format change or unstructured comparison is wrong")
	}

	// A locked worktree whose directory is gone (a volume that is not mounted)
	// is not prunable. It must fail closed, not be dropped silently.
	locked := filepath.Join(t.TempDir(), "locked")
	run("worktree", "add", "-q", "-b", "locked", locked)
	run("worktree", "lock", locked)
	if err := os.RemoveAll(locked); err != nil {
		t.Fatal(err)
	}
	if _, err := Fingerprint(repos); err == nil {
		t.Fatal("a locked worktree on a vanished volume was dropped")
	}
	run("worktree", "unlock", locked)
	if f := fp(); MissingWorktrees(f) != 1 {
		t.Fatal("an unlocked missing worktree is prunable")
	}
	run("worktree", "prune")

	// A worktree that exists but cannot be read still fails closed.
	if err := os.RemoveAll(filepath.Join(kept, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := Fingerprint(repos); err == nil {
		t.Fatal("an unreadable worktree was treated as absent")
	}
	// A repository that is itself gone is an error, not an empty fingerprint.
	if _, err := Fingerprint([]string{filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Fatal("a missing repository produced a fingerprint")
	}
}

// The whole fingerprint is bounded, not only each command: a filesystem that
// hangs on a metadata read ends with an error when the bound is reached.
func TestStatGivesUpWhenItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A FIFO never blocks stat, so use the cancelled context directly: the
	// call must return the context's error rather than wait.
	if _, err := statWithin(ctx, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("no error")
	}
	if _, err := git(ctx, t.TempDir(), "status"); !errors.Is(err, context.Canceled) {
		t.Fatalf("git ignored its context: %v", err)
	}
}
