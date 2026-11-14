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
	"github.com/connorburgess/keepgoing-agents/internal/testexec"
)

func fake(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake")
	testexec.Script(t, p, "#!/bin/sh\n"+body+"\n")
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
	defer func(orig func(context.Context, string, []string) (runner.CodexLimits, error)) {
		runner.ReadCodexLimits = orig
	}(runner.ReadCodexLimits)
	runner.ReadCodexLimits = func(context.Context, string, []string) (runner.CodexLimits, error) {
		return runner.CodexLimits{Blocked: true, ReachedType: "rate_limit_reached", ResetsAt: reset}, nil
	}
	r := Run(context.Background(), cand("codex", prose), Scheduled, 10*time.Second)
	if r.OK || r.ErrorClass != "rate_limit" || r.ResetsAt == nil || !r.ResetsAt.Equal(reset) || r.Detail != "account/rateLimits/read: rate_limit_reached" {
		t.Errorf("%+v", r)
	}
	runner.ReadCodexLimits = func(context.Context, string, []string) (runner.CodexLimits, error) {
		return runner.CodexLimits{}, errors.New("no app-server")
	}
	if r := Run(context.Background(), cand("codex", prose), Scheduled, 10*time.Second); r.ErrorClass != "unknown" {
		t.Errorf("unreadable snapshot must stay unknown: %+v", r)
	}
}

func TestCommandProbe(t *testing.T) {
	c := cand("command", "")
	c.Probe.Command = []string{fake(t, `test "$LOOPTENDER_PROBE_TIER" = exact && test "$LOOPTENDER_PROBE_MODEL" = target-model && test "$LOOPTENDER_CANDIDATE" = example`)}
	if r := Run(context.Background(), c, Exact, 10*time.Second); !r.OK {
		t.Errorf("env not passed: %+v", r)
	}
	// A probe command written before the rename reads the earlier names.
	c.Probe.Command = []string{fake(t, `test "$KEEPGOING_PROBE_TIER" = exact && test "$KEEPGOING_PROBE_MODEL" = target-model && test "$KEEPGOING_CANDIDATE" = example`)}
	if r := Run(context.Background(), c, Exact, 10*time.Second); !r.OK {
		t.Errorf("env not passed under the earlier names: %+v", r)
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

func TestProbesInheritTargetAccount(t *testing.T) {
	for _, kind := range []string{"claude", "codex", "command"} {
		for _, tier := range []Tier{Scheduled, Exact, Manual} {
			t.Run(kind+"/"+string(tier), func(t *testing.T) {
				key, output := "CLAUDE_CONFIG_DIR", `{"type":"result","is_error":false}`
				if kind == "codex" {
					key, output = "CODEX_HOME", `{"type":"turn.completed"}`
				}
				if kind == "command" {
					output = `{"ok":true}`
				}
				t.Setenv(key, "/fictional/default")
				cli := fake(t, `test "$`+key+`" = /fictional/account-b || exit 2
 test "$KEEPGOING_TEST_SETTING" = probe || exit 3
 echo '`+output+`'`)
				env := map[string]string{key: "/fictional/account-b", "KEEPGOING_TEST_SETTING": "target"}
				c := config.Candidate{ID: "account-b", Agent: config.Agent{Adapter: "claude", Claude: &config.Claude{CLI: cli, Env: env}}, Probe: config.Probe{Kind: kind, Env: map[string]string{"KEEPGOING_TEST_SETTING": "probe"}}}
				if kind == "codex" {
					c.Agent = config.Agent{Adapter: "codex", Codex: &config.Codex{CLI: cli, Env: env}}
				}
				if kind == "command" {
					c.Probe.Command = []string{cli}
				}
				if r := Run(context.Background(), c, tier, 10*time.Second); !r.OK {
					t.Fatalf("account not inherited: %+v", r)
				}
				if env["KEEPGOING_TEST_SETTING"] != "target" || os.Getenv(key) != "/fictional/default" {
					t.Fatal("probe mutated target or parent environment")
				}
			})
		}
	}
}

func TestCodexProbeLimitCheckUsesProbeAccount(t *testing.T) {
	t.Setenv("CODEX_HOME", "/fictional/default")
	cli := fake(t, `test "$CODEX_HOME" = /fictional/probe-account || exit 2
if [ "$1" = app-server ]; then
 while IFS= read -r line; do
 case "$line" in
 *'"id":1'*) echo '{"id":1,"result":{}}' ;;
 *'"id":2'*) echo '{"id":2,"result":{"ordinaryUsageAllowed":false}}' ;;
 esac
 done
else
 echo '{"type":"turn.failed"}'
 exit 1
fi`)
	c := config.Candidate{ID: "account-b", Agent: config.Agent{Adapter: "codex", Codex: &config.Codex{CLI: cli, Env: map[string]string{"CODEX_HOME": "/fictional/account-b"}}}, Probe: config.Probe{Kind: "codex", Env: map[string]string{"CODEX_HOME": "/fictional/probe-account"}}}
	if r := Run(context.Background(), c, Exact, 10*time.Second); r.ErrorClass != "rate_limit" {
		t.Fatalf("limit check did not use effective probe environment: %+v", r)
	}
}

// The stream-json probe must judge a turn exactly as the earlier json probe
// did: same health, class, detail and credential expiry for the same result
// object, whether or not usage events surround it.
func TestClaudeProbeJudgesStreamAndPlainOutputAlike(t *testing.T) {
	results := map[string]struct{ line, exit string }{
		"success":                          {`{"type":"result","subtype":"success","is_error":false,"session_id":"s","credential_expires_at":"2026-12-04T18:46:30.684Z","result":"OK"}`, "0"},
		"rate limit":                       {`{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":429,"result":"limit"}`, "1"},
		"auth":                             {`{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":401,"result":"login"}`, "1"},
		"unknown":                          {`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"x"}`, "1"},
		"error exit with a success result": {`{"type":"result","subtype":"success","is_error":false,"result":"OK"}`, "3"},
	}
	for name, r := range results {
		t.Run(name, func(t *testing.T) {
			plain := Run(context.Background(), cand("claude", fake(t, `echo '`+r.line+`'; exit `+r.exit)), Scheduled, 10*time.Second)
			stream := Run(context.Background(), cand("claude", fake(t, `printf '%s\n' '{"type":"system","subtype":"init","session_id":"s"}' '`+eventA+`' '{"type":"assistant","message":{"content":[{"type":"text","text":"OK"}]}}' '`+eventB+`' '`+r.line+`'; exit `+r.exit)), Scheduled, 10*time.Second)
			// The stream puts usage events on both sides of the result line.
			trailing := Run(context.Background(), cand("claude", fake(t, `printf '%s\n' '`+eventA+`' '`+r.line+`' '`+eventB+`'; exit `+r.exit)), Scheduled, 10*time.Second)
			expiry := func(r Result) string {
				if r.CredentialExpiresAt == nil {
					return "none"
				}
				return r.CredentialExpiresAt.UTC().Format(time.RFC3339Nano)
			}
			for _, got := range []Result{stream, trailing} {
				same := plain.OK == got.OK && plain.ErrorClass == got.ErrorClass && plain.Detail == got.Detail && plain.ExitCode == got.ExitCode && expiry(plain) == expiry(got)
				if !same {
					t.Fatalf("plain %+v (expiry %s)\nstream %+v (expiry %s)", plain, expiry(plain), got, expiry(got))
				}
			}
			if name == "success" && expiry(plain) != "2026-12-04T18:46:30.684Z" {
				t.Fatalf("expiry not read: %s", expiry(plain))
			}
			if !trailing.Usage.Available() || len(trailing.Usage.Windows) != 2 {
				t.Fatalf("trailing usage: %+v", trailing.Usage)
			}
			if plain.Usage != nil {
				t.Fatalf("reading without an event: %+v", plain.Usage)
			}
			// Every result carries the identity, reading or not.
			if plain.UsageIdentity == "" || !stream.Usage.For(stream.UsageIdentity) {
				t.Fatalf("identity: plain %q stream %q usage %+v", plain.UsageIdentity, stream.UsageIdentity, stream.Usage)
			}
			if !stream.Usage.Available() || stream.Usage.Source != UsageSourceClaude || !stream.Usage.At.Equal(stream.At) || len(stream.Usage.Windows) != 2 {
				t.Fatalf("usage: %+v", stream.Usage)
			}
		})
	}
	// No result object at all is still an unknown failure, with or without events.
	r := Run(context.Background(), cand("claude", fake(t, `echo '`+eventA+`'`)), Scheduled, 10*time.Second)
	if r.OK || r.ErrorClass != "unknown" || r.Detail != "no result object" {
		t.Fatalf("%+v", r)
	}
	argsFile := filepath.Join(t.TempDir(), "args")
	Run(context.Background(), cand("claude", fake(t, `echo "$@" > `+argsFile)), Scheduled, 10*time.Second)
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--output-format stream-json --verbose") {
		t.Fatalf("args: %s", args)
	}
}

// A Codex reading comes only from the app-server read the probe already makes
// after an unclassified failure. A healthy probe makes no such call.
func TestCodexProbeRecordsUsageOnlyFromTheExistingLimitRead(t *testing.T) {
	calls := 0
	defer func(orig func(context.Context, string, []string) (runner.CodexLimits, error)) {
		runner.ReadCodexLimits = orig
	}(runner.ReadCodexLimits)
	limits := runner.CodexLimits{Windows: []runner.CodexWindow{{Name: "primary", UsedPercent: 40}, {Name: "secondary", UsedPercent: 81}}}
	runner.ReadCodexLimits = func(context.Context, string, []string) (runner.CodexLimits, error) {
		calls++
		return limits, nil
	}
	healthy := fake(t, `printf '%s\n' '{"type":"thread.started","thread_id":"t"}' '{"type":"turn.completed","usage":{}}'`)
	if r := Run(context.Background(), cand("codex", healthy), Scheduled, 10*time.Second); !r.OK || r.Usage != nil || calls != 0 {
		t.Fatalf("healthy probe: %+v calls=%d", r, calls)
	}
	failed := fake(t, `echo '{"type":"thread.started","thread_id":"t"}'; exit 1`)
	r := Run(context.Background(), cand("codex", failed), Scheduled, 10*time.Second)
	if r.OK || r.ErrorClass != "unknown" || calls != 1 || !r.Usage.Available() || r.Usage.Source != UsageSourceCodex || r.Usage.Windows[1].Used != 0.81 {
		t.Fatalf("not blocked: %+v usage=%+v calls=%d", r, r.Usage, calls)
	}
	limits.Blocked, limits.ReachedType = true, "rate_limit_reached"
	if r := Run(context.Background(), cand("codex", failed), Scheduled, 10*time.Second); r.ErrorClass != "rate_limit" || !r.Usage.Available() {
		t.Fatalf("blocked: %+v", r)
	}
	runner.ReadCodexLimits = func(context.Context, string, []string) (runner.CodexLimits, error) {
		return runner.CodexLimits{}, errors.New("unavailable")
	}
	if r := Run(context.Background(), cand("codex", failed), Scheduled, 10*time.Second); r.ErrorClass != "unknown" || r.Usage != nil {
		t.Fatalf("read failed: %+v", r)
	}
}
