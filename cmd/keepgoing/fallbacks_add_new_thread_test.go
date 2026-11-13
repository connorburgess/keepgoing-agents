package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// A BB fallback may start a new thread at handoff instead of naming one.
// Adding it creates nothing and runs no bb command.
func TestFallbacksAddBBNewThread(t *testing.T) {
	f := newBBAddFixture(t)
	args := []string{"--non-interactive", "--agent", "bb", "--new-thread", "--provider", "codex", "--model", "example-large", "--reasoning", "high", "--group", "codex-team"}
	code, preview, errOut := f.add(t, "", append(args, "--dry-run")...) // the default lookup fails the test if bb is ever run
	if code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	for _, want := range []string{
		"Thread:", "none yet: a new thread, created at handoff",
		`creates a BB thread titled "keepgoing: `, "(bb thread spawn) with the handoff message as its first prompt",
		"Nothing is created now.",
		"A handoff to this fallback makes keepgoing create a BB", "At most 3 such threads per loop (loop.limits.max_new_threads), and at most one an hour",
		"Permissions:", "accept-edits (set on the new thread; never a project default)",
		"Permission mode accept-edits: the new thread asks before anything but file edits",
		"works in the same environment and checkout as the conversation it continues", "BB may retry it later",
	} {
		// Warnings are wrapped, so line breaks are not part of the wording.
		if !strings.Contains(strings.Join(strings.Fields(preview), " "), want) {
			t.Errorf("preview missing %q:\n%s", want, preview)
		}
	}
	if code, _, errOut := f.add(t, "", args...); code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.FallbackSet.Candidates[0]
	if c.Adapter != "bb" || c.Provider != "codex" || c.Group != "codex-team" || c.BB == nil || !c.BB.NewThread || c.BB.Thread != "" || c.BB.Model != "example-large" || c.BB.Reasoning != "high" || c.Probe.Kind != "codex" {
		t.Fatalf("candidate %+v bb=%+v", c, c.BB)
	}
	if c.BB.PermissionMode != "" || c.BB.NewThreadPermission() != "accept-edits" {
		t.Fatalf("permission mode %q", c.BB.PermissionMode)
	}
	if c.Describe() != "bb new thread (example-large)" && !strings.Contains(c.Describe(), "new thread") {
		t.Fatalf("describe %q", c.Describe())
	}

	// JSON says what kind of thread it is and never invents an ID.
	f = newBBAddFixture(t)
	code, out, errOut := f.add(t, "", "--non-interactive", "--json", "--dry-run", "--agent", "bb", "--new-thread", "--provider", "codex")
	var r fallbackAddReport
	if code != exitOK || json.Unmarshal([]byte(out), &r) != nil || r.Candidate.Thread != "new_thread" || r.Candidate.ThreadID != "" {
		t.Fatalf("%d %s %s", code, out, errOut)
	}

	// A chosen permission mode is stored and shown.
	f = newBBAddFixture(t)
	code, preview, errOut = f.add(t, "", "--non-interactive", "--agent", "bb", "--new-thread", "--provider", "codex", "--permission-mode", "full")
	cfg, err = config.Load(f.cfg)
	if code != exitOK || err != nil || cfg.FallbackSet.Candidates[0].BB.PermissionMode != "full" || !strings.Contains(strings.Join(strings.Fields(preview), " "), "Permission mode full: the new thread acts without asking") {
		t.Fatalf("%d %s %v\n%s", code, errOut, err, preview)
	}

	// Refusals: both forms at once, neither, and outside a BB candidate.
	for name, bad := range map[string][]string{
		"both":         {"--agent", "bb", "--new-thread", "--thread", bbOtherThread, "--provider", "codex"},
		"neither":      {"--agent", "bb", "--provider", "codex"},
		"no provider":  {"--agent", "bb", "--new-thread"},
		"headless":     {"--agent", "codex", "--new-thread"},
		"bad provider": {"--agent", "bb", "--new-thread", "--provider", "pi"},
		"bad mode":     {"--agent", "bb", "--new-thread", "--provider", "codex", "--permission-mode", "plan"},
		"mode, thread": {"--agent", "bb", "--thread", bbOtherThread, "--provider", "codex", "--permission-mode", "full"},
	} {
		f := newBBAddFixture(t)
		before := f.files(t)
		if code, _, _ := f.add(t, "", append([]string{"--non-interactive"}, bad...)...); code == exitOK || f.files(t) != before {
			t.Errorf("%s: accepted or wrote files (exit %d)", name, code)
		}
	}
}

// The wizard offers a new thread at the thread question. Earlier answers keep
// their meaning; the one added question is the new thread's permission mode.
func TestFallbacksAddBBWizardNewThread(t *testing.T) {
	f := newBBAddFixture(t)
	lists := &fakeBBLists{}
	f.bb = lists.lookup
	answers := strings.Join([]string{
		"bb",
		"no",    // do not ask BB for lists
		"new",   // a new thread at handoff
		"codex", // provider
		"",      // permission mode: accept-edits
		"",      // model: BB's default
		"",      // reasoning: unchanged
		"",      // quota group default
		"",      // probe model
		"",      // probe account directory
		"no",    // handoff
		"yes",   // write
	}, "\n") + "\n"
	code, out, errOut := f.add(t, answers)
	if code != exitOK {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
	if len(lists.calls) != 0 {
		t.Fatalf("bb was run: %v", lists.calls)
	}
	for _, want := range []string{"Or type new", "keepgoing creates a thread only for such a handoff, never now.", "Thread ID, or new", "none yet: a new thread, created at handoff", "Permission mode (accept-edits/auto/full)", "Permissions:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.FallbackSet.Candidates[0]; c.BB == nil || !c.BB.NewThread || c.BB.Thread != "" || c.Provider != "codex" || c.BB.NewThreadPermission() != "accept-edits" {
		t.Fatalf("%+v", c)
	}
}
