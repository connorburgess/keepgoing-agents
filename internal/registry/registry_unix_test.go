//go:build unix

package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryPrivateFilesAndAliases(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "registry")
	reference(t, dir, "example")
	for _, path := range []string{dir, filepath.Join(dir, FileName), filepath.Join(dir, LockName+".lock")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("public registry", err)
		}
	}
	for _, name := range []string{FileName, LockName + ".lock"} {
		t.Run(name, func(t *testing.T) {
			root := privateDir(t)
			other := filepath.Join(t.TempDir(), "other")
			os.WriteFile(other, []byte("unchanged"), 0600)
			if err := os.Symlink(other, filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			if _, err := Add(root, "example", filepath.Join(root, "config"), filepath.Join(root, "state")); err == nil {
				t.Fatal("alias accepted")
			}
			b, _ := os.ReadFile(other)
			if string(b) != "unchanged" {
				t.Fatal("alias target changed")
			}
		})
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("public directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, FileName), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("public file accepted")
	}
}
