//go:build unix

package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-cli")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitPhase(t *testing.T, d Dir, want Phase) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, err := d.Status()
		if err != nil {
			t.Fatal(err)
		}
		if st.Phase == want {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("phase never reached %s", want)
	return Status{}
}

// Synthetic result objects with the field names Claude Code prints.
const claudeOK = `{"type":"result","subtype":"success","is_error":false,"session_id":"00000000-0000-4000-8000-000000000001","api_error_status":null,"terminal_reason":"completed","result":"done"}`
const claudeLimited = `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"00000000-0000-4000-8000-000000000001","api_error_status":429,"result":"You have hit your usage limit"}`
const claudeProseOnly = `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s","api_error_status":null,"result":"usage limit reached 429"}`

func TestClaudeTurnLifecycle(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	cli := fakeCLI(t, `echo "$@" > `+argsFile+`; cat > `+filepath.Join(dir, "got-prompt")+`; sleep 0.3; echo '`+claudeOK+`'`)
	c := NewClaude(config.Claude{CLI: cli, Workdir: dir, Model: "sonnet"}, filepath.Join(dir, "target"))
	ctx := context.Background()

	if obs, _ := c.Observe(ctx); obs.State != decide.Idle {
		t.Fatalf("never run should be idle: %+v", obs)
	}
	if err := c.Send(ctx, "first turn", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	if obs, _ := c.Observe(ctx); obs.State != decide.Active {
		t.Fatalf("expected active: %+v", obs)
	}
	if _, ok := c.Send(ctx, "x", adapter.ModeQueue).(adapter.ErrUnsupported); !ok {
		t.Error("send while running should be unsupported")
	}
	waitPhase(t, c.dir, Finished)
	if obs, _ := c.Observe(ctx); obs.State != decide.Idle {
		t.Fatalf("expected idle after success: %+v", obs)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "got-prompt")); string(got) != "first turn" {
		t.Errorf("prompt = %q", got)
	}
	// Second turn resumes the saved session.
	if err := c.Send(ctx, "second turn", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, c.dir, Finished)
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--resume 00000000-0000-4000-8000-000000000001") || !strings.Contains(string(args), "--model sonnet") {
		t.Errorf("args = %s", args)
	}
}

func TestClaudeErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		out  string
		exit string
		want decide.ErrorClass
	}{
		{claudeLimited, "1", decide.ErrRateLimit},
		{claudeProseOnly, "1", decide.ErrUnknown},
		{"not json at all: usage limit", "1", decide.ErrUnknown},
	} {
		dir := t.TempDir()
		c := NewClaude(config.Claude{CLI: fakeCLI(t, "echo '"+tc.out+"'; exit "+tc.exit), Workdir: dir}, filepath.Join(dir, "target"))
		if err := c.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
			t.Fatal(err)
		}
		waitPhase(t, c.dir, Finished)
		obs, _ := c.Observe(context.Background())
		if obs.State != decide.Error || obs.ErrorClass != tc.want {
			t.Errorf("%s: %+v want %s", tc.out[:20], obs, tc.want)
		}
	}
}

func TestStopKillsTurnAndCrashIsUnknown(t *testing.T) {
	dir := t.TempDir()
	c := NewClaude(config.Claude{CLI: fakeCLI(t, "sleep 30"), Workdir: dir}, filepath.Join(dir, "target"))
	if err := c.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, c.dir, Running)
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The wrapper shell is killed too, so no exit code is written: crashed.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := c.dir.Status()
		if st.Phase != Running {
			if obs, _ := c.Observe(context.Background()); obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown {
				t.Errorf("after stop: %+v", obs)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn still running after stop")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Synthetic codex exec --json events.
const codexOK = `{"type":"thread.started","thread_id":"00000000-0000-4000-8000-0000000000c1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"done"}}
{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`

const codexFailedProse = `{"type":"thread.started","thread_id":"t"}
{"type":"turn.started"}
{"type":"turn.failed","error":{"message":"You've hit your usage limit (429)"}}`

const codexFailedCoded = `{"type":"thread.started","thread_id":"t"}
{"type":"error","message":"x","code":"rate_limit_exceeded"}
{"type":"turn.failed","error":{"message":"x","code":"usage_limit_reached"}}`

func TestParseCodexEvents(t *testing.T) {
	o := ParseCodexEvents([]byte(codexOK))
	if !o.Completed || o.Failed || o.ThreadID != "00000000-0000-4000-8000-0000000000c1" {
		t.Errorf("ok: %+v", o)
	}
	if o := ParseCodexEvents([]byte(codexFailedProse)); !o.Failed || o.Class != decide.ErrUnknown {
		t.Errorf("prose: %+v", o)
	}
	if o := ParseCodexEvents([]byte(codexFailedCoded)); !o.Failed || o.Class != decide.ErrRateLimit {
		t.Errorf("coded: %+v", o)
	}
}

func TestCodexResumeArgs(t *testing.T) {
	dir := t.TempDir()
	c := NewCodex(config.Codex{CLI: "codex", Workdir: dir, Model: "gpt-5.5-codex", Sandbox: "workspace-write"}, filepath.Join(dir, "target"))
	if got := strings.Join(c.Args(), " "); got != "codex exec --json -m gpt-5.5-codex -s workspace-write -" {
		t.Errorf("fresh args = %q", got)
	}
	c.dir.SaveSession("sess-1")
	if got := strings.Join(c.Args(), " "); got != `codex exec resume --json -m gpt-5.5-codex -c sandbox_mode="workspace-write" sess-1 -` {
		t.Errorf("resume args = %q", got)
	}
}

func TestCodexTurn(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "events")
	os.WriteFile(out, []byte(codexOK), 0o600)
	c := NewCodex(config.Codex{CLI: fakeCLI(t, "cat > /dev/null; cat "+out), Workdir: dir}, filepath.Join(dir, "target"))
	if err := c.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, c.dir, Finished)
	if obs, _ := c.Observe(context.Background()); obs.State != decide.Idle {
		t.Errorf("%+v", obs)
	}
	if c.dir.Session() != "00000000-0000-4000-8000-0000000000c1" {
		t.Errorf("session = %q", c.dir.Session())
	}
}
