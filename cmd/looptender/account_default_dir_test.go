package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

func TestAccountExplicitIsDefault(t *testing.T) {
	const home = "/example/home"
	for _, tc := range []struct {
		name     string
		provider string
		mode     string
		dir      string
		want     bool
	}{
		{"claude code default directory", "claude-code", "explicit_directory", "/example/home/.claude", true},
		{"claude code default directory, uncleaned", "claude-code", "explicit_directory", "/example/home/./.claude/", true},
		{"claude code second directory", "claude-code", "explicit_directory", "/example/home/.claude-work", false},
		{"claude code directory under the default", "claude-code", "explicit_directory", "/example/home/.claude/profiles/a", false},
		{"claude code inherited", "claude-code", "inherited", "", false},
		{"codex default directory is not claimed", "codex", "explicit_directory", "/example/home/.codex", false},
		{"codex in the claude default is not claimed", "codex", "explicit_directory", "/example/home/.claude", false},
		{"external provider", "bb", "externally_managed", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := accounts.Entry{Provider: tc.provider, Mode: tc.mode, Directory: tc.dir}
			if got := accountExplicitIsDefault(e, home); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
	// Windows paths compare without regard to case; elsewhere case counts.
	upper := accounts.Entry{Provider: "claude-code", Mode: "explicit_directory", Directory: `C:\Users\Example\.CLAUDE`}
	if got, want := accountExplicitIsDefault(upper, `c:\users\example`), runtime.GOOS == "windows"; got != want {
		t.Errorf("case-different default: got %v, want %v", got, want)
	}
	if accountExplicitIsDefault(accounts.Entry{Provider: "claude-code", Mode: "explicit_directory", Directory: "/.claude"}, "") {
		t.Error("an unknown home matched")
	}
}

// The default is compared after resolving symlinks, as the entry's directory
// was when it was selected.
func TestAccountExplicitIsDefaultResolvesSymlinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "real-home")
	if err := os.MkdirAll(filepath.Join(real, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "home")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	e := accounts.Entry{Provider: "claude-code", Mode: "explicit_directory", Directory: filepath.Join(real, ".claude")}
	if !accountExplicitIsDefault(e, link) {
		t.Error("the default under a symlinked home was not recognized")
	}
}

func TestAccountsAddWarnsOnExplicitDefaultDirectory(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_actual")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	for _, d := range []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude-work"), filepath.Join(home, ".codex")} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// os.UserHomeDir reads HOME, or USERPROFILE on Windows.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cli := filepath.Join(root, "fixture-cli.exe")
	if err := os.WriteFile(cli, []byte("fictional stand-in"), 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "catalog")
	add := func(provider, accountDir string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		e := env{ctx: context.Background(), stdout: &out, stderr: &errOut}
		if code := accountsCommand(e, []string{"add", "--directory", dir, "--provider", provider, "--selection", "explicit_directory", "--account-dir", accountDir, "--cli", cli, "--workdir", root, "--label", "Example studio", "--json"}, nil); code != exitOK {
			t.Fatalf("add: %s", errOut.String())
		}
		return errOut.String()
	}
	if got := add("claude-code", filepath.Join(home, ".claude")); !strings.Contains(got, "warning: "+accountDefaultDirWarning) || !strings.Contains(got, "Use --selection inherited for the default login") {
		t.Errorf("no warning for the default directory: %q", got)
	}
	if got := add("claude-code", filepath.Join(home, ".claude-work")); got != "" {
		t.Errorf("warning for a second directory: %q", got)
	}
	if got := add("codex", filepath.Join(home, ".codex")); got != "" {
		t.Errorf("warning for codex: %q", got)
	}
	f, err := accounts.Load(dir)
	if err != nil || len(f.Entries) != 3 {
		t.Fatalf("the warning refused an entry: %v %d", err, len(f.Entries))
	}

	// update warns the same way, and still saves.
	var out, errOut bytes.Buffer
	e := env{ctx: context.Background(), stdout: &out, stderr: &errOut}
	second := f.Entries[1]
	if code := accountsCommand(e, []string{"update", "--directory", dir, "--id", second.ID, "--expect", second.Revision, "--provider", "claude-code", "--selection", "explicit_directory", "--account-dir", filepath.Join(home, ".claude"), "--cli", cli, "--workdir", root, "--label", "Example studio"}, nil); code != exitOK {
		t.Fatalf("update: %s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "warning: "+accountDefaultDirWarning) {
		t.Errorf("no warning on update: %q", errOut.String())
	}
}

func TestDoctorWarnsOnExplicitDefaultDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "catalog")
	var flagged, second, inherited accounts.Entry
	err = accounts.Update(dir, func(f *accounts.File) error {
		now := time.Now().UTC()
		flagged = accounts.NewEntry(*f, accounts.Entry{Label: "Example studio", Provider: "claude-code", Mode: "explicit_directory", CLI: "/example/bin/claude", Workdir: "/example/work", Directory: "/example/home/.claude"}, "thr_example_actual", now, nil)
		f.Entries = append(f.Entries, flagged)
		second = accounts.NewEntry(*f, accounts.Entry{Label: "Example workshop", Provider: "claude-code", Mode: "explicit_directory", CLI: "/example/bin/claude", Workdir: "/example/work", Directory: "/example/home/.claude-work"}, "thr_example_actual", now, nil)
		f.Entries = append(f.Entries, second)
		inherited = accounts.NewEntry(*f, accounts.Entry{Label: "Example default", Provider: "claude-code", Mode: "inherited", CLI: "/example/bin/claude", Workdir: "/example/work"}, "thr_example_actual", now, nil)
		f.Entries = append(f.Entries, inherited)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var r doctorReport
	doctorAccountDefaultDirectories(&r, dir, "/example/home")
	if len(r.Checks) != 1 {
		t.Fatalf("checks: %+v", r.Checks)
	}
	c := r.Checks[0]
	if c.ID != "accounts."+flagged.ID+".directory" || c.Status != "warning" || !strings.Contains(c.Message, accountDefaultDirWarning) || c.Fix != accountDefaultDirFix {
		t.Errorf("check: %+v", c)
	}
	if strings.Contains(c.Message+c.Fix, "/example/home") {
		t.Errorf("doctor printed a directory: %+v", c)
	}

	// A missing catalog adds nothing and stops nothing.
	var none doctorReport
	doctorAccountDefaultDirectories(&none, filepath.Join(root, "absent"), "/example/home")
	if len(none.Checks) != 0 {
		t.Errorf("checks for a missing catalog: %+v", none.Checks)
	}
}
