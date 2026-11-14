package main

import (
	"bytes"
	"context"
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

// Doctor says which name the per-user directories go by and why, warns
// when the tool's own files exist under the other name too, and never
// suggests removing anything.
func TestDoctorUserDirectory(t *testing.T) {
	check := func(f userdir.Found) doctorCheck {
		var r doctorReport
		doctorUserDirectory(&r, f)
		if len(r.Checks) != 1 || r.Checks[0].ID != "user.directory" {
			t.Fatalf("%+v", r.Checks)
		}
		c := r.Checks[0]
		for _, word := range []string{"remove it", "Remove ", "delete", "()"} {
			if strings.Contains(c.Message+" "+c.Fix, word) {
				t.Fatalf("says %q: %+v", word, c)
			}
		}
		return c
	}
	if c := check(userdir.Found{Name: "looptender"}); c.Status != "ok" || !strings.Contains(c.Message, "named looptender") || strings.Contains(c.Message, "keepgoing") {
		t.Fatalf("a new install: %+v", c)
	}
	if c := check(userdir.Found{Name: "keepgoing", Used: []string{"/private/example/config/keepgoing/registry"}}); c.Status != "ok" || !strings.Contains(c.Message, "named keepgoing") || !strings.Contains(c.Message, "already has them there: /private/example/config/keepgoing/registry") || !strings.Contains(c.Message, "Nothing is moved") {
		t.Fatalf("an earlier directory: %+v", c)
	}
	// A directory that could not be inspected is the reason, said as such
	// and by its own path, and it is a warning: the name was not decided
	// from what is there.
	if c := check(userdir.Found{Name: "keepgoing", Unknown: []string{"/private/example/config/keepgoing"}}); c.Status != "warning" || !strings.Contains(c.Message, "/private/example/config/keepgoing could not be inspected, which counts as the earlier name") || !strings.Contains(c.Fix, "Make /private/example/config/keepgoing readable") {
		t.Fatalf("a directory that cannot be inspected: %+v", c)
	}
	c := check(userdir.Found{Name: "keepgoing", Used: []string{"/private/example/config/keepgoing/registry"}, Unread: []string{"/private/example/config/looptender/registry", "/private/example/config/looptender/routes.yaml"}})
	if c.Status != "warning" || !strings.Contains(c.Message, "are not read while that is so: /private/example/config/looptender/registry, /private/example/config/looptender/routes.yaml") || !strings.Contains(c.Message, "loops run by this release or later never act on one session at once") || !strings.Contains(c.Fix, "Nothing is removed or moved for you") {
		t.Fatalf("both names: %+v", c)
	}
	// The two names as one directory: said, and not a warning.
	if c := check(userdir.Found{Name: "keepgoing", Used: []string{"/private/example/config/keepgoing/registry"}, Linked: []string{"/private/example/config"}}); c.Status != "ok" || !strings.Contains(c.Message, "In /private/example/config the two names are one directory") {
		t.Fatalf("linked names: %+v", c)
	}
}

// Doctor reports the per-user directories even when no configuration can be
// read: they belong to no loop.
func TestDoctorUserDirectoryWithoutAConfiguration(t *testing.T) {
	userHome(t)
	var out, errOut bytes.Buffer
	run(context.Background(), []string{"doctor", "--json", "-c", filepath.Join(t.TempDir(), "absent.yaml")}, &out, &errOut)
	var v struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	found := map[string]doctorCheck{}
	for _, c := range v.Checks {
		found[c.ID] = c
	}
	if found["config"].Status != "error" || found["user.directory"].Status != "ok" || !strings.Contains(found["user.directory"].Message, "named looptender") {
		t.Fatalf("%+v", v.Checks)
	}
}

// The second-loop guard reads the registry in use and, when that is the
// default one, the registry under the tool's other directory name. A
// registry a person chose has no other name.
func TestRegistriesUnderBothNames(t *testing.T) {
	config, state := userHome(t)
	t.Setenv(registryDirEnv, "")
	t.Setenv(envvar.RegistryDir.Legacy(), "")
	current, earlier := filepath.Join(config, "looptender", "registry"), filepath.Join(config, "keepgoing", "registry")
	if got := registriesUnderBothNames(); len(got) != 1 || got[0] != current {
		t.Fatalf("a new install: %v", got)
	}
	if err := os.MkdirAll(current, 0o700); err != nil {
		t.Fatal(err)
	}
	// The earlier name comes into use: its registry first, the other after.
	if err := os.MkdirAll(filepath.Join(state, "keepgoing", "panel"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := registriesUnderBothNames(); len(got) != 2 || got[0] != earlier || got[1] != current {
		t.Fatalf("after the name changed: %v", got)
	}
	chosen := filepath.Join(filepath.Dir(config), "chosen-registry")
	t.Setenv(registryDirEnv, chosen)
	if got := registriesUnderBothNames(); len(got) != 1 || got[0] != chosen {
		t.Fatalf("a chosen registry: %v", got)
	}
}
