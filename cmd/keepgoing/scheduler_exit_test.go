//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func schedulerConfig(t *testing.T, f *fixture, extra string) {
	t.Helper()
	write(t, f.cfg, fmt.Sprintf(`version: 1
loop:
  name: scheduler-example
  state_dir: state
  prompt: Continue the fictional loop.
  adapter: bb
  bb: {thread: thr_primary, cli: %q}
%s`, filepath.Join(f.dir, "bin", "bb"), extra), 0600)
}

func tickResult(t *testing.T, out string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("tick output: %v\n%s", err, out)
	}
	return v
}

// The exit code of `tick` says whether keepgoing ran and recorded the check.
// An external scheduler that disables itself after a few non-zero exits must
// not be switched off by an outage, a failed send or a repository problem:
// those checks are recorded with outcome "failed" and exit 0. Only a check
// that could not be recorded exits 1.
func TestTickExitCodeContractForSchedulers(t *testing.T) {
	t.Run("observation failed", func(t *testing.T) {
		f := newFixture(t)
		schedulerConfig(t, f, "")
		f.run("enable")
		write(t, filepath.Join(f.dir, "bin", "bb"), "#!/bin/sh\nexit 1\n", 0700)
		for i := 0; i < 3; i++ { // three in a row, as a scheduler would count them
			code, out, errs := f.run("tick")
			if r := tickResult(t, out); code != exitOK || r["outcome"] != "failed" || r["action"] != "none" || !strings.Contains(errs, "tick:") {
				t.Fatalf("exit %d %v %s", code, r, errs)
			}
		}
		if _, status, _ := f.run("status"); !strings.Contains(status, "outcome failed") {
			t.Fatalf("status hides the failed check:\n%s", status)
		}
	})
	t.Run("send failed", func(t *testing.T) {
		f := newFixture(t)
		schedulerConfig(t, f, "")
		f.run("enable")
		write(t, filepath.Join(f.dir, "bin", "bb"), strings.Replace(fakeBB, `"thread tell") cat >> "$d/$t.told"; echo >> "$d/$t.told"; echo '{"ok":true}' ;;`, `"thread tell") cat > /dev/null; echo '{"ok":false,"error":{"code":"unavailable"}}'; exit 1 ;;`, 1), 0700)
		code, out, _ := f.run("tick")
		if r := tickResult(t, out); code != exitOK || r["outcome"] != "failed" || r["action"] != "wake" {
			t.Fatalf("exit %d %v", code, r)
		}
	})
	t.Run("repository unreadable", func(t *testing.T) {
		f := newFixture(t)
		schedulerConfig(t, f, fmt.Sprintf("  progress: {repositories: [%q]}\n", filepath.Join(f.dir, "absent-repository")))
		f.run("enable")
		code, out, _ := f.run("tick")
		if r := tickResult(t, out); code != exitOK || r["outcome"] != "failed" || r["reason"] != "could not fingerprint repositories; no action taken" {
			t.Fatalf("exit %d %v", code, r)
		}
		if f.read("thr_primary.told") != "" {
			t.Fatal("an unreadable repository did not fail closed")
		}
	})
	t.Run("check could not be recorded", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		f := newFixture(t)
		schedulerConfig(t, f, "")
		f.run("enable")
		if code, _, _ := f.run("tick"); code != exitOK {
			t.Fatal(code)
		}
		// state.json can no longer be replaced: the result cannot be saved.
		dir := filepath.Join(f.dir, "state")
		if err := os.Chmod(filepath.Join(dir, "state.json"), 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0o700)
		if code, _, errs := f.run("tick"); code != exitFailed {
			t.Fatalf("an unrecorded check exited %d: %s", code, errs)
		}
	})
	t.Run("ok", func(t *testing.T) {
		f := newFixture(t)
		schedulerConfig(t, f, "")
		f.run("enable")
		code, out, _ := f.run("tick")
		if r := tickResult(t, out); code != exitOK || r["outcome"] != "succeeded" {
			t.Fatalf("exit %d %v", code, r)
		}
	})
}

// A worktree of the tracked repository that was registered under a temporary
// directory is gone after a reboot. The check used to record "could not
// fingerprint repositories" and exit 1 until someone ran `git worktree prune`.
// It now leaves that worktree out, says so, and still wakes the session.
func TestTickSurvivesRegisteredWorktreeMissingFromDisk(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newFixture(t)
	repo := filepath.Join(f.dir, "project")
	gone := filepath.Join(t.TempDir(), "temporary-worktree")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	write(t, filepath.Join(repo, "a.txt"), "one\n", 0644)
	git("add", "a.txt")
	git("commit", "-q", "-m", "init")
	git("worktree", "add", "-q", "-b", "temporary", gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	schedulerConfig(t, f, fmt.Sprintf("  progress: {repositories: [%q]}\n", repo))
	f.run("enable")
	for i := 0; i < 3; i++ {
		code, out, errs := f.run("tick")
		r := tickResult(t, out)
		if code != exitOK || r["outcome"] != "succeeded" || r["action"] != "wake" || r["missingWorktrees"] != float64(1) || !strings.Contains(fmt.Sprint(r["warnings"]), "git worktree prune") {
			t.Fatalf("check %d: exit %d %v %s", i, code, r, errs)
		}
		if strings.Contains(out, gone) {
			t.Fatal("tick output names the worktree path")
		}
	}
	git("worktree", "prune")
	_, out, _ := f.run("tick")
	if r := tickResult(t, out); r["missingWorktrees"] != nil || r["warnings"] != nil {
		t.Fatalf("after prune: %v", r)
	}
}
