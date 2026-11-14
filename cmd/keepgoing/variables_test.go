package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/envvar"
)

// The registry variable is still read under its earlier name. The two names
// set to different directories are refused, naming both: a command that
// honoured one would not see the loops registered in the other. An explicit
// --directory is not affected.
func TestRegistryDirectoryReadsTheEarlierName(t *testing.T) {
	earlier := envvar.RegistryDir.Legacy()
	old, current := filepath.Join(t.TempDir(), "registry"), filepath.Join(t.TempDir(), "registry")
	want := func(p string) string {
		t.Helper()
		c, err := canonicalFuturePath(p)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	t.Setenv(registryDirEnv, "")
	t.Setenv(earlier, old)
	if dir, err := registryDirectory(""); err != nil || dir != want(old) {
		t.Fatalf("the earlier name alone: %q %v", dir, err)
	}
	t.Setenv(registryDirEnv, current)
	if _, err := registryDirectory(""); err == nil || !strings.Contains(err.Error(), registryDirEnv) || !strings.Contains(err.Error(), earlier) || strings.Contains(err.Error(), old) {
		t.Fatalf("both names, different directories: %v", err)
	}
	if dir, err := registryDirectory(old); err != nil || dir != want(old) {
		t.Fatalf("an explicit directory: %q %v", dir, err)
	}
	t.Setenv(earlier, current)
	if dir, err := registryDirectory(""); err != nil || dir != want(current) {
		t.Fatalf("both names, one directory: %q %v", dir, err)
	}
}

// Doctor says when a variable's two names disagree and which earlier names
// are in use, and never prints a value.
func TestDoctorVariables(t *testing.T) {
	clear := func(t *testing.T) {
		for _, v := range envvar.Set {
			t.Setenv(v.Name(), "")
			t.Setenv(v.Legacy(), "")
		}
	}
	checks := func() map[string]doctorCheck {
		var r doctorReport
		doctorVariables(&r)
		found := map[string]doctorCheck{}
		for _, c := range r.Checks {
			found[c.ID] = c
		}
		return found
	}

	t.Run("nothing set", func(t *testing.T) {
		clear(t)
		if found := checks(); len(found) != 0 {
			t.Fatalf("%+v", found)
		}
	})
	t.Run("current names only", func(t *testing.T) {
		clear(t)
		t.Setenv(envvar.NetProbe.Name(), "exit 0")
		t.Setenv(envvar.RegistryDir.Name(), "/private/example/registry")
		if found := checks(); len(found) != 0 {
			t.Fatalf("%+v", found)
		}
	})
	t.Run("earlier names in use", func(t *testing.T) {
		clear(t)
		t.Setenv(envvar.NetProbe.Legacy(), "exit 0")
		t.Setenv(envvar.WebhookSecret.Legacy(), "fictional-secret-value")
		found := checks()
		c := found["environment.earlier_names"]
		if len(found) != 1 || c.Status != "ok" || !strings.Contains(c.Message, "KEEPGOING_NET_PROBE, KEEPGOING_WEBHOOK_SECRET") || !strings.Contains(c.Message, "still work") {
			t.Fatalf("%+v", found)
		}
		if strings.Contains(c.Message, "fictional-secret-value") {
			t.Fatalf("a value was printed: %s", c.Message)
		}
	})
	t.Run("a setting named twice, differently", func(t *testing.T) {
		clear(t)
		t.Setenv(envvar.WebhookSecret.Name(), "fictional-new-secret")
		t.Setenv(envvar.WebhookSecret.Legacy(), "fictional-old-secret")
		found := checks()
		c := found["environment.webhook_secret"]
		if len(found) != 1 || c.Status != "warning" || !strings.Contains(c.Message, "LOOPTENDER_WEBHOOK_SECRET is the one used") || c.Fix != "Unset KEEPGOING_WEBHOOK_SECRET." {
			t.Fatalf("%+v", found)
		}
		if strings.Contains(c.Message+c.Fix, "fictional-") {
			t.Fatalf("a value was printed: %+v", c)
		}
	})
	t.Run("a location named twice, differently", func(t *testing.T) {
		clear(t)
		t.Setenv(envvar.RegistryDir.Name(), "/private/example/new")
		t.Setenv(envvar.RegistryDir.Legacy(), "/private/example/old")
		t.Setenv(envvar.RoutesFile.Name(), "/private/example/new/routes.yaml")
		t.Setenv(envvar.RoutesFile.Legacy(), "/private/example/old/routes.yaml")
		found := checks()
		for _, id := range []string{"environment.registry_dir", "environment.routes_file"} {
			if c := found[id]; c.Status != "error" || !strings.Contains(c.Message, "refuse") || strings.Contains(c.Message+c.Fix, "/private/example") {
				t.Fatalf("%s: %+v", id, c)
			}
		}
		if len(found) != 2 {
			t.Fatalf("%+v", found)
		}
	})
	// Doctor as a whole does not also say a registry is in use when the two
	// names disagree: none is.
	t.Run("no registry is named as in use when its names disagree", func(t *testing.T) {
		clear(t)
		f := newFixture(t)
		t.Setenv(envvar.RegistryDir.Name(), filepath.Join(f.dir, "registry-new"))
		t.Setenv(envvar.RegistryDir.Legacy(), filepath.Join(f.dir, "registry-old"))
		_, out, _ := f.run("doctor", "--json")
		if strings.Contains(out, `"registry.directory"`) || !strings.Contains(out, `"environment.registry_dir"`) {
			t.Fatalf("%s", out)
		}
	})
	t.Run("a location named twice, the same place", func(t *testing.T) {
		clear(t)
		t.Setenv(envvar.RegistryDir.Name(), "/private/example/one")
		t.Setenv(envvar.RegistryDir.Legacy(), "/private/example/one/")
		if found := checks(); len(found) != 0 {
			t.Fatalf("%+v", found)
		}
	})
	// The session directory's disagreement is reported once, by the check
	// that says claims cannot be kept.
	t.Run("the session directory is left to its own check", func(t *testing.T) {
		clear(t)
		t.Setenv(envvar.SessionDir.Name(), "/private/example/new")
		t.Setenv(envvar.SessionDir.Legacy(), "/private/example/old")
		if found := checks(); len(found) != 0 {
			t.Fatalf("%+v", found)
		}
		status, message, fix := sessionDirectoryCheck()
		if status != "error" || !strings.Contains(message, "LOOPTENDER_SESSION_DIR and KEEPGOING_SESSION_DIR") || !strings.Contains(fix, "unset one of them") || strings.Contains(message+fix, "/private/example") {
			t.Fatalf("%s: %s %s", status, message, fix)
		}
	})
	t.Run("the session directory under its earlier name", func(t *testing.T) {
		clear(t)
		dir := filepath.Join(t.TempDir(), "sessions")
		t.Setenv(envvar.SessionDir.Legacy(), dir)
		if _, message, _ := sessionDirectoryCheck(); !strings.HasPrefix(message, "KEEPGOING_SESSION_DIR is set") {
			t.Fatalf("%s", message)
		}
	})
}

// The route list's refusal for two disagreeing names says which variables.
func TestRoutesNamedTwiceText(t *testing.T) {
	text := routesProblemText(config.RoutesNamedTwice)
	if !strings.Contains(text, "LOOPTENDER_ROUTES_FILE") || !strings.Contains(text, "KEEPGOING_ROUTES_FILE") {
		t.Fatalf("%s", text)
	}
}
