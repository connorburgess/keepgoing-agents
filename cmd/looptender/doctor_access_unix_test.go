//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorOSAccessUsesKernelPermissionsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	if errors.Is(doctorOSAccess(dir, accessWrite|accessSearch), errAccessUnavailable) {
		t.Skip("ordinary matching real/effective non-root identity required")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	var r doctorReport
	doctorStateAccess(&r, dir, doctorOSAccess)
	if doctorCheckByID(t, r, "state.write_access").Status != "error" {
		t.Fatalf("read-only directory not detected: %+v", r)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tick.lock")
	if err := os.WriteFile(path, []byte("example lock"), 0o400); err != nil {
		t.Fatal(err)
	}
	r = doctorReport{}
	doctorStateAccess(&r, dir, doctorOSAccess)
	if doctorCheckByID(t, r, "state.write_access").Status != "ok" || doctorCheckByID(t, r, "state.tick.lock.write_access").Status != "error" {
		t.Fatalf("read-only lock not detected: %+v", r)
	}
	b, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || string(b) != "example lock" || info.Mode().Perm() != 0o400 {
		t.Fatal("access check changed lock content or permissions")
	}
}
