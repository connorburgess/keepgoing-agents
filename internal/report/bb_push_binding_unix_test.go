//go:build unix

package report

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBBPushCLIBindingResolvesAliasesWithoutRunning(t *testing.T) {
	dir := t.TempDir()
	selected := filepath.Join(dir, "configured-cli")
	alias := filepath.Join(dir, "cli-alias")
	marker := filepath.Join(dir, "executed")
	if err := os.WriteFile(selected, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(selected, alias); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{alias, selected}, {selected, alias}} {
		got, err := resolveBBPushCLI(pair[0], pair[1])
		if err != nil || got != want {
			t.Fatalf("same canonical CLI refused: %q %v", got, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("metadata-only resolution executed the CLI")
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBBPushCLI(alias, ""); err != errBBPushCLI {
		t.Fatal("dangling alias did not fail closed")
	}
}
