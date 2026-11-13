package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "registry")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func reference(t *testing.T, dir, name string) Entry {
	t.Helper()
	e, err := Add(dir, name, filepath.Join(filepath.Dir(dir), name+".yaml"), filepath.Join(filepath.Dir(dir), name+"-state"))
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestRegistrySnapshotAndBindings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "registry")
	f, err := Load(dir)
	if err != nil || len(f.Entries) != 0 {
		t.Fatal(f, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created directory")
	}
	a := reference(t, dir, "example")
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	b := reference(t, dir, "example")
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if a != b || string(before) != string(after) {
		t.Fatal("idempotent add rewrote identity")
	}
	for _, values := range [][3]string{{"renamed", a.ConfigPath, a.StateDir}, {a.Loop, a.ConfigPath, a.StateDir + "-other"}, {a.Loop, a.ConfigPath + "-copy", a.StateDir}} {
		if _, err := Add(dir, values[0], values[1], values[2]); err == nil {
			t.Fatal("changed/duplicate binding accepted")
		}
	}
	after, _ = os.ReadFile(filepath.Join(dir, FileName))
	if string(before) != string(after) {
		t.Fatal("rejection changed file")
	}
	if err := Remove(dir, a.ID); err != nil {
		t.Fatal(err)
	}
	f, err = Load(dir)
	if err != nil || len(f.Entries) != 0 {
		t.Fatal(f, err)
	}
	if err := Remove(dir, a.ID); err == nil {
		t.Fatal("missing ID reported removed")
	}
	c := reference(t, dir, "example")
	if c.ID == a.ID {
		t.Fatal("deleted ID reused")
	}
}

func TestRegistryCorruptionAndLimits(t *testing.T) {
	for _, body := range []string{`null`, `{"schemaVersion":2,"entries":[]}`, `{"schemaVersion":1,"entries":null}`, `{"schemaVersion":1,"entries":[],"PRIVATE":"SECRET"}`, `{"schemaVersion":1,"entries":[]} {}`, string(make([]byte, maxBytes+1))} {
		dir := privateDir(t)
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); !errors.Is(err, ErrInvalid) {
			t.Fatal("corrupt read accepted", err)
		}
		if _, err := Add(dir, "example", filepath.Join(dir, "example.yaml"), filepath.Join(dir, "state")); !errors.Is(err, ErrInvalid) {
			t.Fatal("corrupt write accepted", err)
		}
		after, _ := os.ReadFile(path)
		if string(after) != body {
			t.Fatal("corruption overwritten")
		}
	}
	dir := privateDir(t)
	for i := 0; i < MaxEntries; i++ {
		reference(t, dir, fmt.Sprintf("example-%d", i))
	}
	reference(t, dir, "example-0")
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	if _, err := Add(dir, "overflow", filepath.Join(dir, "other.yaml"), filepath.Join(dir, "other-state")); err == nil {
		t.Fatal("limit ignored")
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if string(before) != string(after) {
		t.Fatal("limit discarded data")
	}
}

func TestRegistryConcurrentWritersAndLock(t *testing.T) {
	dir := privateDir(t)
	held, err := lock.AcquireNamed(dir, LockName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(dir, "example", filepath.Join(dir, "config"), filepath.Join(dir, "state")); !errors.Is(err, lock.ErrHeld) {
		t.Fatal("lock not respected", err)
	}
	held.Release()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	deadline := time.Now().Add(2 * time.Minute)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("loop-%d", i)
			// Retry for as long as another writer holds the lock. A fixed budget of
			// two hundred short retries only measured how slow the filesystem was.
			// The overall limit is far beyond sixteen short writes on any host and
			// exists to name a leaked lock instead of hanging until the test timeout.
			for {
				_, err := Add(dir, name, filepath.Join(dir, name+".yaml"), filepath.Join(dir, name))
				if !errors.Is(err, lock.ErrHeld) {
					errs <- err
					return
				}
				if time.Now().After(deadline) {
					errs <- errors.New("registry lock still held after two minutes: a holder leaked it or never finished")
					return
				}
				runtime.Gosched()
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f, err := Load(dir)
	if err != nil || len(f.Entries) != 16 {
		t.Fatal("lost registration", f, err)
	}
}

func TestRegistryRejectsAmbiguousEntries(t *testing.T) {
	for _, kind := range []string{"id", "config", "state", "loop", "relative"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateDir(t)
			a := reference(t, dir, "first")
			b := reference(t, dir, "second")
			switch kind {
			case "id":
				b.ID = a.ID
			case "config":
				b.ConfigPath = a.ConfigPath
			case "state":
				b.StateDir = a.StateDir
			case "loop":
				b.Loop = "\x1b[31m"
			case "relative":
				b.ConfigPath = "../config.yaml"
			}
			raw, _ := json.Marshal(File{SchemaVersion: 1, Entries: []Entry{a, b}})
			os.WriteFile(filepath.Join(dir, FileName), raw, 0600)
			if _, err := Load(dir); !errors.Is(err, ErrInvalid) {
				t.Fatal("ambiguous record accepted", err)
			}
			if err := Remove(dir, a.ID); !errors.Is(err, ErrInvalid) {
				t.Fatal("ambiguous record edited", err)
			}
		})
	}
}

func TestRegistryPinnedEntry(t *testing.T) {
	dir := privateDir(t)
	entry := reference(t, dir, "example")
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	sentinel := errors.New("callback rejected")
	err := WithEntry(dir, entry.ID, func(got Entry) error {
		if got != entry {
			t.Fatal(got)
		}
		if err := Remove(dir, entry.ID); !errors.Is(err, lock.ErrHeld) {
			t.Fatal("removal was not fenced", err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if string(before) != string(after) {
		t.Fatal("callback changed registry")
	}
	if err := Remove(dir, entry.ID); err != nil {
		t.Fatal("lock not released", err)
	}
	called := false
	if err := WithEntry(dir, entry.ID, func(Entry) error { called = true; return nil }); err == nil || called {
		t.Fatal("removed reference used")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := WithEntry(missing, entry.ID, func(Entry) error { called = true; return nil }); err == nil || called {
		t.Fatal("missing registry used")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("missing registry created")
	}
}
