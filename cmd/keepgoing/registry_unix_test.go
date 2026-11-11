//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/registry"
)

func TestRegistryCLICanonicalAliasesAndProjectPaths(t *testing.T) {
	cfg := registryConfig(t)
	dir := filepath.Join(t.TempDir(), "registry")
	code, out, errs := registryRun(t, dir, "add", "-c", cfg)
	if code != 0 {
		t.Fatal(out, errs)
	}
	alias := filepath.Join(t.TempDir(), "alias.yaml")
	if err := os.Symlink(cfg, alias); err != nil {
		t.Fatal(err)
	}
	code, aliased, errs := registryRun(t, dir, "add", "-c", alias)
	if code == 0 {
		t.Fatal("config symlink accepted", aliased, errs)
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0700)
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := registryRun(t, filepath.Join(link, "registry"), "add", "-c", cfg); code == 0 {
		t.Fatal("aliased checkout accepted")
	}
	project := t.TempDir()
	body, _ := os.ReadFile(cfg)
	// Point the configured progress root at a directory that is not yet a Git checkout.
	body = append(body, []byte("  progress:\n    repositories: ["+strconv.Quote(project)+"]\n")...)
	os.WriteFile(cfg, body, 0600)
	if code, _, _ := registryRun(t, filepath.Join(project, "registry"), "add", "-c", cfg); code == 0 {
		t.Fatal("progress project accepted")
	}
	if _, err := os.Stat(filepath.Join(project, "registry")); !os.IsNotExist(err) {
		t.Fatal("project written")
	}
	// Changing state_dir does not silently repoint an existing reference.
	body = []byte(strings.Replace(string(body), "state_dir: state", "state_dir: other-state", 1))
	os.WriteFile(cfg, body, 0600)
	if code, _, _ := registryRun(t, dir, "add", "-c", cfg); code == 0 {
		t.Fatal("binding silently changed")
	}
}

func TestRegistryCLIPreservesRelativePathsThroughParentAlias(t *testing.T) {
	cfg := registryConfig(t)
	body, _ := os.ReadFile(cfg)
	body = []byte(strings.Replace(string(body), "state_dir: state", "state_dir: ../relative-state", 1))
	os.WriteFile(cfg, body, 0600)
	parent := t.TempDir()
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(filepath.Dir(cfg), alias); err != nil {
		t.Fatal(err)
	}
	aliasedConfig := filepath.Join(alias, filepath.Base(cfg))
	dir := filepath.Join(t.TempDir(), "registry")
	if code, out, errs := registryRun(t, dir, "add", "-c", aliasedConfig); code != 0 {
		t.Fatal(out, errs)
	}
	f, err := registry.Load(dir)
	expected, _ := canonicalFuturePath(filepath.Join(parent, "relative-state"))
	if err != nil || len(f.Entries) != 1 || f.Entries[0].StateDir != expected || f.Entries[0].ConfigPath != aliasedConfig {
		t.Fatal("relative config paths were rebased", f, err)
	}
}
