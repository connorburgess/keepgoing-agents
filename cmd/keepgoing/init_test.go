package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

type setupFixture struct{ root, project, destination string }

func newSetupFixture(t *testing.T) setupFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := setupFixture{root: root, project: filepath.Join(root, "project"), destination: filepath.Join(root, "private", "example-loop")}
	if err := os.Mkdir(f.project, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(root, "no-hooks"), "commit", "--allow-empty", "-q", "-m", "test: initialize fictional project"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = f.project
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.com", "GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	return f
}

func (f setupFixture) args() []string {
	return []string{"--non-interactive", "--name", "example-loop", "--agent", "codex", "--project", f.project, "--directory", f.destination, "--task", "Complete and verify the fictional example task."}
}

// Returning nonexistent executables ensures setup cannot successfully invoke
// any provider, even accidentally. Only discovery is permitted here.
func (f setupFixture) lookPath(name string) (string, error) {
	return filepath.Join(f.root, "not-executable", name), nil
}
func (f setupFixture) run(t *testing.T, args []string, input string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := setupLoop(env{context.Background(), &out, &errOut}, args, strings.NewReader(input), f.lookPath)
	return code, out.String(), errOut.String()
}

func TestInitCreatesDisabledPrivateSetup(t *testing.T) {
	f := newSetupFixture(t)
	code, out, errOut := f.run(t, f.args(), "")
	if code != 0 {
		t.Fatalf("%d: %s", code, errOut)
	}
	cfg, err := config.Load(filepath.Join(f.destination, "keepgoing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.Adapter != "codex" || cfg.Loop.Codex.Workdir != f.project || cfg.Loop.StateDir != filepath.Join(f.destination, "state") {
		t.Fatalf("bad config: %+v", cfg.Loop)
	}
	if cfg.Loop.Codex.Sandbox != "" || len(cfg.Loop.Codex.ExtraArgs) != 0 || cfg.FallbackSet != nil || len(cfg.Loop.Errors.FailoverOn) != 0 {
		t.Fatal("unexpected permissions or fallbacks")
	}
	gate, err := control.Load(cfg.Loop.StateDir)
	if err != nil || gate.Enabled {
		t.Fatalf("loop not disabled: %+v %v", gate, err)
	}
	if _, err := os.Stat(cfg.Loop.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("setup must not start runtime state")
	}
	prompt, err := cfg.Loop.PromptText()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ENTIRE task", "complete -c", "await-user -c", "Complete and verify the fictional example task.", commandQuote(filepath.Join(f.destination, "keepgoing.yaml"))} {
		if !strings.Contains(prompt, s) {
			t.Errorf("missing prompt instruction %q", s)
		}
	}
	if !strings.Contains(out, "supervision disabled") || strings.Contains(out, "Complete and verify the fictional example task.") {
		t.Fatal("preview must show disabled state without echoing task")
	}
	for _, name := range []string{"", "keepgoing.yaml", "prompt.md"} {
		info, err := os.Stat(filepath.Join(f.destination, name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if name == "" {
			want = 0o700
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != want {
			t.Errorf("%s permissions: %v", name, info.Mode())
		}
	}
	// A second invocation must leave the entire existing setup untouched.
	before, _ := os.ReadFile(cfg.Loop.PromptFile)
	code, _, _ = f.run(t, append(f.args(), "--task", "Replacement must not be written."), "")
	after, _ := os.ReadFile(cfg.Loop.PromptFile)
	if code == 0 || !bytes.Equal(before, after) {
		t.Fatal("existing setup was replaced")
	}
	// Exercise the generated config through independent CLI invocations. Even
	// enabling a completed loop must never reach the nonexistent provider CLI.
	for _, cmd := range []string{"complete", "enable", "tick", "tick"} {
		var errOut bytes.Buffer
		if code := run(context.Background(), []string{cmd, "-c", filepath.Join(f.destination, "keepgoing.yaml")}, io.Discard, &errOut); code != 0 {
			t.Fatalf("generated config %s: %d %s", cmd, code, &errOut)
		}
	}
	gate, err = control.Load(cfg.Loop.StateDir)
	if err != nil || !gate.Completed || !gate.Enabled {
		t.Fatalf("completion did not persist: %+v %v", gate, err)
	}
}

func TestInitWizardAndFlagsProduceSameConfiguration(t *testing.T) {
	f := newSetupFixture(t)
	// Accept all limits, one account, and the current CLI model; no fallback.
	input := strings.Join([]string{"example-loop", f.project, "", "Complete and verify the fictional example task.", "codex", "", "", "", "", "", "", "", "", "", "", f.destination, ""}, "\n")
	code, _, errOut := f.run(t, nil, input)
	if code != 0 {
		t.Fatalf("wizard: %d %s", code, errOut)
	}
	want, err := config.Load(filepath.Join(f.destination, "keepgoing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.root, "script-loop")
	code, _, errOut = f.run(t, append(f.args(), "--directory", other), "")
	if code != 0 {
		t.Fatalf("flags: %d %s", code, errOut)
	}
	got, err := config.Load(filepath.Join(other, "keepgoing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got.Loop.StateDir = want.Loop.StateDir
	got.Loop.PromptFile = want.Loop.PromptFile
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("wizard and flags disagree:\n%+v\n%+v", want.Loop, got.Loop)
	}
}

func TestInitOrderedAccountsAndTaskFile(t *testing.T) {
	f := newSetupFixture(t)
	accounts := []string{filepath.Join(f.root, "account-a"), filepath.Join(f.root, "account-b"), filepath.Join(f.root, "account-c")}
	for _, p := range accounts {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "config.txt"), []byte("existing configuration"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	taskFile := filepath.Join(f.root, "task.md")
	if err := os.WriteFile(taskFile, []byte("Fictional task from a file.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append(f.args(), "--task", "", "--task-file", taskFile, "--agent", "claude", "--account-dir", accounts[0], "--group", "pool-a", "--model", "example-model", "--fallback", "codex:pool-b="+accounts[1], "--fallback", "claude:pool-c="+accounts[2], "--wake-every", "2h", "--probe-daily-cap", "3")
	code, _, errOut := f.run(t, args, "")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	cfg, err := config.Load(filepath.Join(f.destination, "keepgoing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.Claude.Env["CLAUDE_CONFIG_DIR"] != accounts[0] || cfg.Loop.Claude.PermissionMode != "" || cfg.Loop.Claude.Model != "example-model" {
		t.Fatalf("primary: %+v", cfg.Loop.Claude)
	}
	fb := cfg.FallbackSet
	if fb == nil || len(fb.Candidates) != 2 || fb.Candidates[0].Adapter != "codex" || fb.Candidates[1].Adapter != "claude" || fb.Redundancy.Span != "group" || fb.Probe.DailyCap != 3 {
		t.Fatalf("fallbacks: %+v", fb)
	}
	if fb.Candidates[0].ProbeEnv()["CODEX_HOME"] != accounts[1] || fb.Candidates[1].ProbeEnv()["CLAUDE_CONFIG_DIR"] != accounts[2] {
		t.Fatal("probe account does not match target")
	}
	if cfg.Loop.Progress.MinWakeInterval.Duration != 2*time.Hour || !reflect.DeepEqual(cfg.Loop.Errors.FailoverOn, []string{"rate_limit"}) || cfg.Loop.SwitchBack.Enabled {
		t.Fatal("incorrect operating limits")
	}
	for _, p := range accounts {
		b, _ := os.ReadFile(filepath.Join(p, "config.txt"))
		if string(b) != "existing configuration" {
			t.Fatal("account configuration changed")
		}
	}
}

func TestInitBBRequiresExplicitExistingTarget(t *testing.T) {
	f := newSetupFixture(t)
	args := append(f.args(), "--agent", "bb", "--thread", "thr_example", "--provider", "codex")
	code, _, errOut := f.run(t, args, "")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	cfg, err := config.Load(filepath.Join(f.destination, "keepgoing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.BB.Thread != "thr_example" || cfg.Loop.BB.Model != "" || cfg.Loop.Group != "codex" {
		t.Fatalf("BB target: %+v", cfg.Loop)
	}
}

func TestInitRejectsInvalidSetupWithoutWrites(t *testing.T) {
	f := newSetupFixture(t)
	for _, tc := range []struct {
		name  string
		extra []string
	}{
		{"missing task", []string{"--task", ""}}, {"two tasks", []string{"--task-file", "example.md"}},
		{"bad name", []string{"--name", "../escape"}}, {"unknown agent", []string{"--agent", "cursor"}},
		{"missing account", []string{"--account-dir", filepath.Join(f.root, "missing")}},
		{"BB no thread", []string{"--agent", "bb", "--provider", "codex"}},
		{"BB no provider", []string{"--agent", "bb", "--thread", "thr_example"}},
		{"BB changes model", []string{"--agent", "bb", "--thread", "thr_example", "--provider", "codex", "--model", "example"}},
		{"headless with thread", []string{"--thread", "thr_example"}},
		{"same quota pool", []string{"--fallback", "codex"}},
		{"duplicate quota pool", []string{"--fallback", "claude", "--fallback", "codex:claude-code"}},
		{"bad fallback", []string{"--fallback", "bb:example"}},
		{"ambiguous group", []string{"--group", "pool=example"}},
		{"zero wake", []string{"--wake-every", "0"}}, {"negative backoff", []string{"--backoff", "-1m"}},
		{"short interval", []string{"--interval", "1s"}}, {"zero nudges", []string{"--max-nudges", "0"}},
		{"zero probes", []string{"--probe-daily-cap", "0"}},
		{"inside project", []string{"--directory", filepath.Join(f.project, "private")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := f.run(t, append(f.args(), tc.extra...), "")
			if code == 0 {
				t.Fatal("invalid setup accepted")
			}
			if errOut == "" {
				t.Fatal("missing actionable error")
			}
			if _, err := os.Stat(f.destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid setup wrote files")
			}
		})
	}
}

func TestInitDryRunEOFAndMissingCLI(t *testing.T) {
	f := newSetupFixture(t)
	code, out, errOut := f.run(t, append(f.args(), "--dry-run"), "")
	if code != 0 || !strings.Contains(out, "Dry run") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Dir(f.destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry run wrote directories")
	}
	code, _, _ = f.run(t, nil, "")
	if code == 0 {
		t.Fatal("EOF accepted")
	}
	var b bytes.Buffer
	code = setupLoop(env{context.Background(), io.Discard, &b}, f.args(), strings.NewReader(""), func(string) (string, error) { return "", exec.ErrNotFound })
	if code == 0 || !strings.Contains(b.String(), "CLI not found") {
		t.Fatalf("missing CLI: %d %s", code, &b)
	}
}

func TestInitRefusesOtherCheckoutAndSymlink(t *testing.T) {
	f := newSetupFixture(t)
	other := filepath.Join(f.root, "other-checkout")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	// A .git file also marks a linked worktree.
	if err := os.WriteFile(filepath.Join(other, ".git"), []byte("gitdir: example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ := f.run(t, append(f.args(), "--directory", filepath.Join(other, "setup")), "")
	if code == 0 {
		t.Fatal("wrote to other checkout")
	}
	link := filepath.Join(f.root, "outside-link")
	if err := os.Symlink(other, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	code, _, _ = f.run(t, append(f.args(), "--directory", filepath.Join(link, "nested", "setup")), "")
	if code == 0 {
		t.Fatal("symlink bypassed checkout check")
	}
	if _, err := os.Stat(filepath.Join(other, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("symlink target modified")
	}
}

func TestInitExistingDestinationAndPermissions(t *testing.T) {
	f := newSetupFixture(t)
	if err := os.MkdirAll(f.destination, 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, _ := f.run(t, f.args(), "")
	if code == 0 {
		t.Fatal("existing empty destination replaced")
	}
	if err := os.Remove(f.destination); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Dir(f.destination), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	code, _, errOut := f.run(t, f.args(), "")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	info, err := os.Stat(filepath.Dir(f.destination))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o750 {
		t.Fatal("changed existing parent permissions")
	}
	if err := writeInit(f.destination, map[string][]byte{"prompt.md": []byte("replacement")}); err == nil {
		t.Fatal("write race overwrote existing setup")
	}
}

func TestInitRejectsUnusableProject(t *testing.T) {
	f := newSetupFixture(t)
	code, _, errOut := f.run(t, append(f.args(), "--project", f.root), "")
	if code == 0 || !strings.Contains(errOut, "initial commit") {
		t.Fatalf("non-Git project: %d %s", code, errOut)
	}
}

func TestInitCancellationWhileReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() { _, err := readInitLine(ctx, bufio.NewReader(reader)); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wizard ignored cancellation")
	}
}
