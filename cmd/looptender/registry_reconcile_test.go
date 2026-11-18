package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
