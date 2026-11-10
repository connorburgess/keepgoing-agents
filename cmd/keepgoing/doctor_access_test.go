package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestDoctorAccessFailuresAreClassifiedAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"allowed", nil, "ok"},
		{"denied", os.ErrPermission, "error"},
		{"readonly", syscall.EROFS, "error"},
		{"ancestor_file", syscall.ENOTDIR, "error"},
		{"unsupported", errAccessUnavailable, "unavailable"},
		{"unknown", errors.New(doctorPrivate), "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r doctorReport
			doctorStateAccess(&r, t.TempDir(), func(string, uint32) error { return tc.err })
			c := doctorCheckByID(t, r, "state.write_access")
			if c.Status != tc.want || strings.Contains(c.Message+c.Fix, doctorPrivate) {
				t.Fatalf("%+v", c)
			}
		})
	}
}

func TestDoctorAccessMissingStateChecksNearestAncestorWithoutCreating(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "missing", "state")
	var r doctorReport
	var calls int
	doctorStateAccess(&r, dir, func(p string, mode uint32) error {
		calls++
		if p != root || mode != accessWrite|accessSearch {
			t.Fatalf("wrong ancestor/access: %s %d", p, mode)
		}
		return nil
	})
	c := doctorCheckByID(t, r, "state.write_access")
	if calls != 1 || c.Status != "ok" || !strings.Contains(c.Message, "nearest existing ancestor") {
		t.Fatalf("%+v, calls %d", c, calls)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("access check created runtime state")
	}
}

func TestDoctorAccessMatchesAppendLockAndAtomicWriteRequirements(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"control.json", "state.json", "health.json", "runs.jsonl", "tick.lock", "daemon.lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(doctorPrivate), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	var r doctorReport
	got := map[string]uint32{}
	doctorStateAccess(&r, dir, func(p string, mode uint32) error {
		got[p] = mode
		return nil
	})
	want := map[string]uint32{dir: accessWrite | accessSearch, filepath.Join(dir, "runs.jsonl"): accessWrite, filepath.Join(dir, "tick.lock"): accessRead | accessWrite, filepath.Join(dir, "daemon.lock"): accessRead | accessWrite}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("access checks: %v", got)
	}
	// Read-only atomic records can still be replaced through the directory.
	// Do not incorrectly demand direct write access to their existing inodes.
	for name := range got {
		if name == dir {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil || string(b) != doctorPrivate {
			t.Fatal("access check changed runtime content")
		}
	}
}

func TestDoctorAccessRejectsBrokenAncestorAndSkipsLockSymlink(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "broken")
	if err := os.Symlink(filepath.Join(root, "missing"), broken); err != nil {
		t.Skip("symlinks unavailable")
	}
	var r doctorReport
	doctorStateAccess(&r, filepath.Join(broken, "state"), func(string, uint32) error {
		t.Fatal("access check followed a broken ancestor")
		return nil
	})
	if doctorCheckByID(t, r, "state.write_access").Status != "error" {
		t.Fatalf("%+v", r)
	}
	if err := os.Symlink(broken, filepath.Join(root, "tick.lock")); err != nil {
		t.Fatal(err)
	}
	r = doctorReport{}
	doctorStateAccess(&r, root, func(p string, _ uint32) error {
		if p != root {
			t.Fatal("append/lock symlink was followed")
		}
		return nil
	})
	if doctorCheckByID(t, r, "state.tick.lock.write_access").Status != "unavailable" {
		t.Fatalf("%+v", r)
	}
}

func TestDoctorAccessOfflinePreservesHoldAndFiles(t *testing.T) {
	f := newDoctorFixture(t)
	if err := control.Save(f.state, control.File{Enabled: true, Paused: true, Stop: true, Completed: true, Note: doctorPrivate}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.state, control.FileName)
	before, _ := os.ReadFile(path)
	_, r, _ := f.run(t, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		t.Fatal("offline access check ran a command")
		return nil, nil
	}, "--offline")
	doctorCheckByID(t, r, "state.write_access")
	after, _ := os.ReadFile(path)
	entries, err := os.ReadDir(f.state)
	if !reflect.DeepEqual(before, after) || err != nil || len(entries) != 1 || entries[0].Name() != control.FileName {
		t.Fatal("doctor changed held runtime state")
	}
}
