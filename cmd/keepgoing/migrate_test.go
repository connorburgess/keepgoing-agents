package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

// migrateHome is a user from before the rename: a registry with a loop kept
// inside the per-user directory and one kept elsewhere, the standard route
// list, and session claims. It returns the configuration and state
// directories and the registered loops' IDs.
type migrateHome struct {
	config, state     string
	inside, elsewhere registry.Entry
	outsideState      string
}

func newMigrateHome(t *testing.T) migrateHome {
	t.Helper()
	config, state := userHome(t)
	if err := (userdir.Place{Base: config}).CanLink(); err != nil {
		t.Skip("links cannot be made here")
	}
	// Tests run with the registry and claims moved aside; this one is about
	// the default places.
	for _, v := range envvar.Locations {
		t.Setenv(v.Name(), "")
		t.Setenv(v.Legacy(), "")
	}
	h := migrateHome{config: config, state: state}
	put := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	earlier := filepath.Join(config, "keepgoing")
	put(filepath.Join(earlier, "routes.yaml"), "version: 1\nroutes: []\n")
	put(filepath.Join(earlier, "loops", "example-inside", "keepgoing.yaml"), "fictional")
	put(filepath.Join(earlier, "loops", "example-inside", "state", "state.json"), "{}")
	put(filepath.Join(state, "keepgoing", "sessions", "0123456789abcdef0123456789abcdef.holder"), "{}")
	h.outsideState = filepath.Join(filepath.Dir(config), "elsewhere", "state")
	put(filepath.Join(h.outsideState, "state.json"), "{}")
	dir := filepath.Join(earlier, "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var err error
	if h.inside, err = registry.Add(dir, "example-inside", filepath.Join(earlier, "loops", "example-inside", "keepgoing.yaml"), filepath.Join(earlier, "loops", "example-inside", "state")); err != nil {
		t.Fatal(err)
	}
	if h.elsewhere, err = registry.Add(dir, "example-elsewhere", filepath.Join(filepath.Dir(h.outsideState), "keepgoing.yaml"), h.outsideState); err != nil {
		t.Fatal(err)
	}
	return h
}

// tree lists every entry under the user's directories, links as links.
func (h migrateHome) tree(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, base := range []string{h.config, h.state} {
		filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				t.Fatal(err)
			}
			kind := "file"
			if d.IsDir() {
				kind = "dir"
			} else if d.Type()&os.ModeSymlink != 0 {
				kind = "link"
			}
			out = append(out, kind+" "+path)
			return nil
		})
	}
	sort.Strings(out)
	return out
}

func migrateRun(t *testing.T, args ...string) (int, migrateOutput, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), append([]string{"migrate", "--json"}, args...), &out, &errOut)
	var v migrateOutput
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s %s", err, out.String(), errOut.String())
	}
	var text bytes.Buffer
	run(context.Background(), append([]string{"migrate"}, args...), &text, &errOut)
	return code, v, text.String()
}

func same(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

// The whole journey on this platform's real places: a preview that changes
// nothing, the move, what the tool then finds, a second run with nothing to
// do, and the undo, which leaves everything as it was.
func TestMigrateMovesAndMovesBack(t *testing.T) {
	h := newMigrateHome(t)
	before := h.tree(t)
	places := 2
	if h.config == h.state {
		places = 1
	}

	// Without --apply: what would be done, and nothing done. (migrateRun
	// runs the command twice, for JSON and for text.)
	code, out, text := migrateRun(t)
	if code != exitOK || out.Outcome != "ready" || !out.DryRun || len(out.Places) != places || len(out.Refusals) != 0 {
		t.Fatalf("preview: %d %+v", code, out)
	}
	for _, p := range out.Places {
		if p.Action != "move" || p.From != filepath.Join(p.Base, "keepgoing") || p.To != filepath.Join(p.Base, "looptender") || p.Done {
			t.Fatalf("preview: %+v", p)
		}
	}
	if !strings.Contains(text, "Would move "+filepath.Join(h.config, "keepgoing")) || !strings.Contains(text, "Nothing was changed") || !strings.Contains(text, "--apply") {
		t.Fatalf("preview text: %s", text)
	}
	if after := h.tree(t); !same(before, after) {
		t.Fatalf("a preview changed something:\n%v\n%v", before, after)
	}

	// The move. (Its second, text, run finds nothing left to do.)
	code, out, text = migrateRun(t, "--apply")
	if code != exitOK || out.Outcome != "done" || out.DryRun || out.References != 1 {
		t.Fatalf("apply: %d %+v", code, out)
	}
	if !strings.Contains(text, "Nothing to do") {
		t.Fatalf("a second apply: %s", text)
	}
	for _, base := range []string{h.config, h.state} {
		if target, err := os.Readlink(filepath.Join(base, "keepgoing")); err != nil || target != "looptender" {
			t.Fatalf("no link left in %s: %q %v", base, target, err)
		}
	}
	// The tool now uses the current name, and finds what it had.
	if userdir.DirName() != "looptender" {
		t.Fatal("the earlier name is still in use")
	}
	dir, err := registryDirectory("")
	if err != nil || dir != filepath.Join(h.config, "looptender", "registry") {
		t.Fatalf("registry: %q %v", dir, err)
	}
	f, err := registry.Load(dir)
	if err != nil || len(f.Entries) != 2 {
		t.Fatalf("%+v %v", f, err)
	}
	moved := filepath.Join(h.config, "looptender", "loops", "example-inside")
	for _, e := range f.Entries {
		switch e.ID {
		case h.inside.ID:
			// Rewritten to where its files resolve to now.
			if e.ConfigPath != filepath.Join(moved, "keepgoing.yaml") || e.StateDir != filepath.Join(moved, "state") {
				t.Fatalf("inside: %+v", e)
			}
			if resolved, err := canonicalFuturePath(h.inside.StateDir); err != nil || resolved != e.StateDir {
				t.Fatalf("the earlier path resolves to %q, the reference says %q", resolved, e.StateDir)
			}
		case h.elsewhere.ID:
			if e != h.elsewhere {
				t.Fatalf("a loop kept elsewhere was rewritten: %+v", e)
			}
		}
	}
	if sessions, _, err := session.Dir(); err != nil || sessions != filepath.Join(h.state, "looptender", "sessions") {
		t.Fatalf("sessions: %q %v", sessions, err)
	}
	// Paths recorded with the earlier name still reach the files.
	if _, err := os.Stat(h.inside.ConfigPath); err != nil {
		t.Fatalf("the earlier path no longer reaches the loop: %v", err)
	}
	var r doctorReport
	doctorUserDirectory(&r, userdir.Look())
	if c := r.Checks[0]; c.Status != "ok" || !strings.Contains(c.Message, "named looptender") || !strings.Contains(c.Message, "the two names are one directory") {
		t.Fatalf("doctor: %+v", c)
	}

	// The undo: a preview, then everything as it was.
	movedTree := h.tree(t)
	code, out, text = migrateRun(t, "--undo")
	if code != exitOK || out.Outcome != "ready" || out.Operation != "undo" || !strings.Contains(text, "--undo --apply") {
		t.Fatalf("undo preview: %d %+v %s", code, out, text)
	}
	if after := h.tree(t); !same(movedTree, after) {
		t.Fatal("an undo preview changed something")
	}
	code, out, _ = migrateRun(t, "--undo", "--apply")
	if code != exitOK || out.Outcome != "done" || out.References != 1 {
		t.Fatalf("undo: %d %+v", code, out)
	}
	if after := h.tree(t); !same(before, after) {
		t.Fatalf("the undo did not leave everything as it was:\n%v\n%v", before, after)
	}
	f, err = registry.Load(filepath.Join(h.config, "keepgoing", "registry"))
	if err != nil || len(f.Entries) != 2 || f.Entries[0] != h.inside || f.Entries[1] != h.elsewhere {
		t.Fatalf("the registry is not as it was: %+v %v", f, err)
	}
	if userdir.DirName() != "keepgoing" {
		t.Fatal("the earlier name is not in use again")
	}
}

// Nothing is moved while anything is using the directories or a loop this
// user runs is being checked or has a daemon. Each refusal says what, and
// nothing at all is changed.
func TestMigrateRefusesWhileInUse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, h migrateHome) (release func())
		code  string
		says  string
	}{
		{"a check holds a session claim", func(t *testing.T, h migrateHome) func() {
			l, err := lock.AcquireNamed(filepath.Join(h.state, "keepgoing", "sessions"), "0123456789abcdef0123456789abcdef")
			if err != nil {
				t.Fatal(err)
			}
			return func() { l.Release() }
		}, "in_use", "holds a session claim"},
		{"the panel is running", func(t *testing.T, h migrateHome) func() {
			l, err := lock.AcquireNamed(filepath.Join(h.state, "keepgoing", "panel"), panelServerLock)
			if err != nil {
				t.Fatal(err)
			}
			return func() { l.Release() }
		}, "in_use", "the panel is running"},
		{"a daemon of a loop kept inside", func(t *testing.T, h migrateHome) func() {
			l, err := lock.AcquireNamed(h.inside.StateDir, lock.Daemon)
			if err != nil {
				t.Fatal(err)
			}
			return func() { l.Release() }
		}, "in_use", "a daemon is running for a loop kept here"},
		{"a check of a loop kept inside", func(t *testing.T, h migrateHome) func() {
			l, err := lock.Acquire(h.inside.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			return func() { l.Release() }
		}, "in_use", "a check is running for a loop kept here"},
		{"a daemon of a registered loop kept elsewhere", func(t *testing.T, h migrateHome) func() {
			l, err := lock.AcquireNamed(h.outsideState, lock.Daemon)
			if err != nil {
				t.Fatal(err)
			}
			return func() { l.Release() }
		}, "loop_running", "its daemon is running"},
		{"a check of a registered loop kept elsewhere", func(t *testing.T, h migrateHome) func() {
			l, err := lock.Acquire(h.outsideState)
			if err != nil {
				t.Fatal(err)
			}
			return func() { l.Release() }
		}, "loop_running", "being checked right now"},
		{"an installed service for a loop kept inside", func(t *testing.T, h migrateHome) func() {
			if err := os.WriteFile(filepath.Join(h.inside.StateDir, "service-launchd.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			return func() {}
		}, "service_installed", "Remove the service"},
		{"directories of both names", func(t *testing.T, h migrateHome) func() {
			if err := os.MkdirAll(filepath.Join(h.config, "looptender", "registry"), 0o700); err != nil {
				t.Fatal(err)
			}
			return func() {}
		}, "both", "directories of both names exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMigrateHome(t)
			release := tc.setup(t, h)
			defer release()
			before := h.tree(t)
			for _, args := range [][]string{nil, {"--apply"}} {
				code, out, text := migrateRun(t, args...)
				if code != exitFailed || out.Outcome != "refused" || len(out.Refusals) == 0 {
					t.Fatalf("%v: %d %+v", args, code, out)
				}
				found := false
				for _, r := range out.Refusals {
					found = found || (r.Code == tc.code && strings.Contains(r.Message, tc.says))
				}
				if !found || !strings.Contains(text, "Nothing was changed") || !strings.Contains(text, tc.says) {
					t.Fatalf("%v: %+v\n%s", args, out.Refusals, text)
				}
				for _, p := range out.Places {
					if p.Done {
						t.Fatalf("%v: something was moved: %+v", args, p)
					}
				}
				if after := h.tree(t); !same(before, after) {
					t.Fatalf("%v: a refused move changed something:\n%v\n%v", args, before, after)
				}
			}
			// Once it is no longer in use, the same command goes through
			// (except where the cause is a file that is still there).
			release()
			if tc.code == "in_use" || tc.code == "loop_running" {
				if code, out, _ := migrateRun(t, "--apply"); code != exitOK || out.Outcome != "done" {
					t.Fatalf("after release: %d %+v", code, out)
				}
			}
		})
	}
}

// A loop that only ever claimed a session, and is in no registry, is found
// through the claim records, and its daemon stops the move too.
func TestMigrateFindsLoopsByTheirSessionRecords(t *testing.T) {
	h := newMigrateHome(t)
	unregistered := filepath.Join(filepath.Dir(h.config), "unregistered", "state")
	if err := os.MkdirAll(unregistered, 0o700); err != nil {
		t.Fatal(err)
	}
	id := session.Identity{Kind: "bb", ID: "thr_example"}
	if err := session.Note(filepath.Join(h.state, "keepgoing", "sessions"), id, session.Supervisor{Loop: "example-unregistered", Config: filepath.Join(filepath.Dir(unregistered), "keepgoing.yaml"), StateDir: unregistered}, 0); err != nil {
		t.Fatal(err)
	}
	l, err := lock.AcquireNamed(unregistered, lock.Daemon)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	code, out, _ := migrateRun(t, "--apply")
	if code != exitFailed || out.Outcome != "refused" || len(out.Refusals) != 1 || out.Refusals[0].Loop != "example-unregistered" || out.Refusals[0].Code != "loop_running" {
		t.Fatalf("%d %+v", code, out)
	}
}

// A computer with no directories, and one that started under the current
// name, have nothing to move in either direction, and nothing is created.
func TestMigrateNothingToDo(t *testing.T) {
	config, state := userHome(t)
	for _, v := range envvar.Locations {
		t.Setenv(v.Name(), "")
		t.Setenv(v.Legacy(), "")
	}
	for _, args := range [][]string{nil, {"--apply"}, {"--undo"}, {"--undo", "--apply"}} {
		if code, out, _ := migrateRun(t, args...); code != exitOK || out.Outcome != "nothing_to_do" {
			t.Fatalf("%v on an empty home: %d %+v", args, code, out)
		}
	}
	for _, base := range []string{config, state} {
		if list, _ := os.ReadDir(base); len(list) != 0 {
			t.Fatalf("something was created in %s: %v", base, list)
		}
	}
	if err := os.MkdirAll(filepath.Join(config, "looptender", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--apply"}, {"--undo", "--apply"}} {
		code, out, text := migrateRun(t, args...)
		if code != exitOK || out.Outcome != "nothing_to_do" || !strings.Contains(text, "was never moved") {
			t.Fatalf("%v on a current home: %d %+v %s", args, code, out, text)
		}
	}
	if _, err := os.Lstat(filepath.Join(config, "keepgoing")); err == nil {
		t.Fatal("a computer that started under the current name was given the earlier one")
	}
}

func TestMigrateUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"migrate", "extra"}, &out, &errOut); code != exitUsage {
		t.Fatalf("%d %s", code, errOut.String())
	}
	if code := run(context.Background(), []string{"migrate", "--unknown"}, &out, &errOut); code != exitUsage {
		t.Fatalf("%d", code)
	}
}

// A run stopped after the move and before the registry was rewritten (here:
// the move done by hand, as a killed run leaves it) is finished by running
// the same command again, which rewrites what is left, in a preview first.
func TestMigrateRewritesTheRegistryLeftByAStoppedRun(t *testing.T) {
	h := newMigrateHome(t)
	places, err := userdir.Places()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range places {
		if err := p.Move(); err != nil {
			t.Fatal(err)
		}
	}
	code, out, text := migrateRun(t)
	if code != exitOK || out.Outcome != "ready" || out.References != 1 || !strings.Contains(text, "Would rewrite 1 loop registry reference") {
		t.Fatalf("preview: %d %+v %s", code, out, text)
	}
	code, out, _ = migrateRun(t, "--apply")
	if code != exitOK || out.Outcome != "done" || out.References != 1 {
		t.Fatalf("apply: %d %+v", code, out)
	}
	f, err := registry.Load(filepath.Join(h.config, "looptender", "registry"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range f.Entries {
		if e.ID == h.inside.ID && !strings.Contains(e.StateDir, string(filepath.Separator)+"looptender"+string(filepath.Separator)) {
			t.Fatalf("not rewritten: %+v", e)
		}
	}
	if code, out, _ := migrateRun(t, "--apply"); code != exitOK || out.Outcome != "nothing_to_do" {
		t.Fatalf("a third run: %d %+v", code, out)
	}
}

// The registry's lock taken by another command at the moment of the
// rewrite: the move is kept, the run says the registry was not updated and
// to run again, and running again rewrites it.
func TestMigrateRewriteRetriedAfterAHeldRegistryLock(t *testing.T) {
	h := newMigrateHome(t)
	var held *lock.Lock
	migrateBeforeRewrite = func() {
		var err error
		held, err = lock.AcquireNamed(filepath.Join(h.config, "looptender", "registry"), registry.LockName)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { migrateBeforeRewrite = nil })
	var buf bytes.Buffer
	code := run(context.Background(), []string{"migrate", "--json", "--apply"}, &buf, &bytes.Buffer{})
	migrateBeforeRewrite = nil
	var out migrateOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if code != exitFailed || out.Outcome != "failed" || out.References != 0 || !strings.Contains(out.Message, "registry could not be updated") || !strings.Contains(out.Message, "Run the same command again") {
		t.Fatalf("%d %+v", code, out)
	}
	for _, p := range out.Places {
		if !p.Done {
			t.Fatalf("not reported as moved: %+v", p)
		}
	}
	held.Release()
	if code, out, _ := migrateRun(t, "--apply"); code != exitOK || out.Outcome != "done" || out.References != 1 {
		t.Fatalf("run again: %d %+v", code, out)
	}
}

// A move back refuses, changing nothing, while a loop's configuration names
// a path under looptender by its full path, as every loop init creates after
// the move does: the path would not exist afterwards. It names the file and
// the path. A configuration elsewhere, found through the registry, counts
// too.
func TestMigrateUndoRefusesWhileAConfigurationNamesTheCurrentName(t *testing.T) {
	for _, where := range []string{"inside", "registered elsewhere"} {
		t.Run(where, func(t *testing.T) {
			h := newMigrateHome(t)
			if code, out, _ := migrateRun(t, "--apply"); code != exitOK || out.Outcome != "done" {
				t.Fatalf("move: %d %+v", code, out)
			}
			current := filepath.Join(h.config, "looptender")
			named := filepath.Join(h.state, "looptender", "example-new", "state")
			cfg := filepath.Join(current, "loops", "example-new", "keepgoing.yaml")
			if where == "registered elsewhere" {
				cfg = filepath.Join(filepath.Dir(h.outsideState), "new", "keepgoing.yaml")
			}
			if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte("version: 1\nloop:\n  name: example-new\n  state_dir: "+named+"\n  prompt: Continue the fictional task.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if where == "registered elsewhere" {
				if _, err := registry.Add(filepath.Join(current, "registry"), "example-new", cfg, filepath.Join(filepath.Dir(cfg), "state")); err != nil {
					t.Fatal(err)
				}
			}
			before := h.tree(t)
			for _, args := range [][]string{{"--undo"}, {"--undo", "--apply"}} {
				code, out, text := migrateRun(t, args...)
				if code != exitFailed || out.Outcome != "refused" || len(out.Refusals) != 1 || out.Refusals[0].Code != "names_current_path" || out.Refusals[0].Path != cfg || !strings.Contains(out.Refusals[0].Message, named) {
					t.Fatalf("%v: %d %+v", args, code, out)
				}
				if !strings.Contains(text, "Nothing was changed") {
					t.Fatalf("%v: %s", args, text)
				}
			}
			if after := h.tree(t); !same(before, after) {
				t.Fatal("a refused move back changed something")
			}
			// A relative state_dir moves with its configuration: no refusal.
			if err := os.WriteFile(cfg, []byte("version: 1\nloop:\n  name: example-new\n  state_dir: state\n  prompt: Continue the fictional task.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if code, out, _ := migrateRun(t, "--undo"); code != exitOK || out.Outcome != "ready" {
				t.Fatalf("with a relative path: %d %+v", code, out)
			}
		})
	}
}

// On Unix the locks found free are held through the move: no check, daemon,
// panel or registry edit can start in the directories while they move, and
// they are free again afterwards.
func TestMigrateHoldsTheLocksThroughTheMove(t *testing.T) {
	if !migrateHoldLocks {
		t.Skip("locks are only looked at here: an open file keeps a directory from being renamed")
	}
	h := newMigrateHome(t)
	locks := []string{filepath.Join(h.inside.StateDir, lock.Tick+".lock"), filepath.Join(h.outsideState, lock.Daemon+".lock"), filepath.Join(h.state, "keepgoing", "sessions", "0123456789abcdef0123456789abcdef.lock")}
	for _, l := range locks {
		if err := os.WriteFile(l, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checked := false
	migrateBeforeMoves = func() {
		checked = true
		for _, l := range locks {
			if held, err := lock.HeldFile(l); err != nil || !held {
				t.Errorf("%s is not held through the move: %v %v", l, held, err)
			}
		}
	}
	t.Cleanup(func() { migrateBeforeMoves = nil })
	if code, out, _ := migrateRun(t, "--apply"); code != exitOK || out.Outcome != "done" {
		t.Fatalf("%d %+v", code, out)
	}
	if !checked {
		t.Fatal("the moment before the moves was not reached")
	}
	for _, l := range []string{filepath.Join(h.config, "looptender", "loops", "example-inside", "state", lock.Tick+".lock"), locks[1], filepath.Join(h.state, "looptender", "sessions", "0123456789abcdef0123456789abcdef.lock")} {
		if held, err := lock.HeldFile(l); err != nil || held {
			t.Errorf("%s is still held after the move: %v %v", l, held, err)
		}
	}
}

// The second place failing after the first was moved: the run says which
// place was moved and which was not and why, rewrites the moved place's
// references, and running again finishes.
func TestMigrateSecondPlaceFails(t *testing.T) {
	h := newMigrateHome(t)
	if h.config == h.state {
		t.Skip("one place here")
	}
	blocker := filepath.Join(h.state, "looptender")
	migrateBeforeMoves = func() {
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { migrateBeforeMoves = nil })
	var buf bytes.Buffer
	code := run(context.Background(), []string{"migrate", "--json", "--apply"}, &buf, &bytes.Buffer{})
	migrateBeforeMoves = nil
	var out migrateOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if code != exitFailed || out.Outcome != "failed" || !strings.HasPrefix(out.Message, "Moved "+h.config+". "+h.state+" was not moved") || out.References != 1 {
		t.Fatalf("%d %+v", code, out)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := migrateRun(t, "--apply"); code != exitOK || out.Outcome != "done" {
		t.Fatalf("run again: %d %+v", code, out)
	}
}

// The little things: a marker a stopped run left behind is cleared by a run
// that finds everything done; a held route list lock is named as such; and
// a preview says when no link can be made, before anything moves.
func TestMigrateSmallCases(t *testing.T) {
	t.Run("a leftover marker", func(t *testing.T) {
		h := newMigrateHome(t)
		if code, out, _ := migrateRun(t, "--apply"); code != exitOK || out.Outcome != "done" {
			t.Fatalf("%d %+v", code, out)
		}
		marker := filepath.Join(h.config, "keepgoing.moving")
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if code, out, _ := migrateRun(t, "--apply"); code != exitOK || out.Outcome != "nothing_to_do" {
			t.Fatalf("%d %+v", code, out)
		}
		if _, err := os.Lstat(marker); err == nil {
			t.Fatal("the marker was left")
		}
	})
	t.Run("the route list is being changed", func(t *testing.T) {
		h := newMigrateHome(t)
		l, err := lock.AcquireNamed(filepath.Join(h.config, "keepgoing"), "routes")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		code, out, _ := migrateRun(t)
		if code != exitFailed || len(out.Refusals) != 1 || out.Refusals[0].Message != "the standard route list is being changed" {
			t.Fatalf("%d %+v", code, out)
		}
	})
	t.Run("no link can be made", func(t *testing.T) {
		h := newMigrateHome(t)
		// Something where the probe link goes makes the probe fail everywhere.
		if err := os.MkdirAll(filepath.Join(h.config, "keepgoing.linktest", "kept"), 0o700); err != nil {
			t.Fatal(err)
		}
		before := h.tree(t)
		code, out, _ := migrateRun(t)
		if code != exitFailed || out.Outcome != "refused" || len(out.Refusals) != 1 || out.Refusals[0].Code != "no_link" {
			t.Fatalf("%d %+v", code, out)
		}
		if after := h.tree(t); !same(before, after) {
			t.Fatal("the preview changed something")
		}
	})
}
