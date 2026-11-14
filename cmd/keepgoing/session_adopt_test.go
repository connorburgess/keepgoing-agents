package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

const adoptID = "00000000-0000-4000-8000-0000000000bb"

// adoptFixture is an enabled headless Claude Code loop that has never run,
// with a Codex and a BB fallback. Nothing is run: the CLIs do not exist.
func adoptFixture(t *testing.T) (path, target string) {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "project"), 0o700)
	writeCustom(t, filepath.Join(dir, "keepgoing.yaml"), `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: Continue the fictional loop.
  fallbacks: fallbacks.yaml
  adapter: claude
  claude: {cli: absent-claude, workdir: project}
`, 0o600)
	writeCustom(t, filepath.Join(dir, "fallbacks.yaml"), `version: 1
candidates:
  - id: codex-fallback
    adapter: codex
    provider: codex
    codex: {cli: absent-codex, workdir: project}
  - id: bb-fallback
    adapter: bb
    provider: example-provider
    bb: {thread: thr_example123, cli: absent-bb}
    probe: {kind: command, command: [absent-probe]}
`, 0o600)
	path = filepath.Join(dir, "keepgoing.yaml")
	if code, _, errs := runKeepgoing("enable", "-c", path); code != exitOK {
		t.Fatal(errs)
	}
	return path, filepath.Join(dir, "state", "targets", "primary")
}

func TestSessionAdoptCommand(t *testing.T) {
	path, target := adoptFixture(t)
	code, out, errs := runKeepgoing("session", "adopt", adoptID, "-c", path, "--json", "--note", "fictional handover")
	var r sessionAdoptReport
	if code != exitOK || json.Unmarshal([]byte(out), &r) != nil || r.Type != "session_adopt" || r.Target != "primary" || r.By != "terminal" ||
		!strings.Contains(r.Message, "Close it in the IDE first: keepgoing cannot check that.") || !strings.Contains(r.Message, claudeTwoPlacesWarning) {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "session")); strings.TrimSpace(string(b)) != adoptID {
		t.Fatalf("saved %q", b)
	}
	events, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "state", "runs.jsonl"))
	if !strings.Contains(string(events), `"event":"session_adopt"`) || !strings.Contains(string(events), "fictional handover") {
		t.Fatalf("event log: %s", events)
	}
	if _, timeline, _ := runKeepgoing("timeline", "-c", path); !strings.Contains(timeline, "session adopted from a terminal or scheduler") || strings.Contains(timeline, adoptID) {
		t.Fatalf("timeline: %s", timeline)
	}
	// Adopting again would replace the conversation it now continues.
	if code, _, errs := runKeepgoing("session", "adopt", "00000000-0000-4000-8000-0000000000cc", "-c", path); code != exitFailed || !strings.Contains(errs, "run keepgoing session reset first") {
		t.Fatalf("%d %s", code, errs)
	}
}

// Every refusal leaves nothing saved.
func TestSessionAdoptRefusals(t *testing.T) {
	nothingSaved := func(t *testing.T, target string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(target, "session")); !os.IsNotExist(err) {
			t.Fatalf("a session was saved: %v", err)
		}
	}
	for name, tc := range map[string]struct {
		args  []string
		setup func(t *testing.T, path, target string)
		code  int
		want  string
	}{
		"not a UUID":        {[]string{"session", "adopt", "sess-not-a-uuid"}, nil, exitUsage, "not shaped like a Claude Code session identifier"},
		"text":              {[]string{"session", "adopt", "my conversation about the parser"}, nil, exitUsage, "not shaped like"},
		"a Codex target":    {[]string{"session", "adopt", adoptID, "--target", "codex-fallback"}, nil, exitUsage, "target codex-fallback is codex, which documents no handover"},
		"a BB target":       {[]string{"session", "adopt", adoptID, "--target", "bb-fallback"}, nil, exitUsage, "target bb-fallback is bb"},
		"an unknown target": {[]string{"session", "adopt", adoptID, "--target", "nobody"}, nil, exitUsage, `no target "nobody"`},
		"no identifier":     {[]string{"session", "adopt"}, nil, exitUsage, "usage: keepgoing session adopt SESSION_ID"},
		"a stop pending": {[]string{"session", "adopt", adoptID}, func(t *testing.T, path, _ string) {
			if code, _, errs := runKeepgoing("stop", "-c", path); code != exitOK {
				t.Fatal(errs)
			}
		}, exitFailed, "a stop is pending"},
		"a turn running": {[]string{"session", "adopt", adoptID}, func(t *testing.T, _, target string) {
			os.MkdirAll(target, 0o700)
			writeCustom(t, filepath.Join(target, "run.json"), fmt.Sprintf(`{"pid":%d,"stopKey":"example-key","startedAt":"2026-01-01T00:00:00Z","argv0":"absent-claude"}`, os.Getpid()), 0o600)
		}, exitFailed, "a turn is running on primary"},
		"a check running": {[]string{"session", "adopt", adoptID}, func(t *testing.T, path, _ string) {
			held, err := lock.Acquire(filepath.Join(filepath.Dir(path), "state"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { held.Release() })
		}, exitSkipped, "a check is running"},
		"not the owner": {[]string{"session", "adopt", adoptID}, func(t *testing.T, path, _ string) {
			t.Setenv("BB_THREAD_ID", "thr_example_owner")
			if code, _, errs := runKeepgoing("enable", "-c", path); code != exitOK {
				t.Fatal(errs)
			}
			t.Setenv("BB_THREAD_ID", "thr_example_other")
		}, exitFailed, "ownership:"},
	} {
		t.Run(name, func(t *testing.T) {
			path, target := adoptFixture(t)
			if tc.setup != nil {
				tc.setup(t, path, target)
			}
			code, _, errs := runKeepgoing(append(tc.args, "-c", path)...)
			if code != tc.code || !strings.Contains(errs, tc.want) {
				t.Fatalf("%d %s", code, errs)
			}
			nothingSaved(t, target)
		})
	}
}
