package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStateRoundTripAndLog(t *testing.T) {
	dir := t.TempDir()
	f, err := Load(dir)
	if err != nil || f.Reprompts != 0 {
		t.Fatalf("empty load: %+v %v", f, err)
	}
	f.Fingerprint = "abc"
	f.Reprompts = 2
	f.CheckedAt = time.Now().UTC().Truncate(time.Second)
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	g, err := Load(dir)
	if err != nil || g.Fingerprint != "abc" || g.Reprompts != 2 || !g.CheckedAt.Equal(f.CheckedAt) {
		t.Fatalf("round trip: %+v %v", g, err)
	}
	info, _ := os.Stat(filepath.Join(dir, FileName))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("state.json mode = %v", info.Mode().Perm())
	}
	for i := 0; i < 2; i++ {
		if err := Append(dir, Event{Loop: "x", Event: "tick", Action: "none"}); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, LogName))
	if n := strings.Count(string(b), "\n"); n != 2 {
		t.Errorf("log lines = %d", n)
	}
}

func TestFingerprintTracksOnlyMetadata(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	run := func(args ...string) {
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
	fp1, err := Fingerprint([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	fp2, _ := Fingerprint([]string{repo})
	if fp1 != fp2 {
		t.Error("fingerprint should be stable with no changes")
	}
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644)
	fp3, _ := Fingerprint([]string{repo})
	if fp3 == fp1 {
		t.Error("fingerprint should change after a tracked file changes")
	}
	// An untracked file is not progress.
	os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("x"), 0o644)
	fp4, _ := Fingerprint([]string{repo})
	if fp4 != fp3 {
		t.Error("untracked files must not affect the fingerprint")
	}
}
