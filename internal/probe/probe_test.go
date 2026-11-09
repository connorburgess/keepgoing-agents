//go:build unix

package probe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
)

func fake(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake")
	os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	return p
}

func cand(kind, cli string) config.Candidate {
	c := config.Candidate{ID: "example", Probe: config.Probe{Kind: kind, CLI: cli, Model: "cheap-model"}}
	c.Agent = config.Agent{Adapter: "bb", Provider: "claude-code", BB: &config.BB{Thread: "thr_example", Model: "target-model"}}
	return c
}

func TestClaudeProbe(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	ok := fake(t, `echo "$@" > `+argsFile+`; echo '{"type":"result","subtype":"success","is_error":false,"session_id":"s","credential_expires_at":"2026-12-04T18:46:30.684Z","result":"OK"}'`)
	r := Run(context.Background(), cand("claude", ok), Scheduled, 10*time.Second)
	if !r.OK || r.Model != "cheap-model" || r.CredentialExpiresAt == nil {
		t.Errorf("%+v", r)
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--no-session-persistence", "--restricted", "--max-turns 1", "--model cheap-model"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
	r = Run(context.Background(), cand("claude", ok), Exact, 10*time.Second)
	if r.Model != "target-model" {
		t.Errorf("exact probe model = %q", r.Model)
	}
	limited := fake(t, `echo '{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":429,"result":"limit"}'; exit 1`)
	r = Run(context.Background(), cand("claude", limited), Scheduled, 10*time.Second)
	if r.OK || r.ErrorClass != "rate_limit" {
		t.Errorf("limited: %+v", r)
	}
}

func TestCodexProbe(t *testing.T) {
	ok := fake(t, `printf '%s\n' '{"type":"thread.started","thread_id":"t"}' '{"type":"turn.completed","usage":{}}'`)
	c := cand("codex", ok)
	if r := Run(context.Background(), c, Scheduled, 10*time.Second); !r.OK {
		t.Errorf("%+v", r)
	}
	// The shape of a real probe: an error item from a hook warning before the turn.
	warned := fake(t, `printf '%s\n' '{"type":"thread.started","thread_id":"t"}' '{"type":"item.completed","item":{"id":"item_0","type":"error","message":"example hook warning"}}' '{"type":"turn.started"}' '{"type":"turn.completed","usage":{}}'`)
	if r := Run(context.Background(), cand("codex", warned), Scheduled, 10*time.Second); !r.OK {
		t.Errorf("hook warning item failed the probe: %+v", r)
	}
	bad := fake(t, `printf '%s\n' '{"type":"turn.failed","error":{"message":"usage limit"}}'; exit 1`)
	if r := Run(context.Background(), cand("codex", bad), Scheduled, 10*time.Second); r.OK || r.ErrorClass != "unknown" {
		t.Errorf("%+v", r)
	}
}

func TestCodexProbeLimitFromAppServer(t *testing.T) {
	prose := fake(t, `printf '%s\n' '{"type":"turn.started"}' '{"type":"turn.failed","error":{"message":"Example usage limit message"}}'; exit 1`)
	reset := time.Unix(1794393286, 0).UTC()
	defer func(orig func(context.Context, string) (runner.CodexLimits, error)) { runner.ReadCodexLimits = orig }(runner.ReadCodexLimits)
	runner.ReadCodexLimits = func(context.Context, string) (runner.CodexLimits, error) {
		return runner.CodexLimits{Blocked: true, ReachedType: "rate_limit_reached", ResetsAt: reset}, nil
	}
	r := Run(context.Background(), cand("codex", prose), Scheduled, 10*time.Second)
	if r.OK || r.ErrorClass != "rate_limit" || r.ResetsAt == nil || !r.ResetsAt.Equal(reset) || r.Detail != "account/rateLimits/read: rate_limit_reached" {
		t.Errorf("%+v", r)
	}
	runner.ReadCodexLimits = func(context.Context, string) (runner.CodexLimits, error) {
		return runner.CodexLimits{}, errors.New("no app-server")
	}
	if r := Run(context.Background(), cand("codex", prose), Scheduled, 10*time.Second); r.ErrorClass != "unknown" {
		t.Errorf("unreadable snapshot must stay unknown: %+v", r)
	}
}

func TestCommandProbe(t *testing.T) {
	c := cand("command", "")
	c.Probe.Command = []string{fake(t, `test "$KEEPGOING_PROBE_TIER" = exact && test "$KEEPGOING_PROBE_MODEL" = target-model`)}
	if r := Run(context.Background(), c, Exact, 10*time.Second); !r.OK {
		t.Errorf("env not passed: %+v", r)
	}
	c.Probe.Env = map[string]string{"CLAUDE_CONFIG_DIR": "/fictional/second-account"}
	c.Probe.Command = []string{fake(t, `test "$CLAUDE_CONFIG_DIR" = /fictional/second-account`)}
	if r := Run(context.Background(), c, Scheduled, 10*time.Second); !r.OK {
		t.Errorf("probe.env not passed: %+v", r)
	}
	c.Probe.Env = nil
	c.Probe.Command = []string{fake(t, `exit 75`)}
	c.Probe.ExitCodes = map[int]string{75: "rate_limit"}
	if r := Run(context.Background(), c, Scheduled, 10*time.Second); r.OK || r.ErrorClass != "rate_limit" {
		t.Errorf("%+v", r)
	}
	c.Probe.Command = []string{fake(t, `echo '{"ok":false,"errorClass":"auth"}'`)}
	if r := Run(context.Background(), c, Scheduled, 10*time.Second); r.OK || r.ErrorClass != "auth" {
		t.Errorf("%+v", r)
	}
}

func TestTimeoutAndMissingBinary(t *testing.T) {
	c := cand("command", "")
	c.Probe.Command = []string{fake(t, `sleep 5`)}
	if r := Run(context.Background(), c, Scheduled, 200*time.Millisecond); r.OK || r.Detail != "timeout" {
		t.Errorf("%+v", r)
	}
	c.Probe.Command = []string{"/nonexistent/keepgoing-probe"}
	if r := Run(context.Background(), c, Scheduled, time.Second); r.OK || r.Detail != "not runnable" {
		t.Errorf("%+v", r)
	}
}
