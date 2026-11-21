package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/registry"
)

func TestEnablePublishesToExistingDockReader(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	d := &dockReader{query: inspectDaemon, staleAfter: defaultStaleAfter, accountsDir: t.TempDir(), ctx: context.Background(), directory: os.Getenv(registryDirEnv), now: func() time.Time { return now }}
	before, err := d.json(false)
	if err != nil || strings.Contains(string(before), "example-loop") {
		t.Fatalf("before: %s %v", before, err)
	}
	for i := 0; i < 2; i++ {
		if code, out, errs := f.run("enable"); code != exitOK || strings.Contains(out, "Local loop references") || strings.Contains(errs, "reference could not") {
			t.Fatal(code, out, errs)
		}
	}
	refs, err := registry.Load(os.Getenv(registryDirEnv))
	if err != nil || len(refs.Entries) != 1 || refs.Entries[0].ConfigPath != f.cfg {
		t.Fatal(refs, err)
	}
	cached := d.cache[false]
	cached.at = time.Now().Add(-3 * time.Second)
	d.cache[false] = cached
	now = now.Add(3 * time.Second)
	after, err := d.json(false)
	if err != nil || !strings.Contains(string(after), "example-loop") {
		t.Fatalf("reader did not discover enabled loop: %s %v", after, err)
	}
	if f.read("calls") != "" || f.read("probes") != "" {
		t.Fatal("publication called provider")
	}
}

func TestEnablePublicationFailureDoesNotReplayControl(t *testing.T) {
	f := newFixture(t)
	dir := os.Getenv(registryDirEnv)
	if err := os.WriteFile(dir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, errs := f.run("enable")
	if code != exitOK || !strings.Contains(errs, "reference could not be saved") {
		t.Fatal(code, errs)
	}
	if j := controlHistory(t, f); len(j.Records) != 1 {
		t.Fatalf("lifecycle replayed: %+v", j)
	}
	if b, err := os.ReadFile(dir); err != nil || string(b) != "blocked" {
		t.Fatal("registry obstruction changed", err)
	}
	if _, err := os.Stat(filepath.Join(dir, registry.FileName)); err == nil {
		t.Fatal("unexpected registry")
	}
}

func TestRefusedEnableDoesNotPublish(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	if code, _, errs := f.run("enable"); code != exitOK {
		t.Fatal(code, errs)
	}
	if err := os.RemoveAll(os.Getenv(registryDirEnv)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BB_THREAD_ID", "thr_other_owner")
	if code, _, _ := f.run("enable"); code == exitOK {
		t.Fatal("unexpected ownership success")
	}
	refs, err := registry.Load(os.Getenv(registryDirEnv))
	if err != nil || len(refs.Entries) != 0 {
		t.Fatal(refs, err)
	}
}
