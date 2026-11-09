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

// Follows the event shapes of a real `codex exec --json` probe, including an
// error item emitted before the turn by a local hook warning. That item must
// not fail the turn. Ids and text are fictional.
const codexWithHookWarning = `{"type":"thread.started","thread_id":"00000000-0000-7000-8000-0000000000c2"}
{"type":"item.completed","item":{"id":"item_0","type":"error","message":"example hook warning"}}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"OK"}}
{"type":"turn.completed","usage":{"input_tokens":21000,"cached_input_tokens":13000,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0}}`

func TestParseCodexEventsIgnoresErrorItems(t *testing.T) {
	o := ParseCodexEvents([]byte(codexWithHookWarning))
	if !o.Completed || o.Failed || o.ThreadID == "" {
		t.Errorf("%+v", o)
	}
}

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

// fakeAppServer answers the initialize handshake and account/rateLimits/read
// the way codex app-server does over stdio JSON-RPC.
const fakeAppServer = `while IFS= read -r line; do
  case "$line" in
    *'"id":1'*) echo '{"id":1,"result":{"userAgent":"fake"}}' ;;
    *'"id":2'*) echo '{"id":2,"result":{"ordinaryUsageAllowed":false,"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1794393286}}}}' ;;
  esac
done`

func TestReadCodexLimitsSpeaksTheAppServerProtocol(t *testing.T) {
	l, err := readCodexLimits(context.Background(), fakeCLI(t, fakeAppServer))
	if err != nil || !l.Blocked || l.ReachedType != "rate_limit_reached" || l.ResetsAt.Unix() != 1794393286 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := readCodexLimits(context.Background(), fakeCLI(t, "exit 1")); err == nil {
		t.Error("a CLI without app-server must be an error, not a limit")
	}
}

// The real Codex usage-limit stream carries the limit only as prose.
const codexLimitProse = `{"type":"thread.started","thread_id":"00000000-0000-7000-8000-0000000000c3"}
{"type":"turn.started"}
{"type":"error","message":"Example usage limit message, try again later."}
{"type":"turn.failed","error":{"message":"Example usage limit message, try again later."}}`

func TestCodexTurnLimitClassifiedFromAppServerNotProse(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "events")
	os.WriteFile(out, []byte(codexLimitProse), 0o600)
	c := NewCodex(config.Codex{CLI: fakeCLI(t, "cat > /dev/null; cat "+out+"; exit 1"), Workdir: dir}, filepath.Join(dir, "target"))
	if err := c.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, c.dir, Finished)
	reset := time.Unix(1794393286, 0).UTC()
	defer func(orig func(context.Context, string) (CodexLimits, error)) { ReadCodexLimits = orig }(ReadCodexLimits)
	ReadCodexLimits = func(context.Context, string) (CodexLimits, error) {
		return CodexLimits{Blocked: true, ReachedType: "rate_limit_reached", ResetsAt: reset}, nil
	}
	if obs, _ := c.Observe(context.Background()); obs.ErrorClass != decide.ErrRateLimit || !obs.ResetsAt.Equal(reset) {
		t.Errorf("blocked snapshot: %+v", obs)
	}
	ReadCodexLimits = func(context.Context, string) (CodexLimits, error) { return CodexLimits{}, nil }
	if obs, _ := c.Observe(context.Background()); obs.ErrorClass != decide.ErrUnknown {
		t.Errorf("allowed snapshot must leave the prose failure unknown: %+v", obs)
	}
}
