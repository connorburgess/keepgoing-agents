package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// loopFixture is a setup directory as `init` leaves it: the marker, the
// configuration and the prompt, with the state directory inside. The agent
// is a BB thread whose CLI does not exist, so nothing can be contacted.
type loopFixture struct {
	t                         *testing.T
	root, dir, path, registry string
}

func newLoopFixture(t *testing.T) *loopFixture {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &loopFixture{t: t, root: root, dir: filepath.Join(root, "setups", "example-loop"), registry: filepath.Join(root, "registry")}
	f.path = filepath.Join(f.dir, "keepgoing.yaml")
	for _, d := range []string{f.dir, f.registry} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.write("keepgoing.yaml", "version: 1\nloop:\n  name: example-loop\n  state_dir: "+filepath.Join(f.dir, "state")+"\n  adapter: bb\n  bb: {thread: thr_example123, cli: absent-bb}\n  prompt_file: prompt.md\n")
	f.write("prompt.md", "Continue the fictional loop.\n")
	f.write(loopMarkerName, string(newLoopMarker("example-loop")))
	return f
}

func (f *loopFixture) write(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *loopFixture) run(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// remove runs `loop remove` against the fixture's own registry directory,
// never the user's.
func (f *loopFixture) remove(extra ...string) (int, loopRemovalReport, string) {
	f.t.Helper()
	code, out, errs := f.run(append([]string{"loop", "remove", "-c", f.path, "--registry", f.registry, "--json"}, extra...)...)
	var r loopRemovalReport
	if out != "" {
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			f.t.Fatalf("not JSON: %v\n%s\n%s", err, out, errs)
		}
	}
	return code, r, errs
}

func (f *loopFixture) must(args ...string) {
	f.t.Helper()
	if code, _, errs := f.run(args...); code != exitOK {
		f.t.Fatalf("%v: %d %s", args, code, errs)
	}
}

// snapshot is every path under root with its type and a digest of its
// content, so any change at all shows.
func (f *loopFixture) snapshot() string {
	f.t.Helper()
	var lines []string
	filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		line := p + " " + d.Type().String()
		if d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			line += fmt.Sprintf(" %x", sha256.Sum256(b))
		}
		lines = append(lines, line)
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func refusalCodes(r loopRemovalReport) string {
	var codes []string
	for _, x := range r.Refusals {
		codes = append(codes, x.Code)
	}
	sort.Strings(codes)
	return strings.Join(codes, ",")
}

// The default is a preview: it says exactly what would go and changes
// nothing at all, not even a lock file, for a loop that ran and for one that
// never did.
func TestLoopRemovePreviewChangesNothing(t *testing.T) {
	f := newLoopFixture(t)
	before := f.snapshot()
	code, r, errs := f.remove()
	if code != exitOK || r.Applied || len(r.Refusals) != 0 || len(r.Items) != 1 || r.Items[0].Kind != "configuration_directory" || r.Items[0].Path != "" ||
		!strings.Contains(r.Items[0].What, "keepgoing.yaml") || !strings.Contains(r.Message, "preview only, nothing was removed") {
		t.Fatalf("%d %+v %s", code, r, errs)
	}
	if after := f.snapshot(); after != before {
		t.Fatalf("a preview of a loop that never ran changed something:\n%s\n---\n%s", before, after)
	}

	f.must("enable", "-c", f.path)
	f.must("disable", "-c", f.path)
	f.must("registry", "add", "-c", f.path, "--directory", f.registry)
	before = f.snapshot()
	code, r, errs = f.remove()
	if code != exitOK || r.Applied || len(r.Refusals) != 0 || len(r.Items) != 3 || r.Items[0].Kind != "state_directory" || r.Items[1].Kind != "registry_entry" || r.Items[2].Kind != "configuration_directory" {
		t.Fatalf("%d %+v %s", code, r, errs)
	}
	if after := f.snapshot(); after != before {
		t.Fatalf("a preview changed something:\n%s\n---\n%s", before, after)
	}
	// Paths only on request; the text form says the same.
	if _, r, _ := f.remove("--private"); r.Items[0].Path != filepath.Join(f.dir, "state") || r.Items[2].Path != f.dir {
		t.Fatalf("%+v", r.Items)
	}
	_, text, _ := f.run("loop", "remove", "-c", f.path, "--registry", f.registry)
	if !strings.Contains(text, "example-loop: preview only, nothing was removed") || strings.Contains(text, f.dir) || !strings.Contains(text, "the registry entry loop_") {
		t.Fatalf("text preview: %s", text)
	}
	if after := f.snapshot(); after != before {
		t.Fatal("previews changed something")
	}
}

// --yes removes the state directory, the registry entry and the setup
// directory, records it, and touches nothing else.
func TestLoopRemoveApplies(t *testing.T) {
	f := newLoopFixture(t)
	f.must("enable", "-c", f.path)
	f.must("disable", "-c", f.path)
	f.must("registry", "add", "-c", f.path, "--directory", f.registry)
	// Neighbours that must survive: another setup, and a work repository.
	other := filepath.Join(f.root, "setups", "other-loop")
	work := filepath.Join(f.root, "work")
	for _, d := range []string{other, work} {
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, "keep.txt"), []byte("fictional"), 0o600)
	}
	t.Setenv("BB_THREAD_ID", "") // a terminal
	code, r, errs := f.remove("--yes")
	if code != exitOK || !r.Applied || !r.Complete || !r.Recorded || strings.Join(r.Removed, ",") != "state_directory,registry_entry,configuration_directory" || len(r.Failed) != 0 {
		t.Fatalf("%d %+v %s", code, r, errs)
	}
	if _, err := os.Lstat(f.dir); !os.IsNotExist(err) {
		t.Fatalf("the setup directory is still there: %v", err)
	}
	for _, d := range []string{other, work, filepath.Dir(f.dir)} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s was touched: %v", d, err)
		}
	}
	_, list, _ := f.run("registry", "list", "--directory", f.registry, "--json")
	if strings.Contains(list, "example-loop") {
		t.Fatalf("the registry entry is still there: %s", list)
	}
	log, _ := os.ReadFile(filepath.Join(f.registry, "removals.jsonl"))
	if !strings.Contains(string(log), `"loop":"example-loop"`) || !strings.Contains(string(log), `"by":"terminal"`) || !strings.Contains(string(log), `"complete":true`) || strings.Contains(string(log), f.dir) {
		t.Fatalf("removal log: %s", log)
	}

	// From a BB thread, with paths recorded on request.
	g := newLoopFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	if code, r, errs := g.remove("--yes", "--private"); code != exitOK || !r.Complete {
		t.Fatalf("%d %+v %s", code, r, errs)
	}
	log, _ = os.ReadFile(filepath.Join(g.registry, "removals.jsonl"))
	if !strings.Contains(string(log), `"by":"thread"`) || !strings.Contains(string(log), "thr_example_owner") || !strings.Contains(string(log), "keepgoing.yaml") {
		t.Fatalf("removal log: %s", log)
	}
	// Nothing left: the command can no longer find the loop.
	if code, _, errs := g.run("loop", "remove", "-c", g.path, "--registry", g.registry); code != exitUsage || !strings.Contains(errs, "could not be loaded") {
		t.Fatalf("%d %s", code, errs)
	}
}

// Each refusal names its reason and leaves everything as it was. --force
// lifts only "switched on" and "held for a person".
func TestLoopRemoveRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(t *testing.T, f *loopFixture)
		want  string
		force bool // --force lifts it
	}{
		"switched on": {func(t *testing.T, f *loopFixture) { f.must("enable", "-c", f.path) }, "enabled", true},
		"held for a person": {func(t *testing.T, f *loopFixture) {
			f.must("enable", "-c", f.path)
			f.must("await-user", "-c", f.path)
			f.must("disable", "-c", f.path)
		}, "awaiting_user", true},
		"a turn in progress": {func(t *testing.T, f *loopFixture) {
			f.must("enable", "-c", f.path)
			f.must("disable", "-c", f.path)
			target := filepath.Join(f.dir, "state", "targets", "example-fallback")
			os.MkdirAll(target, 0o700)
			os.WriteFile(filepath.Join(target, "run.json"), []byte(fmt.Sprintf(`{"pid":%d,"stopKey":"k","startedAt":"2026-01-01T00:00:00Z","argv0":"x"}`, os.Getpid())), 0o600)
		}, "turn_running", false},
		"a check running": {func(t *testing.T, f *loopFixture) {
			f.must("enable", "-c", f.path)
			f.must("disable", "-c", f.path)
			held, err := lock.Acquire(filepath.Join(f.dir, "state"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { held.Release() })
		}, "check_running", false},
		"a daemon holding its lock": {func(t *testing.T, f *loopFixture) {
			f.must("enable", "-c", f.path)
			f.must("disable", "-c", f.path)
			held, err := lock.AcquireNamed(filepath.Join(f.dir, "state"), lock.Daemon)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { held.Release() })
		}, "daemon_lock_held", false},
		"a managed service": {func(t *testing.T, f *loopFixture) {
			f.must("enable", "-c", f.path)
			f.must("disable", "-c", f.path)
			os.WriteFile(filepath.Join(f.dir, "state", "service-launchd.json"), []byte("{}"), 0o600)
		}, "service_installed", false},
		"not the owner": {func(t *testing.T, f *loopFixture) {
			t.Setenv("BB_THREAD_ID", "thr_example_owner")
			f.must("enable", "-c", f.path)
			f.must("disable", "-c", f.path)
			t.Setenv("BB_THREAD_ID", "thr_example_other")
		}, "not_owner", false},
		"no marker":                           {func(t *testing.T, f *loopFixture) { os.Remove(filepath.Join(f.dir, loopMarkerName)) }, "no_marker", false},
		"a marker for another loop":           {func(t *testing.T, f *loopFixture) { f.write(loopMarkerName, string(newLoopMarker("another-loop"))) }, "no_marker", false},
		"a marker keepgoing did not write":    {func(t *testing.T, f *loopFixture) { f.write(loopMarkerName, "remove me\n") }, "no_marker", false},
		"an entry keepgoing did not write":    {func(t *testing.T, f *loopFixture) { f.write("notes.txt", "fictional notes\n") }, "unexpected_entries", false},
		"a directory keepgoing did not write": {func(t *testing.T, f *loopFixture) { os.Mkdir(filepath.Join(f.dir, "work"), 0o700) }, "unexpected_entries", false},
		"inside a checkout":                   {func(t *testing.T, f *loopFixture) { os.Mkdir(filepath.Join(f.root, "setups", ".git"), 0o700) }, "inside_checkout", false},
		"a state directory elsewhere": {func(t *testing.T, f *loopFixture) {
			elsewhere := filepath.Join(f.root, "elsewhere")
			os.MkdirAll(elsewhere, 0o700)
			f.write("keepgoing.yaml", "version: 1\nloop:\n  name: example-loop\n  state_dir: "+elsewhere+"\n  adapter: bb\n  bb: {thread: thr_example123, cli: absent-bb}\n  prompt_file: prompt.md\n")
		}, "state_outside_setup", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newLoopFixture(t)
			tc.setup(t, f)
			before := f.snapshot()
			for _, args := range [][]string{nil, {"--yes"}} {
				code, r, errs := f.remove(args...)
				if code != exitFailed || r.Applied || !strings.Contains(refusalCodes(r), tc.want) || len(r.Removed) != 0 {
					t.Fatalf("%v: %d %+v %s", args, code, r, errs)
				}
				for _, x := range r.Refusals {
					if x.Code == tc.want && (x.Force != tc.force || x.Reason == "") {
						t.Fatalf("%+v", x)
					}
				}
			}
			if after := f.snapshot(); after != before {
				t.Fatalf("a refusal changed something:\n%s\n---\n%s", before, after)
			}
			code, r, _ := f.remove("--yes", "--force")
			if tc.force != (code == exitOK && r.Complete) {
				t.Fatalf("--force: %d %+v", code, r)
			}
			if !tc.force {
				if after := f.snapshot(); after != before {
					t.Fatal("--force changed something it must not")
				}
			}
		})
	}
	// The setup directory reached through a symlink is not removed.
	f := newLoopFixture(t)
	link := filepath.Join(f.root, "link")
	if err := os.Symlink(f.dir, link); err == nil {
		code, out, _ := f.run("loop", "remove", "-c", filepath.Join(link, "keepgoing.yaml"), "--registry", f.registry, "--yes", "--json")
		if code != exitFailed || !strings.Contains(out, "not_a_directory") {
			t.Fatalf("%d %s", code, out)
		}
		if _, err := os.Stat(f.path); err != nil {
			t.Fatal("a symlinked setup was removed")
		}
	}
	// Usage.
	if code, _, errs := f.run("loop"); code != exitUsage || !strings.Contains(errs, "usage: keepgoing loop remove") {
		t.Fatalf("%d %s", code, errs)
	}
	if code, _, errs := f.run("loop", "remove", "extra", "-c", f.path); code != exitUsage {
		t.Fatalf("%d %s", code, errs)
	}
	if code, _, errs := f.run("loop", "remove", "-c", filepath.Join(f.root, "missing.yaml"), "--registry", f.registry); code != exitUsage || !strings.Contains(errs, "what belongs to the loop is not known") {
		t.Fatalf("%d %s", code, errs)
	}
}

// One directory removed and the other not: the report says which, the
// command fails, and the same command finishes the job.
func TestLoopRemovePartialFailureIsReportedAndRetried(t *testing.T) {
	f := newLoopFixture(t)
	f.must("enable", "-c", f.path)
	f.must("disable", "-c", f.path)
	f.must("registry", "add", "-c", f.path, "--directory", f.registry)
	real := loopRemove
	loopRemove = func(path string) error {
		if filepath.Base(path) == "prompt.md" {
			return errors.New("fictional failure")
		}
		return real(path)
	}
	code, r, errs := f.remove("--yes")
	loopRemove = real
	if code != exitFailed || !r.Applied || r.Complete || strings.Join(r.Removed, ",") != "state_directory,registry_entry" ||
		len(r.Failed) != 1 || r.Failed[0].Kind != "configuration_directory" || strings.Contains(r.Failed[0].Reason, "fictional") || !strings.Contains(r.Message, "run the same command again") {
		t.Fatalf("%d %+v %s", code, r, errs)
	}
	// The state is gone, the configuration and its marker are still there.
	if _, err := os.Stat(filepath.Join(f.dir, "state")); !os.IsNotExist(err) {
		t.Fatal("the state directory was reported removed but exists")
	}
	for _, name := range []string{"keepgoing.yaml", loopMarkerName, "prompt.md"} {
		if _, err := os.Stat(filepath.Join(f.dir, name)); err != nil {
			t.Fatalf("%s is gone, so the retry could not find the loop: %v", name, err)
		}
	}
	log, _ := os.ReadFile(filepath.Join(f.registry, "removals.jsonl"))
	if !strings.Contains(string(log), `"complete":false`) {
		t.Fatalf("removal log: %s", log)
	}
	// A second failure, this time at the configuration file itself, after
	// the other files have gone: still retryable.
	loopRemove = func(path string) error {
		if filepath.Base(path) == "keepgoing.yaml" {
			return errors.New("fictional failure")
		}
		return real(path)
	}
	code, r, errs = f.remove("--yes")
	loopRemove = real
	if code != exitFailed || r.Complete || len(r.Failed) != 1 || r.Failed[0].Kind != "configuration_directory" {
		t.Fatalf("second failure: %d %+v %s", code, r, errs)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "prompt.md")); !os.IsNotExist(err) {
		t.Fatal("prompt.md should have been removed before the configuration")
	}
	// The same command again.
	code, r, errs = f.remove("--yes")
	if code != exitOK || !r.Complete || strings.Join(r.Removed, ",") != "configuration_directory" {
		t.Fatalf("retry: %d %+v %s", code, r, errs)
	}
	if _, err := os.Lstat(f.dir); !os.IsNotExist(err) {
		t.Fatalf("the setup directory is still there: %v", err)
	}

	// The state directory fails first: nothing after it is attempted.
	g := newLoopFixture(t)
	g.must("enable", "-c", g.path)
	g.must("disable", "-c", g.path)
	realAll := loopRemoveAll
	loopRemoveAll = func(string) error { return errors.New("fictional failure") }
	code, r, _ = g.remove("--yes", "--private")
	loopRemoveAll = realAll
	if code != exitFailed || len(r.Removed) != 0 || len(r.Failed) != 1 || r.Failed[0].Kind != "state_directory" || !strings.Contains(r.Failed[0].Reason, "fictional") {
		t.Fatalf("%d %+v", code, r)
	}
	if _, err := os.Stat(g.path); err != nil {
		t.Fatal("the configuration was removed after the state directory failed")
	}
}

// init marks the directory it creates, so a setup made from now on can be
// removed.
func TestInitWritesTheLoopMarker(t *testing.T) {
	if err := readLoopMarker(t.TempDir(), "example-loop"); err == nil || !strings.Contains(err.Error(), "carries no keepgoing marker") {
		t.Fatalf("an unmarked directory: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, loopMarkerName), newLoopMarker("example-loop"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readLoopMarker(dir, "example-loop"); err != nil {
		t.Fatal(err)
	}
	if err := readLoopMarker(dir, "other-loop"); err == nil {
		t.Fatal("a marker for another loop was accepted")
	}
}
