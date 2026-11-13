package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

const resetSessionID = "sess_example_0001"

// sessionResetFixture is an enabled loop on an agent defined in
// configuration that continues a session, with a BB fallback. Its last turn
// finished and reported a session, which is saved. Nothing is ever run: the
// executables do not exist.
func sessionResetFixture(t *testing.T, edit func(string) string) (path, target string) {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "") // the loop starts with no owner, whoever runs the test
	dir := t.TempDir()
	text := `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: Continue the fictional loop.
  fallbacks: fallbacks.yaml
  adapter: custom
  custom:
    name: example-agent
    workdir: .
    definition:
      start: [absent-example-agent, run, --json]
      resume: [absent-example-agent, run, --json, --session, "{session}"]
      output: {format: json}
      session: {field: session}
      finished: {field: status, equals: [ok]}
      capabilities: {exact_resume: true, can_be_stopped: true}
`
	if edit != nil {
		text = edit(text)
	}
	writeCustom(t, filepath.Join(dir, "keepgoing.yaml"), text, 0o600)
	writeCustom(t, filepath.Join(dir, "fallbacks.yaml"), `version: 1
candidates:
  - id: bb-fallback
    adapter: bb
    provider: example-provider
    bb: {thread: thr_example123, cli: absent-bb}
    probe: {kind: command, command: [absent-probe]}
`, 0o600)
	path = filepath.Join(dir, "keepgoing.yaml")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"enable", "-c", path}, &out, &errOut); code != exitOK {
		t.Fatalf("enable: %d %s", code, errOut.String())
	}
	target = filepath.Join(dir, "state", "targets", "primary")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"run.json": `{"pid":1,"stopKey":"example-key","startedAt":"2026-01-01T00:00:00Z","argv0":"absent-example-agent"}`,
		"exit":     "0\n",
		"stdout":   `{"status":"ok","session":"` + resetSessionID + `"}`,
		"session":  resetSessionID + "\n",
	} {
		writeCustom(t, filepath.Join(target, name), content, 0o600)
	}
	return path, target
}

func runKeepgoing(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// session reset forgets the saved session on purpose, records who ran it,
// and clears no hold. The finished turn that reported the session cannot
// bring it back.
func TestSessionResetCommand(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "")
	path, target := sessionResetFixture(t, nil)
	if code, _, errs := runKeepgoing("await-user", "-c", path, "--note", "fictional question"); code != exitOK {
		t.Fatal(errs)
	}
	code, out, errs := runKeepgoing("session", "reset", "-c", path)
	if code != exitOK || !strings.Contains(out, "example-loop: session reset; the next turn on primary starts a new conversation") || !strings.Contains(out, "run keepgoing resume when you are ready") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(target, "session")); !os.IsNotExist(err) {
		t.Fatalf("the session is still saved: %v", err)
	}
	// The hold is untouched, and a status read (which observes nothing) or a
	// dry check does not restore the session from the last turn's output.
	_, status, _ := runKeepgoing("status", "-c", path, "--json")
	if !strings.Contains(status, `"awaitingUser": true`) {
		t.Fatalf("the hold was cleared: %s", status)
	}
	runKeepgoing("resume", "-c", path)
	runKeepgoing("tick", "-c", path, "--dry-run")
	if _, err := os.Stat(filepath.Join(target, "session")); !os.IsNotExist(err) {
		t.Fatalf("a check restored the reset session: %v", err)
	}
	// Recorded where it survives: the event log with who ran it, and the
	// timeline in words, without an identity.
	events, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "state", "runs.jsonl"))
	if !strings.Contains(string(events), `"event":"session_reset"`) || !strings.Contains(string(events), `"by":"terminal"`) || !strings.Contains(string(events), `"hadSession":true`) {
		t.Fatalf("event log: %s", events)
	}
	_, timeline, _ := runKeepgoing("timeline", "-c", path)
	if !strings.Contains(timeline, "session reset from a terminal or scheduler (no BB thread identity)") || strings.Contains(timeline, resetSessionID) {
		t.Fatalf("timeline: %s", timeline)
	}

	// From a BB thread, as JSON, with nothing saved any more.
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	code, out, errs = runKeepgoing("session", "reset", "-c", path, "--json", "--note", "fictional reason")
	var report sessionResetReport
	if code != exitOK || json.Unmarshal([]byte(out), &report) != nil || report.Type != "session_reset" || report.Target != "primary" || report.HadSession || report.By != "thread" || len(report.Holds) != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	events, _ = os.ReadFile(filepath.Join(filepath.Dir(path), "state", "runs.jsonl"))
	if !strings.Contains(string(events), `"thread":"thr_example_owner"`) || !strings.Contains(string(events), "fictional reason") {
		t.Fatalf("event log: %s", events)
	}
	if _, timeline, _ := runKeepgoing("timeline", "-c", path); !strings.Contains(timeline, "session reset by a BB thread") || strings.Contains(timeline, "thr_example_owner") {
		t.Fatalf("timeline: %s", timeline)
	}
}

// Every refusal leaves the saved session exactly as it was.
func TestSessionResetRefusals(t *testing.T) {
	saved := func(t *testing.T, target string) {
		t.Helper()
		if b, err := os.ReadFile(filepath.Join(target, "session")); err != nil || strings.TrimSpace(string(b)) != resetSessionID {
			t.Fatalf("the saved session changed: %q %v", b, err)
		}
		if _, err := os.Stat(filepath.Join(target, "session-reset")); !os.IsNotExist(err) {
			t.Fatalf("a reset was recorded: %v", err)
		}
	}
	t.Run("a turn is running", func(t *testing.T) {
		t.Setenv("BB_THREAD_ID", "")
		path, target := sessionResetFixture(t, nil)
		// No exit record and a live process: the turn is in progress.
		os.Remove(filepath.Join(target, "exit"))
		writeCustom(t, filepath.Join(target, "run.json"), fmt.Sprintf(`{"pid":%d,"stopKey":"example-key","startedAt":"2026-01-01T00:00:00Z","argv0":"absent-example-agent"}`, os.Getpid()), 0o600)
		code, _, errs := runKeepgoing("session", "reset", "-c", path)
		if code != exitFailed || !strings.Contains(errs, "a turn is running on primary") {
			t.Fatalf("%d %s", code, errs)
		}
		saved(t, target)
	})
	t.Run("a check is running", func(t *testing.T) {
		t.Setenv("BB_THREAD_ID", "")
		path, target := sessionResetFixture(t, nil)
		held, err := lock.Acquire(filepath.Join(filepath.Dir(path), "state"))
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release()
		if code, _, errs := runKeepgoing("session", "reset", "-c", path); code != exitSkipped || !strings.Contains(errs, "a check is running") {
			t.Fatalf("%d %s", code, errs)
		}
		saved(t, target)
	})
	t.Run("not the owner", func(t *testing.T) {
		path, target := sessionResetFixture(t, nil)
		t.Setenv("BB_THREAD_ID", "thr_example_owner")
		if code, _, errs := runKeepgoing("enable", "-c", path); code != exitOK {
			t.Fatal(errs)
		}
		t.Setenv("BB_THREAD_ID", "thr_example_other")
		if code, _, errs := runKeepgoing("session", "reset", "-c", path); code != exitFailed || !strings.Contains(errs, "ownership:") {
			t.Fatalf("%d %s", code, errs)
		}
		saved(t, target)
		// The owner may, and so may a terminal with no thread identity.
		t.Setenv("BB_THREAD_ID", "thr_example_owner")
		if code, _, errs := runKeepgoing("session", "reset", "-c", path); code != exitOK {
			t.Fatalf("the owner was refused: %d %s", code, errs)
		}
	})
	for name, tc := range map[string]struct {
		args []string
		edit func(string) string
		want string
	}{
		"a BB target":       {[]string{"--target", "bb-fallback"}, nil, "target bb-fallback is bb, whose conversation keepgoing does not own"},
		"an unknown target": {[]string{"--target", "nobody"}, nil, `no target "nobody"`},
		"an extra argument": {[]string{"primary"}, nil, "usage: keepgoing session reset"},
		"an agent with no resume": {nil, func(s string) string {
			s = strings.Replace(s, "      resume: [absent-example-agent, run, --json, --session, \"{session}\"]\n", "", 1)
			s = strings.Replace(s, "      session: {field: session}\n", "", 1)
			return strings.Replace(s, "exact_resume: true, ", "", 1)
		}, "every turn already starts a new conversation"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BB_THREAD_ID", "")
			path, target := sessionResetFixture(t, tc.edit)
			code, _, errs := runKeepgoing(append([]string{"session", "reset", "-c", path}, tc.args...)...)
			if code != exitUsage || !strings.Contains(errs, tc.want) {
				t.Fatalf("%d %s", code, errs)
			}
			saved(t, target)
		})
	}
	// The flag belongs to this command only, and the group needs a subcommand.
	path, _ := sessionResetFixture(t, nil)
	if code, _, errs := runKeepgoing("status", "-c", path, "--target", "primary"); code != exitUsage || !strings.Contains(errs, "--target is only supported by session reset") {
		t.Fatalf("%d %s", code, errs)
	}
	if code, _, errs := runKeepgoing("session"); code != exitUsage || !strings.Contains(errs, "session needs a subcommand") {
		t.Fatalf("%d %s", code, errs)
	}
	if code, _, errs := runKeepgoing("session", "forget", "-c", path); code != exitUsage || !strings.Contains(errs, `unknown command "session forget"`) {
		t.Fatalf("%d %s", code, errs)
	}
}
