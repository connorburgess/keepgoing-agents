//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/testexec"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

func TestBuildReviewCustomRegistryChecksDefaultSibling(t *testing.T) {
	w := newBuildWorld(t, "bin")
	custom := filepath.Join(w.f.dir, "custom-registry")
	os.Mkdir(custom, 0700)
	w.builds.registry = custom
	base, e := buildUserConfigHome()
	if e != nil {
		t.Fatal(e)
	}
	defaults := filepath.Join(base, userdir.Name, "registry")
	os.MkdirAll(defaults, 0700)
	t.Cleanup(func() { os.Remove(filepath.Join(defaults, registry.FileName)) })
	other := w.entry
	other.ID = "loop_1123456789abcdef0123456789abcdef"
	other.Loop = "default-sibling"
	other.ConfigPath = filepath.Join(w.f.dir, "other.yaml")
	other.StateDir = filepath.Join(w.f.dir, "other-state")
	os.MkdirAll(other.StateDir, 0700)
	if e = state.SaveCheckExecutable(other.StateDir, w.target, "v0.2.1", w.t0); e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(registry.File{SchemaVersion: 1, Entries: []registry.Entry{other}})
	if e = os.WriteFile(filepath.Join(defaults, registry.FileName), data, 0600); e != nil {
		t.Fatal(e)
	}
	// A duplicate reference in both registries is reported once.
	if e = os.WriteFile(filepath.Join(custom, registry.FileName), data, 0600); e != nil {
		t.Fatal(e)
	}
	c, _ := w.builds.assess(w.entry, noBuildDaemon, w.t0)
	if c.Action != "" || strings.Count(c.Text, "default-sibling") != 1 {
		t.Fatalf("%+v", c)
	}
	if e = os.WriteFile(filepath.Join(defaults, registry.FileName), []byte("invalid"), 0600); e != nil {
		t.Fatal(e)
	}
	c, _ = w.builds.assess(w.entry, noBuildDaemon, w.t0)
	if c.Action != "" || !strings.Contains(c.Text, "could not be checked") {
		t.Fatalf("%+v", c)
	}
	// Restore isolated shared test default registry for later tests.

}

func TestBuildReviewConfigBinding(t *testing.T) {
	for _, action := range []string{buildReplaceAction, buildUndoAction} {
		for _, change := range []string{"name", "state"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				w := newBuildWorld(t, "bin")
				if action == buildUndoAction {
					r := w.review(t, buildReplaceAction)
					res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
					if res.Outcome != "done" {
						t.Fatalf("%+v", res)
					}
				}
				r := w.review(t, action)
				tick, e := lock.Acquire(w.entry.StateDir)
				if e != nil {
					t.Fatal(e)
				}
				old := read(t, w.target)
				config := read(t, w.entry.ConfigPath)
				if change == "name" {
					config = strings.Replace(config, "name: example-loop", "name: another-loop", 1)
				} else {
					config = strings.Replace(config, "state_dir: state", "state_dir: other-state", 1)
				}
				done := make(chan struct{})
				go func() {
					time.Sleep(75 * time.Millisecond)
					_ = os.WriteFile(w.entry.ConfigPath, []byte(config), 0600)
					_ = tick.Release()
					close(done)
				}()
				res := w.builds.apply(context.Background(), w.entry, action, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
				<-done
				if res.Outcome == "done" || read(t, w.target) != old {
					t.Fatalf("valid changed config accepted: %+v", res)
				}
				ctl, _ := panelReadControl(w.entry.StateDir)
				if _, ref := w.builds.review(context.Background(), w.entry, panelLoop{}, ctl, action, noBuildDaemon, w.t0); ref == nil {
					t.Fatal("prepare accepted mismatched registration")
				}
			})
		}
	}
}

func TestBuildReviewRunningOldImageCannotRecordNewPath(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs real keepgoing")
	}
	f := newFixture(t)
	f.run("enable")
	dir := filepath.Join(f.dir, "image")
	os.Mkdir(dir, 0700)
	old := filepath.Join(dir, "keepgoing")
	next := filepath.Join(dir, "new-build")
	buildBinary := func(path, label string) {
		cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+label, "-o", path, ".")
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("build %v %s", e, out)
		}
	}
	buildBinary(old, "v0.2.1")
	buildBinary(next, "v9.5.0")
	// Block the real check in a disposable bb adapter after process startup,
	// then atomically replace its executable pathname before its final save.
	bb := filepath.Join(f.dir, "blocking-bb")
	testexec.Script(t, bb, `#!/bin/sh
if [ "$1 $2" = "thread show" ]; then
 touch "$FAKE_BB/image-ready"
 while [ ! -f "$FAKE_BB/image-release" ]; do sleep 0.05; done
fi
`+strings.TrimPrefix(fakeBB, "#!/bin/sh\n"))
	cfg := strings.Replace(read(t, f.cfg), "cli: "+filepath.Join(f.dir, "bin", "bb"), "cli: "+bb, 1)
	os.WriteFile(f.cfg, []byte(cfg), 0600)
	cmd := exec.Command(old, "tick", "-c", f.cfg)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	ready := filepath.Join(f.bbDir, "image-ready")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, e := os.Stat(ready); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("check did not reach adapter")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Install a real newer binary while the old image is still checking.
	if e := os.Rename(next, old); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(f.bbDir, "image-release"), nil, 0600)
	if e := cmd.Wait(); e != nil {
		t.Fatalf("check: %v %s", e, out.String())
	}
	newVersion, e := exec.Command(old, "version").CombinedOutput()
	if e != nil || !strings.Contains(string(newVersion), "v9.5.0") {
		t.Fatalf("new binary changed: %v %s", e, newVersion)
	}
	record, e := state.LoadCheckExecutable(filepath.Join(f.dir, "state"))
	if e != nil {
		t.Fatal(e)
	}
	if record != nil {
		t.Fatalf("old running code claimed new file identity: %+v", record)
	}
}
