//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestBuildSecuritySourceHandleAndIdentity(t *testing.T) {
	t.Run("renamed source stays pinned", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		want := read(t, w.source)
		next := w.source + "-dirty"
		os.WriteFile(next, []byte("dirty replacement"), 0700)
		if e := os.Rename(next, w.source); e != nil {
			t.Fatal(e)
		}
		r := w.review(t, buildReplaceAction)
		res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
		if res.Outcome != "done" || read(t, w.target) != want {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("mutated retained source refuses", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		old := read(t, w.target)
		os.WriteFile(w.source, []byte("dirty in place"), 0700)
		res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
		if res.Outcome == "done" || read(t, w.target) != old {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("manual upgrade before review refuses", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		os.WriteFile(w.target, []byte("manually upgraded v9.5.0"), 0700)
		c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0)
		if c.Action != "" || !strings.Contains(c.Text, "changed since its last check") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("hardlinks refuse", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		if e := os.Link(w.target, w.target+"-link"); e != nil {
			t.Fatal(e)
		}
		c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0)
		if c.Action != "" || !strings.Contains(c.Text, "hard links") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("parent substitution cannot redirect swap", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		rec, _ := state.LoadCheckExecutable(w.entry.StateDir)
		target, e := openBuildTarget(w.target, rec.Identity)
		if e != nil {
			t.Fatal(e)
		}
		defer target.close()
		parent := filepath.Dir(w.target)
		moved := parent + "-moved"
		if e = os.Rename(parent, moved); e != nil {
			t.Fatal(e)
		}
		decoy := t.TempDir()
		victim := filepath.Join(decoy, filepath.Base(w.target))
		os.WriteFile(victim, []byte("untouched"), 0700)
		if e = os.Symlink(decoy, parent); e != nil {
			t.Fatal(e)
		}
		if e = target.swap(context.Background(), []byte("new")); e == nil {
			t.Fatal("accepted substituted parent")
		}
		if read(t, victim) != "untouched" {
			t.Fatal("redirected swap")
		}
	})
}

func TestBuildSecurityScopeDaemonAndAgent(t *testing.T) {
	t.Run("outside config directory", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		other := filepath.Join(t.TempDir(), "sharedbin")
		os.WriteFile(other, []byte("shared"), 0700)
		state.SaveCheckExecutable(w.entry.StateDir, other, "v0.2.1", w.t0)
		c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0)
		if c.Action != "" || !strings.Contains(c.Text, "Shared locations") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("shared registered file names other loop", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		dir := t.TempDir()
		os.Chmod(dir, 0700)
		w.builds.registry = dir
		other := w.entry
		other.ID = "loop_1123456789abcdef0123456789abcdef"
		other.Loop = "second-example"
		other.ConfigPath = filepath.Join(w.f.dir, "other.yaml")
		other.StateDir = filepath.Join(w.f.dir, "other-state")
		os.MkdirAll(other.StateDir, 0700)
		state.SaveCheckExecutable(other.StateDir, w.target, "v0.2.1", w.t0)
		// The registry writer validates registrations; use its persisted schema.
		data, _ := json.Marshal(registry.File{SchemaVersion: 1, Entries: []registry.Entry{w.entry, other}})
		os.WriteFile(filepath.Join(dir, "registry.json"), data, 0600)
		t.Setenv("LOOPTENDER_REGISTRY_DIR", dir)
		t.Setenv("KEEPGOING_REGISTRY_DIR", dir)
		w.builds.registry = "" // exercise the default registry too
		c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0)
		if c.Action != "" || !strings.Contains(c.Text, other.Loop) {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("daemon lock refuses even without endpoint", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		lk, e := lock.AcquireNamed(w.entry.StateDir, lock.Daemon)
		if e != nil {
			t.Fatal(e)
		}
		defer lk.Release()
		old := read(t, w.target)
		res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
		if res.Outcome == "done" || read(t, w.target) != old {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("agent neither offered nor accepted", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		w.builds.agent = true
		c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0)
		if c.Action != "" {
			t.Fatalf("%+v", c)
		}
		res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
		if res.Outcome == "done" {
			t.Fatalf("%+v", res)
		}
		t.Setenv("BB_THREAD_ID", "thr_example_agent")
		agentSession := newBuildSession(t.TempDir(), "")
		defer agentSession.close()
		if !agentSession.agent {
			t.Fatal("agent not captured")
		}
		found := false
		for _, e := range panelServerEnvironment() {
			found = found || e == "BB_THREAD_ID=thr_example_agent"
		}
		if !found {
			t.Fatal("identity stripped from panel environment")
		}
	})
}

func TestBuildSecurityUndoAndReceipt(t *testing.T) {
	t.Run("Undo preserves every loop file", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		r := w.review(t, buildReplaceAction)
		res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
		if res.Outcome != "done" {
			t.Fatalf("%+v", res)
		}
		w.f.run("pause")
		state.SaveCheckExecutable(w.entry.StateDir, w.target, "v0.2.2", w.t0.Add(time.Minute))
		os.WriteFile(filepath.Join(w.entry.StateDir, "daemon-endpoint.json"), []byte(`{"fixture":true}`), 0600)
		before := treeBytes(t, w.entry.StateDir)
		r = w.review(t, buildUndoAction)
		res = w.builds.apply(context.Background(), w.entry, buildUndoAction, r.digest, noBuildDaemon, func() time.Time { return w.t0.Add(time.Hour) })
		if res.Outcome != "done" {
			t.Fatalf("%+v", res)
		}
		after := treeBytes(t, w.entry.StateDir)
		if !bytes.Equal(before, after) {
			t.Fatal("Undo changed loop records")
		}
	})
	t.Run("schema change refuses", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		os.WriteFile(filepath.Join(w.entry.StateDir, "fixture.json"), []byte(`{"schemaVersion":1}`), 0600)
		r := w.review(t, buildReplaceAction)
		res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
		if res.Outcome != "done" {
			t.Fatalf("%+v", res)
		}
		os.WriteFile(filepath.Join(w.entry.StateDir, "fixture.json"), []byte(`{"schemaVersion":2}`), 0600)
		c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0.Add(time.Hour))
		if c.Action != "" || !strings.Contains(c.Text, "record format") {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("forged loop receipt ignored and private Undo expires", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		os.WriteFile(filepath.Join(w.entry.StateDir, buildUpdateName), []byte(`{"version":1,"target":"/fictional/other"}`), 0600)
		c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0)
		if c.Action != buildReplaceAction {
			t.Fatalf("%+v", c)
		}
		r := w.review(t, buildReplaceAction)
		res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
		if res.Outcome != "done" {
			t.Fatalf("%+v", res)
		}
		u, _ := loadBuildUpdate(w.builds.recordDir(w.entry))
		if u == nil {
			t.Fatal("missing private receipt")
		}
		root := filepath.Join(w.builds.recordDir(w.entry), buildBackupsDir)
		for _, name := range []string{"20310101T120000.000000000Z", "20320101T120000.000000000Z", "20330101T120000.000000000Z"} {
			os.Mkdir(filepath.Join(root, name), 0700)
		}
		buildPrune(root, u.Backup)
		if _, e := os.Stat(filepath.Join(root, u.Backup, buildBackupFile)); e != nil {
			t.Fatal("pruned live backup")
		}
		c, _ = w.builds.assess(w.entry, noBuildDaemon, w.t0.Add(7*24*time.Hour))
		if c.Action == buildUndoAction || !c.updatedAt.IsZero() {
			t.Fatalf("expired receipt offered: %+v", c)
		}
	})
	t.Run("read only carries no build action", func(t *testing.T) {
		w := newBuildWorld(t, "bin")
		var b *buildSession
		c, _ := b.assess(w.entry, noBuildDaemon, w.t0)
		if c.Action != "" || strings.Contains(c.Text, "this panel can") {
			t.Fatalf("%+v", c)
		}
	})
}
func treeBytes(t *testing.T, dir string) []byte {
	t.Helper()
	files := map[string]string{}
	filepath.WalkDir(dir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			t.Fatal(e)
		}
		if d.Type().IsRegular() && !strings.HasSuffix(path, ".lock") {
			rel, _ := filepath.Rel(dir, path)
			files[rel] = read(t, path)
		}
		return nil
	})
	b, _ := json.Marshal(files)
	return b
}

func TestBuildSecuritySwapsAndRunsRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and executes two real keepgoing binaries")
	}
	w := newBuildWorld(t, "Bïn 二进制 files")
	build := func(path, label string) {
		t.Helper()
		_ = os.Remove(path)
		cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+label, "-o", path, ".")
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("build: %v %s", e, out)
		}
	}
	build(w.target, "v0.2.1")
	build(w.source, "v0.2.2")
	w.builds.close()
	w.builds = newBuildSession(w.builds.dir, w.builds.registry)
	defer w.builds.close()
	if e := state.SaveCheckExecutable(w.entry.StateDir, w.target, "v0.2.1", w.t0); e != nil {
		t.Fatal(e)
	}
	r := w.review(t, buildReplaceAction)
	res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
	if res.Outcome != "done" {
		t.Fatalf("%+v", res)
	}
	out, e := exec.Command(w.target, "version").CombinedOutput()
	if e != nil || !strings.Contains(string(out), "v0.2.2") {
		t.Fatalf("swapped binary: %v %s", e, out)
	}
	if runtime.GOOS == "darwin" {
		if out, e = exec.Command("codesign", "--verify", w.target).CombinedOutput(); e != nil {
			t.Fatalf("signature: %v %s", e, out)
		}
	}
	r = w.review(t, buildUndoAction)
	res = w.builds.apply(context.Background(), w.entry, buildUndoAction, r.digest, noBuildDaemon, func() time.Time { return w.t0.Add(time.Hour) })
	if res.Outcome != "done" {
		t.Fatalf("%+v", res)
	}
	out, e = exec.Command(w.target, "version").CombinedOutput()
	if e != nil || !strings.Contains(string(out), "v0.2.1") {
		t.Fatalf("restored binary: %v %s", e, out)
	}
}

func TestBuildSecurityAgentHTTPRefusal(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_agent")
	w := newPanelChangeWorld(t, true)
	e := w.entry(t, "docs-site-refresh")
	_, p := w.post(t, dockKey(e.ID), "prepare", w.form, `{"action":"replace_build"}`, nil)
	if p.Preview != nil || p.Result == nil || p.Result.Outcome != "refused" {
		t.Fatalf("%+v", p)
	}
	_, body := dockRequest(t, http.MethodGet, w.page+dockAPIPath, withCookie(w.cookie))
	if strings.Contains(body, `"action":"replace_build"`) {
		t.Fatal("agent page offers build action")
	}
}

func TestBuildSecuritySymlinkSourcePinned(t *testing.T) {
	w := newBuildWorld(t, "bin")
	w.builds.close()
	link := w.source + "-link"
	if e := os.Symlink(w.source, link); e != nil {
		t.Fatal(e)
	}
	buildSource = func() (string, error) { return link, nil }
	w.builds = newBuildSession(w.builds.dir, w.builds.registry)
	defer w.builds.close()
	dirty := w.source + "-dirty"
	os.WriteFile(dirty, []byte("dirty"), 0700)
	os.Remove(link)
	os.Symlink(dirty, link)
	r := w.review(t, buildReplaceAction)
	res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
	if res.Outcome != "done" || read(t, w.target) != read(t, w.source) {
		t.Fatalf("%+v", res)
	}
}

func TestBuildSecurityFailedSwapCleansBackup(t *testing.T) {
	w := newBuildWorld(t, "bin")
	old := read(t, w.target)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := w.builds.replace(ctx, w.entry, w.t0)
	if res.Outcome != "refused" || read(t, w.target) != old {
		t.Fatalf("%+v", res)
	}
	entries, _ := os.ReadDir(filepath.Join(w.builds.recordDir(w.entry), buildBackupsDir))
	if len(entries) != 0 {
		t.Fatal("failed swap left backup")
	}
}

func TestBuildSecurityPrivateReceiptStrict(t *testing.T) {
	w := newBuildWorld(t, "bin")
	r := w.review(t, buildReplaceAction)
	res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
	if res.Outcome != "done" {
		t.Fatalf("%+v", res)
	}
	dir := w.builds.recordDir(w.entry)
	path := filepath.Join(dir, buildUpdateName)
	original := []byte(read(t, path))
	for _, bad := range [][]byte{
		bytes.Replace(original, []byte(`"target":`), []byte(`"Target":`), 1),
		bytes.Replace(original, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		bytes.Replace(original, []byte(`"dev":`), []byte(`"Dev":`), 1),
		bytes.Replace(original, []byte(`"backup":`), []byte(`"backup":"../chosen","Backup":`), 1),
		append(append([]byte{}, original...), []byte(` {}`)...),
	} {
		if e := os.WriteFile(path, bad, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := loadBuildUpdate(dir); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestBuildSecurityDaemonStartsWhileWaiting(t *testing.T) {
	w := newBuildWorld(t, "bin")
	r := w.review(t, buildReplaceAction)
	tick, e := lock.Acquire(w.entry.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	defer tick.Release()
	done := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		time.Sleep(75 * time.Millisecond)
		daemon, e := lock.AcquireNamed(w.entry.StateDir, lock.Daemon)
		_ = tick.Release()
		errc <- e
		if e == nil {
			<-done
			_ = daemon.Release()
		}
	}()
	old := read(t, w.target)
	res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
	close(done)
	if e = <-errc; e != nil {
		t.Fatal(e)
	}
	if res.Outcome == "done" || read(t, w.target) != old {
		t.Fatalf("daemon starting during wait accepted: %+v", res)
	}
}

func TestBuildSecurityStartupFailureAndHomeAlias(t *testing.T) {
	w := newBuildWorld(t, "bin")
	buildSource = func() (string, error) { return filepath.Join(w.f.dir, "missing-source"), nil }
	missing := newBuildSession(t.TempDir(), "")
	defer missing.close()
	c, _ := missing.assess(w.entry, noBuildDaemon, w.t0)
	if c.Action != "" || !strings.Contains(c.Text, "at startup") {
		t.Fatalf("%+v", c)
	}
	alias := filepath.Join(t.TempDir(), "home-alias")
	if e := os.Symlink(w.f.dir, alias); e != nil {
		t.Fatal(e)
	}
	t.Setenv("HOME", alias)
	resolved, e := filepath.EvalSymlinks(w.target)
	if e != nil {
		t.Fatal(e)
	}
	if got := buildTilde(resolved); !strings.HasPrefix(got, "~/") {
		t.Fatalf("home alias not shortened: %s", got)
	}
	for _, label := range []string{"v0.2.2-dirty.extra", "v0.2.2+dirty.fixture"} {
		if kind, _ := buildOrder(label, "v0.2.1"); kind != "" {
			t.Fatalf("dirty source offered: %s", label)
		}
	}
}

func TestBuildSecurityMovedParentRetainsScope(t *testing.T) {
	for _, opened := range []bool{false, true} {
		t.Run(fmt.Sprint(opened), func(t *testing.T) {
			w := newBuildWorld(t, "bin")
			rec, _ := state.LoadCheckExecutable(w.entry.StateDir)
			var target *buildTarget
			var err error
			if opened {
				target, err = openBuildTarget(w.target, rec.Identity, w.entry.ConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				defer target.close()
			}
			outside := filepath.Join(t.TempDir(), "moved-bin")
			if err = os.Rename(filepath.Dir(w.target), outside); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(outside, filepath.Dir(w.target)); err != nil {
				t.Fatal(err)
			}
			if !opened {
				target, err = openBuildTarget(w.target, rec.Identity, w.entry.ConfigPath)
				if err == nil {
					target.close()
					t.Fatal("opened target outside configuration scope")
				}
				return
			}
			old := read(t, filepath.Join(outside, filepath.Base(w.target)))
			if err = target.swap(context.Background(), []byte("new")); err == nil {
				t.Fatal("swapped through moved parent")
			}
			if read(t, filepath.Join(outside, filepath.Base(w.target))) != old {
				t.Fatal("moved target changed")
			}
		})
	}
}

func TestBuildSecurityConfigChangesWhileWaiting(t *testing.T) {
	w := newBuildWorld(t, "bin")
	r := w.review(t, buildReplaceAction)
	tick, e := lock.Acquire(w.entry.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(75 * time.Millisecond)
		_ = os.WriteFile(w.entry.ConfigPath, []byte("invalid: ["), 0600)
		_ = tick.Release()
		close(released)
	}()
	old := read(t, w.target)
	res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
	<-released
	if res.Outcome == "done" || read(t, w.target) != old {
		t.Fatalf("changed config accepted: %+v", res)
	}
}
