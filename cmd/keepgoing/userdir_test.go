package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

// userHome gives a test a user of its own, with no directory of the tool's
// in it, on every platform, and returns that user's configuration and state
// directories.
func userHome(t *testing.T) (config, state string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, ".local", "state"))
	t.Setenv("AppData", filepath.Join(root, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "AppData", "Local"))
	if config, err = userdir.ConfigHome(); err != nil {
		t.Fatal(err)
	}
	if state, err = userdir.StateHome(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{config, state} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return config, state
}

// The registry's default place: looptender/registry on a computer with no
// earlier files, and keepgoing/registry once the earlier directory exists
// under either the configuration or the state directory.
func TestRegistryDefaultDirectoryFollowsTheEarlierName(t *testing.T) {
	config, state := userHome(t)
	t.Setenv(registryDirEnv, "")
	t.Setenv(envvar.RegistryDir.Legacy(), "")
	if dir, err := registryDirectory(""); err != nil || dir != filepath.Join(config, "looptender", "registry") {
		t.Fatalf("a new install: %q %v", dir, err)
	}
	if _, err := os.Lstat(filepath.Join(config, "looptender")); err == nil {
		t.Fatal("resolving the directory created it")
	}
	// Only session claims from before the rename: the registry follows.
	if err := os.MkdirAll(filepath.Join(state, "keepgoing", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if dir, err := registryDirectory(""); err != nil || dir != filepath.Join(config, "keepgoing", "registry") {
		t.Fatalf("an earlier directory: %q %v", dir, err)
	}
}

// init's default home for a new loop follows the same rule.
func TestInitDefaultDirectoryFollowsTheEarlierName(t *testing.T) {
	f := newSetupFixture(t)
	config, _ := userHome(t)
	args := []string{"--non-interactive", "--name", "example-loop", "--agent", "codex", "--project", f.project, "--task", "Complete and verify the fictional example task.", "--dry-run", "--json"}
	directory := func() string {
		t.Helper()
		code, out, errOut := f.run(t, args, "")
		var v struct {
			ConfigPath string `json:"configPath"`
		}
		if err := json.Unmarshal([]byte(out), &v); code != 0 || err != nil || v.ConfigPath == "" {
			t.Fatalf("%d %s %s %v", code, out, errOut, err)
		}
		return filepath.Dir(v.ConfigPath)
	}
	if got, want := directory(), filepath.Join(config, "looptender", "loops", "example-loop"); got != want {
		t.Fatalf("a new install: %q, want %q", got, want)
	}
	if err := os.MkdirAll(filepath.Join(config, "keepgoing", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, want := directory(), filepath.Join(config, "keepgoing", "loops", "example-loop"); got != want {
		t.Fatalf("an earlier directory: %q, want %q", got, want)
	}
	if _, err := os.Lstat(filepath.Join(config, "looptender")); err == nil {
		t.Fatal("a dry run created the directory")
	}
}

// Doctor says which name the per-user directories go by, and warns when
// directories of both names exist.
func TestDoctorUserDirectory(t *testing.T) {
	check := func(f userdir.Found) doctorCheck {
		var r doctorReport
		doctorUserDirectory(&r, f)
		if len(r.Checks) != 1 || r.Checks[0].ID != "user.directory" {
			t.Fatalf("%+v", r.Checks)
		}
		return r.Checks[0]
	}
	if c := check(userdir.Found{Name: "looptender"}); c.Status != "ok" || !strings.Contains(c.Message, "named looptender") || strings.Contains(c.Message, "keepgoing") {
		t.Fatalf("a new install: %+v", c)
	}
	if c := check(userdir.Found{Name: "keepgoing", Used: []string{"/private/example/config/keepgoing"}}); c.Status != "ok" || !strings.Contains(c.Message, "named keepgoing") || !strings.Contains(c.Message, "/private/example/config/keepgoing") || !strings.Contains(c.Message, "Nothing is moved") {
		t.Fatalf("an earlier directory: %+v", c)
	}
	c := check(userdir.Found{Name: "keepgoing", Used: []string{"/private/example/config/keepgoing"}, Ignored: []string{"/private/example/state/looptender"}})
	if c.Status != "warning" || !strings.Contains(c.Message, "nothing in /private/example/state/looptender is read") || !strings.Contains(c.Fix, "/private/example/state/looptender") {
		t.Fatalf("both names: %+v", c)
	}
}
