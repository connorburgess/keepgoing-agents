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

func TestBBPushInheritedWhitespaceMatchesLauncher(t *testing.T) {
	dir := t.TempDir()
	selected := filepath.Join(dir, "configured-cli")
	for _, suffix := range []string{"", " ", "\ufeff", "\u0085"} {
		if err := os.WriteFile(selected+suffix, []byte("not executable\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := filepath.EvalSymlinks(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, inherited := range []string{" \t\n", "\ufeff", " \t" + selected + "\r\n", "\ufeff" + selected + "\ufeff"} {
		got, err := resolveBBPushCLI(selected, inherited)
		if err != nil || got != canonical {
			t.Fatalf("launcher-compatible override refused: %q %v", got, err)
		}
	}
	for _, suffix := range []string{" ", "\ufeff"} {
		if _, err := resolveBBPushCLI(selected+suffix, selected+suffix); err != errBBPushCLI {
			t.Fatal("trimmed override redirected to different file")
		}
		// The selected pathname itself must never be trimmed.
		got, err := resolveBBPushCLI(selected+suffix, " ")
		if err != nil || got != canonical+suffix {
			t.Fatal("literal selected path changed")
		}
	}
	// U+0085 is Go whitespace but not JavaScript trim whitespace. Trimming it
	// would incorrectly authorize an inherited override to a different file.
	if _, err := resolveBBPushCLI(selected, selected+"\u0085"); err != errBBPushCLI {
		t.Fatal("non-JavaScript whitespace was trimmed")
	}
}
