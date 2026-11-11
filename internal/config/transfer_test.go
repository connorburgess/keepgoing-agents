package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBBTransferEditPreservesConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loop.yaml")
	raw := []byte("# private config\nversion: 1\nloop:\n  name: fixture\n  state_dir: ./state\n  adapter: bb\n  provider: codex\n  bb:\n    thread: thr_source # intended primary\n    model: example-model\n  prompt: 'PRIVATE-PROMPT'\n  limits:\n    max_sends: 17\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := c.PrepareBBTransfer("thr_destination")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if !bytes.Equal(raw, before) {
		t.Fatal("prepare wrote config")
	}
	if edit.AfterDigest() == c.SourceDigest() || len(edit.AfterDigest()) != 64 {
		t.Fatal("invalid digests")
	}
	if err := edit.Commit(); err != nil {
		t.Fatal(err)
	}
	next, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if next.Loop.BB.Thread != "thr_destination" || next.Loop.Prompt != "PRIVATE-PROMPT" || next.Loop.BB.Model != "example-model" || next.Loop.Limits.MaxSends != 17 || next.Loop.StateDir != c.Loop.StateDir || next.SourceDigest() != edit.AfterDigest() {
		t.Fatal("unexpected config change")
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "# intended primary") {
		t.Fatal("comment lost")
	}
	if c.CheckSources() == nil {
		t.Fatal("old snapshot remained usable")
	}
	if edit.Commit() == nil {
		t.Fatal("edit replay accepted")
	}
}

func TestBBTransferEditRejectsSourceChangesAndAliases(t *testing.T) {
	for _, kind := range []string{"changed", "anchor", "symlink", "trailing_doc"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "loop.yaml")
			raw := "version: 1\nloop:\n  name: fixture\n  state_dir: ./state\n  adapter: bb\n  bb:\n    thread: thr_source\n  prompt: example\n"
			if kind == "anchor" {
				raw = strings.Replace(raw, "thread: thr_source", "thread: &target thr_source", 1)
			}
			if kind == "trailing_doc" {
				raw += "---\nprivate: secret\n"
			}
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				link := filepath.Join(dir, "alias.yaml")
				if err := os.Symlink(path, link); err != nil {
					t.Skip(err)
				}
				path = link
			}
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			edit, err := c.PrepareBBTransfer("thr_destination")
			if kind != "changed" {
				if err == nil {
					t.Fatal("unsafe input accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			changed := []byte(raw + "# concurrent edit\n")
			os.WriteFile(path, changed, 0600)
			if edit.Commit() == nil {
				t.Fatal("stale source replaced")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(changed, after) {
				t.Fatal("concurrent edit lost")
			}
		})
	}
}
