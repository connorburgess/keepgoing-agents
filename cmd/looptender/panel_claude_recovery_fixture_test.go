package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

// Opt-in running panel fixture. Start its test binary in panelServerEnvironment
// with a disposable HOME. Only the reader is fictional; authenticated HTTP,
// catalog revisions, private cache, frontend and native CLI reads are real.
func TestPanelAccountSelectionClaudeRunningFixture(t *testing.T) {
	destination := os.Getenv("LOOPTENDER_CLAUDE_RECOVERY_FIXTURE")
	if destination == "" {
		t.Skip("running fictional panel not requested")
	}
	// TestMain clears inherited caller identifiers; restore this fixture's
	// explicit fictional panel caller before capturing the startup environment.
	t.Setenv("BB_THREAD_ID", "thr_example_panel_owner")
	if !panelQuotaProviderSupported("claude-code") {
		t.Skip("Claude retrieval requires Darwin arm64")
	}
	dir, err := accountsDirectory("")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "fictional-non-executable-provider")
	if err := os.WriteFile(cli, []byte("fictional image; production admission must refuse"), 0600); err != nil {
		t.Fatal(err)
	}
	environment := os.Environ()
	old := append(append([]string{}, environment...), "CLAUDE_CODE_OAUTH_TOKEN=fictional-old-terminal-context")
	var entries []accounts.Entry
	for _, mode := range []string{"inherited", "explicit_directory"} {
		err := accounts.Update(dir, func(f *accounts.File) error {
			e := accounts.Entry{Label: "Example Claude " + mode, Provider: "claude-code", Mode: mode, CLI: cli, Workdir: root}
			if mode == "explicit_directory" {
				e.Directory = root
			}
			e = accounts.NewEntry(*f, e, "thr_example_terminal", time.Now(), old)
			entries = append(entries, e)
			f.Entries = append(f.Entries, e)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	run := func(_ context.Context, provider, executable string, env []string, cwd string) ([]byte, error) {
		if provider != "claude-code" || executable != cli || cwd != root {
			t.Error("fixed selection changed")
		}
		calls.Add(1)
		_ = os.WriteFile(destination+".calls", []byte{byte('0' + calls.Load())}, 0600)
		return []byte(claudeQuotaUsage), nil
	}
	for _, entry := range entries {
		if _, err := accountQuotaRefresh(context.Background(), dir, entry.ID, nil, run, old, time.Now); err != nil {
			t.Fatal(err)
		}
	}
	calls.Store(0)
	_ = os.WriteFile(destination+".calls", []byte("0"), 0600)
	// This fixture never admits its fake image through production acquisition.
	if raw, err := accountClaudeQuotaRead(context.Background(), cli, environment, root); raw != nil || err == nil {
		t.Fatal("production image admission accepted fictional reader")
	}
	registry := filepath.Join(root, "registry")
	reader := &dockReader{accountsDir: dir, directory: registry, refresh: 15 * time.Second, now: time.Now}
	c, err := newPanelChanges(context.Background(), "", reader, time.Now(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	c.quotaRun = run
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s, err := newPanelState(port, 5*time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	h := &panelHandler{state: s, guard: newDockGuard(port), page: newDockServer(port, s, reader), changes: c}
	server := &http.Server{Handler: h}
	defer server.Close()
	go func() { _ = server.Serve(ln) }()
	address, _, err := s.mint("", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"url": address, "environment": panelServerEnvironment(), "ids": []string{entries[0].ID, entries[1].ID}, "catalog": filepath.Join(dir, accounts.FileName), "cache": filepath.Join(dir, accounts.ClaudeQuotaName), "registry": registry})
	if err := os.WriteFile(destination, raw, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("fixture was not stopped")
		case <-tick.C:
			if _, err := os.Stat(destination + ".stop"); err == nil {
				return
			}
		}
	}
}
