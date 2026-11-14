package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A configuration's sources are unavailable when a file it was read from can
// no longer be read at all; a file that changed but can be read is not.
func TestSourcesUnavailable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keepgoing.yaml")
	body := "version: 1\nloop:\n  name: example-loop\n  state_dir: state\n  adapter: process\n  process: {status_command: [/bin/true], resume_command: [/bin/true]}\n  prompt: Continue the fictional loop.\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if at := c.ReadAt(); at.Before(before.Add(-time.Second)) || at.After(time.Now().UTC()) {
		t.Fatalf("read at %v", at)
	}
	if c.SourcesUnavailable() {
		t.Fatal("an unchanged file is unavailable")
	}
	if err := os.WriteFile(path, []byte(body+"# changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c.SourcesUnavailable() || c.CheckSources() == nil {
		t.Fatal("a changed, readable file: not unavailable, but changed")
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if c.SourcesUnavailable() {
		t.Fatal("an oversized readable file is not unavailable")
	}
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if !c.SourcesUnavailable() {
		t.Fatal("a moved file is available")
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if !c.SourcesUnavailable() {
		t.Fatal("a directory in its place is available")
	}
	if (&Config{}).SourcesUnavailable() || !(&Config{}).ReadAt().IsZero() {
		t.Fatal("a configuration from memory has unavailable sources or a read time")
	}
}
