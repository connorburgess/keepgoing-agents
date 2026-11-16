package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/registry"
)

// A fallbacks command names a registered loop with --loop, as the routes
// commands do, and then acts exactly as with -c and that loop's file.
func TestFallbacksByLoopName(t *testing.T) {
	f := newManageFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	reg := filepath.Join(t.TempDir(), "registry")
	if err := os.Mkdir(reg, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Add(reg, cfg.Loop.Name, f.cfg, cfg.Loop.StateDir); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) (int, string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := run(context.Background(), args, &out, &errOut)
		return code, out.String(), errOut.String()
	}

	// The positional ID may come before or after the flags.
	if code, out, errOut := call("fallbacks", "move", "third", "--to", "1", "--loop", "example-loop", "--directory", reg, "--yes"); code != exitOK || !strings.Contains(out, "Moved fallback third to place 1 of 3.") {
		t.Fatalf("move by name: %d %s %s", code, out, errOut)
	}
	if code, out, errOut := call("fallbacks", "move", "--loop=example-loop", "--directory="+reg, "--to", "3", "--yes", "third"); code != exitOK || !strings.Contains(out, "Moved fallback third to place 3 of 3.") {
		t.Fatalf("move with flags first: %d %s %s", code, out, errOut)
	}
	if got := f.ids(t); got != "first,second,third" {
		t.Fatalf("order %s", got)
	}
	if code, out, errOut := call("fallbacks", "handoff", "off", "--loop", "example-loop", "--directory", reg, "--dry-run"); code != exitOK || !strings.Contains(out, "nothing written") {
		t.Fatalf("handoff by name: %d %s %s", code, out, errOut)
	}
	if code, out, errOut := call("fallbacks", "list", "--loop", "example-loop", "--directory", reg); code != exitOK || !strings.Contains(out, "third") {
		t.Fatalf("list by name: %d %s %s", code, out, errOut)
	}
	if err := os.MkdirAll(f.account("claude-d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := call("fallbacks", "add", "--loop", "example-loop", "--directory", reg, "--non-interactive", "--agent", "claude", "--account-dir", f.account("claude-d"), "--group", "pool-d", "--dry-run"); code != exitOK || !strings.Contains(out, "pool-d") {
		t.Fatalf("add by name: %d %s %s", code, out, errOut)
	}

	before := f.files(t)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"unknown loop":        {[]string{"fallbacks", "move", "third", "--to", "1", "--loop", "absent-loop", "--directory", reg, "--yes"}, `no registered loop named "absent-loop"`},
		"both -c and --loop":  {[]string{"fallbacks", "move", "third", "--to", "1", "--loop", "example-loop", "--directory", reg, "-c", f.cfg, "--yes"}, "pass either --loop NAME or -c FILE, not both"},
		"directory alone":     {[]string{"fallbacks", "move", "third", "--to", "1", "--directory", reg, "-c", f.cfg, "--yes"}, "--directory applies only with --loop NAME"},
		"loop without a name": {[]string{"fallbacks", "list", "--loop"}, "--loop takes one registered loop name"},
		"loop twice":          {[]string{"fallbacks", "list", "--loop", "example-loop", "--loop", "example-loop", "--directory", reg}, "--loop takes one registered loop name"},
	} {
		if code, out, errOut := call(tc.args...); code != exitUsage || out != "" || !strings.Contains(errOut, tc.want) {
			t.Errorf("%s: %d %q %q", name, code, out, errOut)
		}
	}
	if f.files(t) != before {
		t.Fatal("a refused --loop request changed the loop's files")
	}

	// A registry that cannot be read is said to be so, without a path.
	bad := filepath.Join(t.TempDir(), "registry")
	if err := os.Mkdir(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(bad, registry.FileName), "not a registry", 0o600)
	if code, out, errOut := call("fallbacks", "list", "--loop", "example-loop", "--directory", bad); code != exitFailed || out != "" || !strings.Contains(errOut, "The loop registry cannot be read.") || strings.Contains(errOut, bad) {
		t.Errorf("unreadable registry: %d %q %q", code, out, errOut)
	}

	// A configuration moved since it was registered is refused as routes
	// --loop refuses it: by name, never by its path.
	moved := f.cfg + ".moved"
	if err := os.Rename(f.cfg, moved); err != nil {
		t.Fatal(err)
	}
	for _, sub := range [][]string{{"list"}, {"move", "third", "--to", "1", "--yes"}, {"add", "--non-interactive", "--agent", "claude", "--dry-run"}} {
		args := append(append([]string{"fallbacks"}, sub...), "--loop", "example-loop", "--directory", reg)
		code, out, errOut := call(args...)
		if code != exitUsage || out != "" || !strings.Contains(errOut, "Loop example-loop's configuration cannot be loaded; inspect it with keepgoing validate.") || strings.Contains(errOut, f.destination) {
			t.Errorf("%v with a moved configuration: %d %q %q", sub, code, out, errOut)
		}
	}
}

// Two registrations with one loop name are ambiguous: the command says so
// rather than pick one.
func TestFallbacksByLoopNameTwice(t *testing.T) {
	a, b := newManageFixture(t), newManageFixture(t)
	reg := filepath.Join(t.TempDir(), "registry")
	if err := os.Mkdir(reg, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []addFixture{a, b} {
		cfg, err := config.Load(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Add(reg, cfg.Loop.Name, f.cfg, cfg.Loop.StateDir); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"fallbacks", "list", "--loop", "example-loop", "--directory", reg}, &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "2 registered loops are named") {
		t.Fatalf("%d %s %s", code, out.String(), errOut.String())
	}
}
