//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// buildWorld is a fictional loop whose checks run a stand-in file, and a
// panel whose own file is another stand-in. Neither is ever executed.
type buildWorld struct {
	f              *fixture
	entry          registry.Entry
	target, source string
	t0             time.Time
	builds         *buildSession
}

func newBuildWorld(t *testing.T, dirName string) *buildWorld {
	t.Helper()
	f := newFixture(t)
	f.run("enable")
	w := &buildWorld{f: f, t0: time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)}
	stateDir := filepath.Join(f.dir, "state")
	w.entry = registry.Entry{ID: "loop_0123456789abcdef0123456789abcdef", Loop: "example-docs", ConfigPath: f.cfg, StateDir: stateDir}
	bin := filepath.Join(f.dir, dirName)
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	w.target, w.source = filepath.Join(bin, "keepgoing"), filepath.Join(f.dir, "panel-keepgoing")
	if err := os.WriteFile(w.target, []byte("#!/bin/sh\n# fictional build v0.2.1\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.source, []byte("#!/bin/sh\n# fictional build v0.2.2, a little longer\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveCheckExecutable(stateDir, w.target, "v0.2.1", w.t0); err != nil {
		t.Fatal(err)
	}
	prevVersion, prevSource, prevOS, prevOwned, prevWait, prevLimit := version, buildSource, buildOS, buildOwnedByUser, buildLockWait, buildBackupLimit
	version = "v0.2.2"
	buildSource = func() (string, error) { return w.source, nil }
	buildOS = "darwin"
	buildLockWait = 300 * time.Millisecond
	t.Cleanup(func() {
		version, buildSource, buildOS, buildOwnedByUser, buildLockWait, buildBackupLimit = prevVersion, prevSource, prevOS, prevOwned, prevWait, prevLimit
	})
	w.builds = newBuildSession(filepath.Join(f.dir, "private-panel"), filepath.Join(f.dir, "empty-registry"))
	t.Cleanup(w.builds.close)
	return w
}

var noBuildDaemon = daemonStatus{State: "not_recorded"}

func (w *buildWorld) review(t *testing.T, action string) buildReviewed {
	t.Helper()
	ctl, why := panelReadControl(w.entry.StateDir)
	if why != "" {
		t.Fatal(why)
	}
	r, refused := w.builds.review(context.Background(), w.entry, panelLoop{dockLoop{Name: w.entry.Loop}}, ctl, action, noBuildDaemon, w.t0)
	if refused != nil {
		t.Fatalf("refused: %+v", refused)
	}
	return r
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// When it is offered, and in which words, from the two labels and the file.
func TestBuildReplacementIsOfferedOnlyWhereSafe(t *testing.T) {
	for _, c := range []struct{ panel, file, kind string }{
		{"v0.2.2", "v0.2.1", "newer"},
		{"v0.2.2", "v0.2.1-dev.abc1234", "unordered"},
		{"v0.2.2-dev.abc1234", "v0.2.1-dev.def5678", "unordered"},
		{"v0.2.1", "v0.2.2", ""},       // never an older build in place of a newer one
		{"v0.2.2", "v0.2.2", ""},       // nothing to do
		{"v0.2.2-dirty", "v0.2.1", ""}, // never from a dirty build
		{"v0.2.2", "abc-dirty", ""},
		{"dev", "v0.2.1", ""},
		{"v0.2.2", "dev", ""},
		{"v0.2.2", "", ""},
	} {
		if kind, _ := buildOrder(c.panel, c.file); kind != c.kind {
			t.Errorf("%s over %s: %q", c.panel, c.file, kind)
		}
	}
	w := newBuildWorld(t, "bin")
	offer := func() dockBuildChange { c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0); return c }
	if c := offer(); c.Action != buildReplaceAction || c.Kind != "newer" || strings.Contains(c.Text, w.target) {
		t.Fatalf("%+v", c)
	}
	// A daemon of the loop is running: a restart would be needed.
	if c, _ := w.builds.assess(w.entry, daemonStatus{State: "responsive"}, w.t0); c.Action != "" || !strings.Contains(c.Text, "needs a restart") {
		t.Errorf("%+v", c)
	}
	// Windows, a file that is not the user's, a symlink, this panel's own file, a missing file.
	buildOS = "windows"
	if c := offer(); c.Action != "" || !strings.Contains(c.Text, "On Windows") {
		t.Errorf("%+v", c)
	}
	buildOS = "darwin"
	buildOwnedByUser = func(os.FileInfo) bool { return false }
	if c := offer(); c.Action != "" || !strings.Contains(c.Text, "not yours") {
		t.Errorf("%+v", c)
	}
	buildOwnedByUser = fileOwnedByUser
	link := filepath.Join(w.f.dir, "link-keepgoing")
	if err := os.Symlink(w.target, link); err != nil {
		t.Fatal(err)
	}
	_ = state.SaveCheckExecutable(w.entry.StateDir, link, "v0.2.1", w.t0)
	if c := offer(); c.Action != "" || !strings.Contains(c.Text, "symbolic link") {
		t.Errorf("%+v", c)
	}
	_ = state.SaveCheckExecutable(w.entry.StateDir, w.source, "v0.2.1", w.t0)
	if c := offer(); c.Action != "" || !strings.Contains(c.Text, "this panel's own file") {
		t.Errorf("%+v", c)
	}
	_ = state.SaveCheckExecutable(w.entry.StateDir, w.target, "v0.2.1", w.t0)
	_ = os.Remove(w.target)
	if c := offer(); c.Action != "" || !strings.Contains(c.Text, "no longer there") {
		t.Errorf("%+v", c)
	}
	// Package managers' trees.
	for path, want := range map[string]string{
		"/opt/homebrew/bin/keepgoing":                          "Homebrew",
		"/usr/local/Cellar/keepgoing/0.2.1/bin/keepgoing":      "Homebrew",
		"/home/linuxbrew/.linuxbrew/bin/keepgoing":             "Homebrew",
		"/nix/store/abc-keepgoing/bin/keepgoing":               "Nix",
		"/usr/bin/keepgoing":                                   "the system's package manager",
		"/opt/keepgoing/keepgoing":                             "the system's package manager",
		"/snap/bin/keepgoing":                                  "snap",
		`C:/Users/example/scoop/apps/keepgoing/keepgoing.exe`:  "Scoop",
		"/Users/example/bin/keepgoing":                         "",
		"/Users/example/Bïn 二进制 files/keepgoing":               "",
		"/Users/example/projects/keepgoing-loop/bin/keepgoing": "",
	} {
		if got := buildPackageTree(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}

// The whole cycle through the panel's own functions, in a folder whose name
// has accented Latin, CJK and spaces: review, replace, the private record,
// then Undo after a check ran on the new build.
func TestBuildReplaceAndUndo(t *testing.T) {
	w := newBuildWorld(t, "Bïn 二进制 files")
	old, newer := read(t, w.target), read(t, w.source)
	r := w.review(t, buildReplaceAction)
	lines := strings.Join(func() []string {
		var s []string
		for _, l := range r.preview.Lines {
			s = append(s, l.render(w.t0))
		}
		return s
	}(), "\n")
	for _, want := range []string{w.target, w.source, "v0.2.1", "v0.2.2", "backed up", "Nothing is restarted"} {
		if !strings.Contains(lines, want) {
			t.Errorf("the review does not say %q:\n%s", want, lines)
		}
	}
	res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
	if res.Outcome != "done" {
		t.Fatalf("%+v", res)
	}
	if read(t, w.target) != newer {
		t.Fatal("the file was not replaced")
	}
	if info, _ := os.Stat(w.target); info.Mode().Perm() != 0o750 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	backups, _ := os.ReadDir(filepath.Join(w.builds.recordDir(w.entry), buildBackupsDir))
	if len(backups) != 1 {
		t.Fatalf("%d backups", len(backups))
	}
	bdir := filepath.Join(w.builds.recordDir(w.entry), buildBackupsDir, backups[0].Name())
	if read(t, filepath.Join(bdir, buildBackupFile)) != old || read(t, filepath.Join(bdir, "config.yaml")) != read(t, w.f.cfg) {
		t.Error("the backup does not hold the old file and the configuration")
	}
	if _, err := os.Stat(filepath.Join(bdir, buildBackupState, state.CheckExecutableName)); err != nil {
		t.Error("the backup does not hold the state")
	}
	// Waiting for the next check, with Undo offered.
	if c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0); c.Action != buildUndoAction || !c.Pending {
		t.Fatalf("%+v", c)
	}
	// The history says what was done, with no path.
	j, _ := incident.Load(w.entry.StateDir)
	b, _ := json.Marshal(j)
	if !bytes.Contains(b, []byte("panel_replace_build")) || bytes.Contains(b, []byte(w.target)) {
		t.Errorf("history: %s", b)
	}
	// A check runs the new file and writes a record of its own.
	_ = state.SaveCheckExecutable(w.entry.StateDir, w.target, "v0.2.2", w.t0.Add(time.Hour))
	if err := os.WriteFile(filepath.Join(w.entry.StateDir, "written-by-the-new-build.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0); c.Pending || !strings.Contains(c.Text, "now run") {
		t.Errorf("%+v", c)
	}
	u := w.review(t, buildUndoAction)
	if u.preview.Confirm != "Restore only the file" {
		t.Errorf("%+v", u.preview)
	}
	res = w.builds.apply(context.Background(), w.entry, buildUndoAction, u.digest, noBuildDaemon, func() time.Time { return w.t0.Add(2 * time.Hour) })
	if res.Outcome != "done" {
		t.Fatalf("%+v", res)
	}
	if read(t, w.target) != old {
		t.Fatal("the file was not restored")
	}
	if info, _ := os.Stat(w.target); info.Mode().Perm() != 0o750 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(w.entry.StateDir, "written-by-the-new-build.json")); err != nil {
		t.Error("Undo removed a record")
	}
	if rec, _ := state.LoadCheckExecutable(w.entry.StateDir); rec == nil || rec.Label != "v0.2.2" {
		t.Errorf("Undo changed the executable record: %+v", rec)
	}
	if u, _ := loadBuildUpdate(w.builds.recordDir(w.entry)); u != nil {
		t.Error("the update record was left")
	}
	j, _ = incident.Load(w.entry.StateDir)
	b, _ = json.Marshal(j)
	if bytes.Contains(b, []byte("panel_undo_replace_build")) {
		t.Errorf("history: %s", b)
	}
}

// Each refusal changes nothing.
func TestBuildReplaceRefusals(t *testing.T) {
	t.Run("the file changed since the review", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		if err := os.WriteFile(w.target, []byte("someone else's build"), 0o750); err != nil {
			t.Fatal(err)
		}
		if res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, time.Now); res.Outcome != "review_again" || read(t, w.target) != "someone else's build" {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a check is running", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		lk, err := lock.Acquire(w.entry.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		defer lk.Release()
		old := read(t, w.target)
		if res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, time.Now); res.Outcome != "refused" || !strings.Contains(res.Text, "A check is running") || read(t, w.target) != old {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a check runs while the change waits for it", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		lk, err := lock.Acquire(w.entry.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		// The check finishes inside the wait, having recorded itself.
		go func() {
			time.Sleep(100 * time.Millisecond)
			_ = state.SaveCheckExecutable(w.entry.StateDir, w.target, "v0.2.1-dev.fixture", w.t0.Add(time.Minute))
			lk.Release()
		}()
		old := read(t, w.target)
		if res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, time.Now); res.Outcome != "review_again" || read(t, w.target) != old {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("the configuration does not load", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		if err := os.WriteFile(w.f.cfg, []byte("not: [valid"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := read(t, w.target)
		res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, time.Now)
		if res.Outcome == "done" || read(t, w.target) != old {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("the state is over the cap", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		buildBackupLimit = 16
		old := read(t, w.target)
		res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, time.Now)
		if res.Outcome != "refused" || !strings.Contains(res.Text, "Replace it by hand") || read(t, w.target) != old {
			t.Fatalf("%+v", res)
		}
		if entries, _ := os.ReadDir(filepath.Join(w.builds.recordDir(w.entry), buildBackupsDir)); len(entries) != 0 {
			t.Errorf("a partial backup was left: %d", len(entries))
		}
	})
	t.Run("Undo after someone else changed the file", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		if res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, time.Now); res.Outcome != "done" {
			t.Fatalf("%+v", res)
		}
		if err := os.WriteFile(w.target, []byte("a later build"), 0o750); err != nil {
			t.Fatal(err)
		}
		ctl, _ := panelReadControl(w.entry.StateDir)
		if _, refused := w.builds.review(context.Background(), w.entry, panelLoop{}, ctl, buildUndoAction, noBuildDaemon, time.Now()); refused == nil || !strings.Contains(refused.Text, "someone else's change") {
			t.Fatalf("%+v", refused)
		}
	})
}

// The newest three backups are kept.
func TestBuildBackupsKeepTheNewestThree(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"20300101T120000.000000000Z", "20300102T120000.000000000Z", "20300103T120000.000000000Z", "20300104T120000.000000000Z"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	buildPrune(root)
	entries, _ := os.ReadDir(root)
	if len(entries) != 3 || entries[0].Name() != "20300102T120000.000000000Z" {
		t.Errorf("%v", entries)
	}
}

// The path a check records stays private: not in status, the timeline or the
// panel's default report.
func TestCheckExecutableIsPrivate(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	stateDir := filepath.Join(f.dir, "state")
	rec, err := state.LoadCheckExecutable(stateDir)
	if err != nil || rec == nil || !filepath.IsAbs(rec.Path) {
		t.Fatalf("%+v %v", rec, err)
	}
	info, _ := os.Stat(filepath.Join(stateDir, state.CheckExecutableName))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("%v", info.Mode())
	}
	for _, args := range [][]string{{"status"}, {"status", "--json"}, {"timeline"}, {"timeline", "--json"}} {
		if _, out, errs := f.run(args...); strings.Contains(out+errs, rec.Path) {
			t.Errorf("%v shows the executable's path", args)
		}
	}
	// A dry run records nothing new.
	before := rec.At
	f.run("tick", "--dry-run")
	if again, _ := state.LoadCheckExecutable(stateDir); !again.At.Equal(before) {
		t.Error("a dry run recorded the executable")
	}
}

// A build change is the panel's own: client review and apply refuse it, and
// so does the change to every loop.
func TestBuildChangeIsPanelOnly(t *testing.T) {
	for _, args := range [][]string{
		{"client", "review", buildReplaceAction, "--loop", "l0123456789ab"},
		{"client", "apply", buildUndoAction, "--loop", "l0123456789ab", "--expect", "r1.1.x.y"},
	} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), args, &out, &errs); code != exitUsage {
			t.Errorf("%v: %d", args, code)
		}
	}
	if req, ok := panelReadAction(strings.NewReader(`{"action":"replace_build"}`)); !ok || req.Action != buildReplaceAction {
		t.Error("the panel does not take the action")
	}
}

// After a replacement, a failed check raises a need with Undo on the page.
func TestAFailedCheckAfterAReplacementIsANeed(t *testing.T) {
	in := dockEvidence{controls: &clientControls{Enabled: true}, status: func(s *statusOut) {
		s.State.LastCheck.At, s.State.LastCheck.Outcome = dockNow.Add(-time.Minute), "failed"
	}}.input()
	in.Entry.ID, in.Entry.Loop = "loop_00000000000000000000000000000001", "example-docs"
	in.BuildChange = &dockBuildChange{Action: buildUndoAction, Label: "Undo the replacement", updatedAt: dockNow.Add(-time.Hour)}
	r := buildDockReport([]dockInput{in}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true)
	if len(r.NeedsYou) != 1 || r.NeedsYou[0].Kind != "build_replaced" {
		t.Fatalf("%+v", r.NeedsYou)
	}
	// Before the replacement, the same failure is not this need.
	in.BuildChange.updatedAt = dockNow
	if r := buildDockReport([]dockInput{in}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true); len(r.NeedsYou) == 1 && r.NeedsYou[0].Kind == "build_replaced" {
		t.Errorf("%+v", r.NeedsYou)
	}
}

// Through the page's own endpoint: the read offers it without a path, the
// review names both files, the confirmation replaces the file and the history
// says so; a change to every loop is refused.
func TestBuildReplaceThroughThePage(t *testing.T) {

	source := filepath.Join(t.TempDir(), "panel-keepgoing")
	if err := os.WriteFile(source, []byte("#!/bin/sh\n# fictional v0.2.2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	prevVersion, prevSource := version, buildSource
	version, buildSource = "v0.2.2", func() (string, error) { return source, nil }
	t.Cleanup(func() { version, buildSource = prevVersion, prevSource })
	w := newPanelChangeWorld(t, true)
	e := w.entry(t, "docs-site-refresh")
	key := dockKey(e.ID)
	target := filepath.Join(filepath.Dir(e.ConfigPath), "Bïn 二进制 files", "keepgoing")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\n# fictional v0.2.1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveCheckExecutable(e.StateDir, target, "v0.2.1", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	_, body := dockRequest(t, http.MethodGet, w.page+dockAPIPath, withCookie(w.cookie))
	var r dockReport
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if bc := dockLoopNamed(t, r, "docs-site-refresh").BuildChange; bc == nil || bc.Action != buildReplaceAction {
		t.Fatalf("not offered: %+v", bc)
	}
	if strings.Contains(body, "二进制") {
		t.Error("the read carries the file's path")
	}
	if resp, _ := w.post(t, panelGroupKey, "prepare", w.form, `{"action":"replace_build"}`, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a change to every loop: %d", resp.StatusCode)
	}
	_, p := w.post(t, key, "prepare", w.form, `{"action":"replace_build"}`, nil)
	if p.Preview == nil || p.Preview.Confirm != "Replace the file" || !strings.Contains(p.Preview.Lines[0].render(time.Now()), "二进制") {
		t.Fatalf("%+v", p)
	}
	_, done := w.post(t, key, "confirm", p.Preview.Form, `{"action":"replace_build"}`, nil)
	if done.Result == nil || done.Result.Outcome != "done" {
		t.Fatalf("%+v", done)
	}
	if read(t, target) != read(t, source) || lastOperation(t, e) != "panel_replace_build" {
		t.Fatalf("not replaced: %q", lastOperation(t, e))
	}
	// The same form again is spent.
	if _, again := w.post(t, key, "confirm", p.Preview.Form, `{"action":"replace_build"}`, nil); again.Result == nil || again.Result.Outcome != "review_again" {
		t.Errorf("%+v", again)
	}
}
