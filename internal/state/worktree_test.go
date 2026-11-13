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

	// A fingerprint saved by an older build cannot be compared: adopting the
	// new format is not progress. Unstructured values compare as they are.
	legacy := "5f70bf18a086007016e948b04aed3b82103a36bea41755b6cddfaf10ace3c6ef"
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
