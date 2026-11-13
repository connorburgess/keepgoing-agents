package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	base, err := Fingerprint([]string{repo})
	if err != nil || MissingWorktrees([]string{repo}) != 0 {
		t.Fatal(err)
	}

	kept := filepath.Join(t.TempDir(), "kept")
	gone := filepath.Join(t.TempDir(), "gone")
	run("worktree", "add", "-q", "-b", "kept", kept)
	run("worktree", "add", "-q", "-b", "gone", gone)
	both, err := Fingerprint([]string{repo})
	if err != nil || both == base {
		t.Fatalf("worktrees are part of the fingerprint: %v", err)
	}
	if err := os.RemoveAll(gone); err != nil { // as after a reboot
		t.Fatal(err)
	}
	withMissing, err := Fingerprint([]string{repo})
	if err != nil {
		t.Fatalf("a missing registered worktree failed the fingerprint: %v", err)
	}
	if n := MissingWorktrees([]string{repo}); n != 1 {
		t.Fatalf("missing worktrees = %d", n)
	}
	// The remaining worktree still counts as progress evidence.
	os.WriteFile(filepath.Join(kept, "a.txt"), []byte("one\ntwo\n"), 0o644)
	changed, err := Fingerprint([]string{repo})
	if err != nil || changed == withMissing {
		t.Fatalf("progress in a remaining worktree was lost: %v", err)
	}
	// Pruning the stale registration changes nothing.
	run("worktree", "prune")
	pruned, err := Fingerprint([]string{repo})
	if err != nil || pruned != changed || MissingWorktrees([]string{repo}) != 0 {
		t.Fatalf("prune changed the fingerprint or left a missing worktree: %v", err)
	}

	// A worktree that exists but cannot be read still fails closed: it is
	// not evidence that nothing changed.
	if err := os.RemoveAll(filepath.Join(kept, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := Fingerprint([]string{repo}); err == nil {
		t.Fatal("an unreadable worktree was treated as absent")
	}
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		locked := filepath.Join(t.TempDir(), "locked")
		inner := filepath.Join(locked, "tree")
		if err := os.Mkdir(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		run("worktree", "prune")
		run("worktree", "add", "-q", "-b", "locked", inner)
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(locked, 0o755)
		if _, err := Fingerprint([]string{repo}); err == nil {
			t.Fatal("a worktree whose directory cannot be inspected was treated as absent")
		}
		if n := MissingWorktrees([]string{repo}); n != 0 {
			t.Fatalf("uninspectable worktree counted as missing: %d", n)
		}
	}
	// A repository that is itself gone is an error, not an empty fingerprint.
	if _, err := Fingerprint([]string{filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Fatal("a missing repository produced a fingerprint")
	}
}
