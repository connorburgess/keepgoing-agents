package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
)

func registryRun(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	args = append([]string{"registry"}, args...)
	args = append(args, "--directory", dir, "--json")
	code := run(context.Background(), args, &out, &errs)
	return code, out.String(), errs.String()
}
func registryConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keepgoing.yaml")
	// A deliberately nonexistent CLI and prompt file prove discovery does not
	// need a provider installation or read task content.
	body := `version: 1
loop:
  name: example-loop
  state_dir: state
  adapter: bb
  provider: example
  bb: {thread: thr_example, cli: nonexistent-provider-fixture}
  prompt_file: private-missing-task.txt
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestRegistryCLIReferencesDoNotControlLoops(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "registry")
	cfg := registryConfig(t)
	runtime := filepath.Join(filepath.Dir(cfg), "state")
	if err := os.Mkdir(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	// Deliberately opaque bytes: discovery must not load or rewrite control.
	control := filepath.Join(runtime, "control.json")
	original := []byte("PRIVATE-HELD-CONTROL")
	os.WriteFile(control, original, 0600)
	code, out, errs := registryRun(t, dir, "add", "-c", cfg)
	var added registryOutput
	if code != 0 || json.Unmarshal([]byte(out), &added) != nil || len(added.Entries) != 1 {
		t.Fatal(code, out, errs)
	}
	id := added.Entries[0].ID
	for _, args := range [][]string{{"list"}, {"add", "-c", cfg}} {
		code, out, errs = registryRun(t, dir, args...)
		if code != 0 || strings.Contains(out+errs, filepath.Dir(cfg)) || strings.Contains(out+errs, "PRIVATE") {
			t.Fatal(code, out, errs)
		}
	}
	code, out, errs = registryRun(t, dir, "list", "--private")
	var full registryOutput
	if code != 0 || json.Unmarshal([]byte(out), &full) != nil || full.Entries[0].ConfigPath == "" || full.Entries[0].StateDir == "" {
		t.Fatal(code, out, errs)
	}
	saved, _ := os.ReadFile(filepath.Join(dir, registry.FileName))
	if strings.Contains(string(saved), "prompt") || strings.Contains(string(saved), "thr_example") {
		t.Fatal("registry copied task/target fields")
	}
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, errs = registryRun(t, dir, "list")
	if code != 0 || !strings.Contains(out, id) {
		t.Fatal(code, out, errs)
	}
	code, out, errs = registryRun(t, dir, "remove", "--id", id)
	if code != 0 {
		t.Fatal(code, out, errs)
	}
	after, _ := os.ReadFile(control)
	if string(after) != string(original) {
		t.Fatal("registration changed controls")
	}
	files, _ := os.ReadDir(runtime)
	if len(files) != 1 {
		t.Fatal("discovery created runtime files")
	}
}
func TestRegistryCLIRejectsCheckoutLockAndBadInput(t *testing.T) {
	cfg := registryConfig(t)
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0700)
	if code, _, _ := registryRun(t, filepath.Join(root, "registry"), "add", "-c", cfg); code == 0 {
		t.Fatal("checkout accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "registry")); !os.IsNotExist(err) {
		t.Fatal("checkout written")
	}
	dir := filepath.Join(t.TempDir(), "registry")
	if code, _, _ := registryRun(t, dir, "list"); code != 0 {
		t.Fatal("missing list failed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("list wrote registry")
	}
	held, err := lock.AcquireNamed(dir, registry.LockName)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := registryRun(t, dir, "add", "-c", cfg)
	held.Release()
	if code != exitSkipped {
		t.Fatal("lock result", code)
	}
	os.WriteFile(cfg, []byte("PRIVATE-BROKEN-CONFIG"), 0600)
	code, out, errs := registryRun(t, dir, "add", "-c", cfg)
	if code == 0 || strings.Contains(out+errs, "PRIVATE") || strings.Contains(out+errs, cfg) {
		t.Fatal("bad config exposed", code, out, errs)
	}
	for _, args := range [][]string{{"remove"}, {"add"}, {"list", "--id", "other"}, {"remove", "--id", "../other"}, {"list", "--unknown"}, {"list", "unexpected"}} {
		if code, _, _ := registryRun(t, dir, args...); code == 0 {
			t.Fatal("invalid args accepted", args)
		}
	}
}
