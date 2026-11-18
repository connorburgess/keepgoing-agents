package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/registry"
)

func registryPreview(t *testing.T, dir, path string) registryPublication {
	t.Helper()
	code, out, errs := registryRun(t, dir, "reconcile", "-c", path, "--thread", "thr_example")
	var r struct {
		Registration registryPublication `json:"registration"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil || r.Registration.State != "preview" || !validRegistryBinding(r.Registration.Binding) {
		t.Fatal(code, out, errs)
	}
	for _, secret := range []string{path, "thr_example", "private-missing-task"} {
		if strings.Contains(out, secret) {
			t.Fatal("private identity leaked", out)
		}
	}
	return r.Registration
}
func TestRegistryReconcileLostReplyAndControls(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example")
	path := registryConfig(t)
	dir := filepath.Join(t.TempDir(), "registry")
	runtime := filepath.Join(filepath.Dir(path), "state")
	os.Mkdir(runtime, 0700)
	original := []byte(`{"ownership":{"threadId":"thr_example","generation":7},"enabled":true,"paused":true,"awaitingUser":true,"note":"PRIVATE HELD NOTE"}`)
	os.WriteFile(filepath.Join(runtime, control.FileName), original, 0600)
	budget := []byte(`{"version":1,"used":19}`)
	os.WriteFile(filepath.Join(runtime, "sends.json"), budget, 0600)
	p := registryPreview(t, dir, path)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("preview wrote registry")
	}
	var id string
	for i := 0; i < 2; i++ {
		code, out, errs := registryRun(t, dir, "reconcile", "-c", path, "--thread", "thr_example", "--binding", p.Binding)
		var r struct {
			Registration registryPublication `json:"registration"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &r) != nil || r.Registration.State != "registered" {
			t.Fatal(code, out, errs)
		}
		if i == 0 {
			id = r.Registration.ID
		} else if id != r.Registration.ID {
			t.Fatal("lost reply duplicated identity")
		}
	}
	ctl, _ := os.ReadFile(filepath.Join(runtime, control.FileName))
	sends, _ := os.ReadFile(filepath.Join(runtime, "sends.json"))
	if !bytes.Equal(ctl, original) || !bytes.Equal(sends, budget) {
		t.Fatal("publication changed hold/ownership/accounting")
	}
	f, err := registry.Load(dir)
	if err != nil || len(f.Entries) != 1 {
		t.Fatal(f, err)
	}
	// Status/list never repairs references or starts a provider.
	before, _ := os.ReadFile(filepath.Join(dir, registry.FileName))
	registryRun(t, dir, "list")
	after, _ := os.ReadFile(filepath.Join(dir, registry.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("read rewrote registry")
	}
}
func TestRegistryReconcileChangedBindings(t *testing.T) {
	for _, which := range []string{"config", "owner", "target", "pending", "corrupt", "replaced_config", "replaced_runtime"} {
		t.Run(which, func(t *testing.T) {
			t.Setenv("BB_THREAD_ID", "thr_example")
			path := registryConfig(t)
			dir := filepath.Join(t.TempDir(), "registry")
			runtime := filepath.Join(filepath.Dir(path), "state")
			os.Mkdir(runtime, 0700)
			os.WriteFile(filepath.Join(runtime, "control.json"), []byte(`{"ownership":{"threadId":"thr_example","generation":1}}`), 0600)
			p := registryPreview(t, dir, path)
			b, err := captureRegistryBinding(path, "thr_example", "thr_example")
			if err != nil {
				t.Fatal(err)
			}
			switch which {
			case "config":
				raw, _ := os.ReadFile(path)
				os.WriteFile(path, bytes.Replace(raw, []byte("name: example-loop"), []byte("name: changed-loop"), 1), 0600)
			case "owner":
				os.WriteFile(filepath.Join(runtime, "control.json"), []byte(`{"ownership":{"threadId":"thr_other","generation":2}}`), 0600)
			case "target":
				os.WriteFile(filepath.Join(runtime, "state.json"), []byte(`{"activeTarget":"missing"}`), 0600)
			case "pending":
				os.WriteFile(filepath.Join(runtime, "control.json"), []byte(`{"pendingTransfer":{"id":"pending"}}`), 0600)
			case "corrupt":
				os.WriteFile(filepath.Join(runtime, "control.json"), []byte(`null`), 0600)
			case "replaced_runtime":
				if err := os.Rename(runtime, runtime+".previous"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(runtime, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(runtime, "control.json"), []byte(`{"ownership":{"threadId":"thr_example","generation":1}}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced_config":
				raw, _ := os.ReadFile(path)
				stage := path + ".replacement"
				os.WriteFile(stage, raw, 0600)
				os.Chtimes(stage, b.configInfo.ModTime(), b.configInfo.ModTime())
				if err := os.Rename(stage, path); err != nil {
					t.Fatal(err)
				}
			}
			if which == "replaced_config" {
				if _, err := registerBound(dir, b, "thr_example"); err == nil {
					t.Fatal("replaced config accepted")
				}
			}
			if code, _, _ := registryRun(t, dir, "reconcile", "-c", path, "--thread", "thr_example", "--binding", p.Binding); code == 0 {
				t.Fatal("changed binding accepted")
			}
			f, err := registry.Load(dir)
			if err != nil || len(f.Entries) != 0 {
				t.Fatal("refusal published", f, err)
			}
		})
	}
}
func TestRegistryReconcileConcurrentIdentityAndUnrelatedReferences(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example")
	path := registryConfig(t)
	dir := filepath.Join(t.TempDir(), "registry")
	other, err := registry.Add(dir, "other-loop", filepath.Join(t.TempDir(), "other.yaml"), filepath.Join(t.TempDir(), "other-state"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := captureRegistryBinding(path, "thr_example", "thr_example")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e, err := registerBound(dir, b, "thr_example"); err == nil {
				ids <- e.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	final, err := registerBound(dir, b, "thr_example")
	if err != nil {
		t.Fatal(err)
	}
	for id := range ids {
		if id != final.ID {
			t.Fatal("concurrent identity changed")
		}
	}
	f, _ := registry.Load(dir)
	if len(f.Entries) != 2 || f.Entries[0] != other {
		t.Fatal("unrelated registration changed", f)
	}
	// A cancelled explicit request cannot publish.
	var out, errs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := registryReconcile(env{ctx: ctx, stdout: &out, stderr: &errs}, []string{"-c", path, "--thread", "thr_example", "--directory", filepath.Join(t.TempDir(), "cancelled"), "--binding", b.digest, "--json"}); code == 0 {
		t.Fatal("cancelled request published")
	}
}

func TestRegistryReconcileRefusalsDoNotRepairPrivateRecords(t *testing.T) {
	for _, scenario := range []string{"wrong_caller", "wrong_provider", "bad_owner", "state_null", "config_symlink", "runtime_symlink", "locked", "registry_corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("BB_THREAD_ID", "thr_example")
			path := registryConfig(t)
			runtime := filepath.Join(filepath.Dir(path), "state")
			if err := os.Mkdir(runtime, 0700); err != nil {
				t.Fatal(err)
			}
			ctlPath := filepath.Join(runtime, "control.json")
			ctl := []byte(`{"enabled":true,"paused":true,"awaitingUser":true,"ownership":{"threadId":"thr_example","generation":1},"note":"PRIVATE NOTE"}`)
			if err := os.WriteFile(ctlPath, ctl, 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(t.TempDir(), "registry")
			p := registryPreview(t, dir, path)
			args := []string{"reconcile", "-c", path, "--thread", "thr_example", "--binding", p.Binding}
			switch scenario {
			case "wrong_caller":
				t.Setenv("BB_THREAD_ID", "thr_other")
			case "wrong_provider":
				args = append(args, "--provider", "other")
			case "bad_owner":
				os.WriteFile(ctlPath, []byte(`{"ownership":{"threadId":"thr_example","generation":0}}`), 0600)
			case "state_null":
				os.WriteFile(filepath.Join(runtime, "state.json"), []byte(`null`), 0600)
			case "config_symlink":
				raw, _ := os.ReadFile(path)
				os.WriteFile(path+".source", raw, 0600)
				os.Remove(path)
				if err := os.Symlink(path+".source", path); err != nil {
					t.Skip("symlink creation unavailable")
				}
			case "runtime_symlink":
				if err := os.Rename(runtime, runtime+".source"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(runtime+".source", runtime); err != nil {
					t.Skip("symlink creation unavailable")
				}
			case "locked":
				held, err := lock.Acquire(runtime)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Release()
			case "registry_corrupt":
				os.Mkdir(dir, 0700)
				os.WriteFile(filepath.Join(dir, registry.FileName), []byte(`PRIVATE INVALID REGISTRY`), 0600)
			}
			before, _ := os.ReadFile(ctlPath)
			if code, out, errs := registryRun(t, dir, args...); code == 0 {
				t.Fatal("unsafe publication", out, errs)
			}
			after, _ := os.ReadFile(ctlPath)
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed controls")
			}
			if scenario == "registry_corrupt" {
				raw, _ := os.ReadFile(filepath.Join(dir, registry.FileName))
				if string(raw) != "PRIVATE INVALID REGISTRY" {
					t.Fatal("corrupt registry changed")
				}
			} else {
				f, err := registry.Load(dir)
				if err != nil || len(f.Entries) != 0 {
					t.Fatal("refusal published", f, err)
				}
			}
		})
	}
}

func TestRegistryReconcilePreviewPinsRegistryDestination(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example")
	path := registryConfig(t)
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	p := registryPreview(t, first, path)
	if code, _, _ := registryRun(t, second, "reconcile", "-c", path, "--thread", "thr_example", "--binding", p.Binding); code == 0 {
		t.Fatal("preview authorized another registry")
	}
	f, err := registry.Load(second)
	if err != nil || len(f.Entries) != 0 {
		t.Fatal("wrong destination changed", f, err)
	}
}

// Storage publishes after the binding callback returns. Releasing the runtime
// locks at callback return would let a transfer overtake the reference save.
func TestRegistryReconcileKeepsLocksThroughPublicationAndReleasesOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		path := registryConfig(t)
		runtime := filepath.Join(filepath.Dir(path), "state")
		if err := os.Mkdir(runtime, 0700); err != nil {
			t.Fatal(err)
		}
		b, err := captureRegistryBinding(path, "thr_example", "thr_example")
		if err != nil {
			t.Fatal(err)
		}
		checked := false
		_, err = registerBoundWith(filepath.Join(t.TempDir(), "registry"), b, "thr_example", func(_, _, _, _ string, check func() error) (registry.Entry, error) {
			if err := check(); err != nil {
				return registry.Entry{}, err
			}
			checked = true
			for _, name := range []string{lock.Tick, lock.Control} {
				held, e := lock.AcquireNamed(runtime, name)
				if e == nil {
					held.Release()
					t.Fatalf("%s released before storage publication", name)
				}
				if !errors.Is(e, lock.ErrHeld) {
					t.Fatal(e)
				}
			}
			if fail {
				return registry.Entry{}, errors.New("fictional storage failure")
			}
			return registry.Entry{ConfigPath: b.path, StateDir: b.runtime}, nil
		})
		if !checked || (err != nil) != fail {
			t.Fatal(checked, err)
		}
		for _, name := range []string{lock.Tick, lock.Control} {
			held, e := lock.AcquireNamed(runtime, name)
			if e != nil {
				t.Fatalf("%s retained after return: %v", name, e)
			}
			held.Release()
		}
	}
}

func TestRegistryReconcileAliasedParentPreservesRuntimeAndOwner(t *testing.T) {
	for _, owner := range []string{"thr_other", "thr_example"} {
		t.Run(owner, func(t *testing.T) {
			root := t.TempDir()
			physical, logical := filepath.Join(root, "physical", "config"), filepath.Join(root, "logical")
			if err := os.MkdirAll(physical, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(logical, 0700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(logical, "link")
			if err := os.Symlink(physical, alias); err != nil {
				t.Skip("directory symlink unavailable")
			}
			path := filepath.Join(alias, "keepgoing.yaml")
			raw := `version: 1
loop:
  name: example-loop
  state_dir: ../state
  adapter: bb
  provider: example
  bb: {thread: thr_example, cli: nonexistent-provider-fixture}
  prompt_file: ../private-missing-task.txt
`
			if err := os.WriteFile(filepath.Join(physical, "keepgoing.yaml"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			ordinary, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(ordinary.Loop.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := control.Save(ordinary.Loop.StateDir, control.File{Enabled: true, Paused: true, Ownership: &control.Ownership{ThreadID: owner, Generation: 7}}); err != nil {
				t.Fatal(err)
			}
			ctlPath := filepath.Join(ordinary.Loop.StateDir, control.FileName)
			before, _ := os.ReadFile(ctlPath)
			b, err := captureRegistryBinding(path, "thr_example", "thr_example")
			if owner != "thr_example" {
				if err == nil {
					t.Fatal("aliased config bypassed the actual runtime owner")
				}
				after, _ := os.ReadFile(ctlPath)
				if !bytes.Equal(before, after) {
					t.Fatal("alias refusal changed controls")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expected, err := canonicalFuturePath(ordinary.Loop.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			if b.path != path || b.runtime != expected || b.cfg.Loop.PromptFile != ordinary.Loop.PromptFile {
				t.Fatal("relative config meaning changed")
			}
			dir := filepath.Join(t.TempDir(), "registry")
			entry, err := registerBound(dir, b, "thr_example")
			if err != nil {
				t.Fatal(err)
			}
			if entry.ConfigPath != path || entry.StateDir != expected {
				t.Fatal("published config/runtime differs from ordinary config.Load")
			}
			reloaded, err := config.Load(entry.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := canonicalFuturePath(reloaded.Loop.StateDir)
			if err != nil || actual != entry.StateDir {
				t.Fatal("registered client would load another runtime", err)
			}
			after, _ := os.ReadFile(ctlPath)
			if !bytes.Equal(before, after) {
				t.Fatal("alias publication changed controls")
			}
			if _, err := os.Stat(filepath.Join(root, "physical", "state")); !os.IsNotExist(err) {
				t.Fatal("publication touched substituted runtime")
			}
		})
	}
}

func TestRegistryReconcileHardlinkCannotReturnDifferentSourceReference(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	one, two, runtime := filepath.Join(root, "one"), filepath.Join(root, "two"), filepath.Join(root, "state")
	for _, dir := range []string{one, two, runtime} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf("version: 1\nloop:\n  name: example-loop\n  state_dir: %s\n  adapter: bb\n  provider: example\n  bb: {thread: thr_primary, cli: nonexistent-provider-fixture}\n  fallbacks: fallbacks.yaml\n  prompt: Continue.\n", filepath.ToSlash(runtime))
	first, second := filepath.Join(one, "keepgoing.yaml"), filepath.Join(two, "keepgoing.yaml")
	if err := os.WriteFile(first, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Skip("hardlink unavailable")
	}
	for _, x := range []struct{ dir, thread string }{{one, "thr_first"}, {two, "thr_second"}} {
		fb := fmt.Sprintf("version: 1\ncandidates:\n  - id: backup\n    adapter: bb\n    provider: codex\n    bb: {thread: %s}\n", x.thread)
		if err := os.WriteFile(filepath.Join(x.dir, "fallbacks.yaml"), []byte(fb), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := control.Save(runtime, control.File{Ownership: &control.Ownership{ThreadID: "thr_second", Generation: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime, "state.json"), []byte(`{"activeTarget":"backup"}`), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "registry")
	old, err := registry.Add(dir, "example-loop", first, runtime)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, registry.FileName))
	if err != nil {
		t.Fatal(err)
	}
	b, err := captureRegistryBinding(second, "thr_second", "thr_second")
	if err != nil {
		t.Fatal(err)
	}
	if b.provider != "codex" {
		t.Fatal("bound primary rather than current provider")
	}
	if _, err := registerBound(dir, b, "thr_second"); err == nil {
		t.Fatal("reported a stored reference with different relative fallback semantics")
	}
	after, _ := os.ReadFile(filepath.Join(dir, registry.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("alias refusal changed existing references")
	}
	f, err := registry.Load(dir)
	if err != nil || len(f.Entries) != 1 || f.Entries[0] != old {
		t.Fatal("existing identity changed", f, err)
	}
	// The existing original spelling remains usable when its own current target
	// is explicitly selected. Never silently rewrite it to the requested alias.
	original, err := captureRegistryBinding(first, "thr_first", "thr_second")
	if err != nil {
		t.Fatal(err)
	}
	same, err := registerBound(dir, original, "thr_second")
	if err != nil || same.ID != old.ID {
		t.Fatal("original identity no longer usable", same, err)
	}
}
