package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/evidence"
)

// codexSpec is `codex exec --json` headless, one turn per process, resuming
// the same session. Status comes from the event stream's event types and any
// structured error code, plus the exit code.
var codexSpec = &Spec{
	Name:         "codex",
	Provider:     "codex",
	DefaultCLI:   "codex",
	AccountEnv:   "CODEX_HOME",
	SettingNames: []string{"cli", "workdir", "model", "sandbox", "skip_git_repo_check", "extra_args", "env"},
	Prompt:       PromptStdin,
	MaxOutput:    8 << 20, // Bound observation memory; the private output file is unchanged.
	Capabilities: Capabilities{ExactResume: true, Stoppable: true, StructuredErrors: true, TurnUsage: true},
	Argv:         func(set Settings, session, _ string) []string { return codexArgv(set, session) },
	Read: func(turn Status) Reading {
		o := ParseCodexEvents(turn.Stdout)
		if o.Evidence.Source != "" {
			o.Evidence.ExitCode = runnerEvidence(turn).ExitCode
		}
		return Reading{Session: o.ThreadID, Completed: o.Completed && !o.Failed && turn.ExitCode == 0, Class: o.Class, Evidence: o.Evidence, Usage: o.Usage}
	},
	// Codex's event stream reports a usage limit only as prose. Its
	// app-server has the structured snapshot.
	Limits: &LimitLookup{
		UsageSource: "codex_rate_limits_read",
		Read: func(ctx context.Context, cli string, env []string) (Limits, error) {
			return ReadCodexLimits(ctx, cli, env)
		},
		EvidenceSource: "codex_rate_limits", EvidenceCode: "subscription_window_blocked",
		ProbeDetail: "account/rateLimits/read: ",
	},
	Probe: ProbeSpec{
		Command: func(p ProbeRequest) ([]string, string) {
			argv := []string{p.CLI, "exec", "--json", "--ephemeral", "--skip-git-repo-check", "-s", "read-only"}
			if p.Model != "" {
				argv = append(argv, "-m", p.Model)
			}
			return append(argv, p.Prompt), ""
		},
		Judge: func(stdout []byte, code int) ProbeVerdict {
			o := ParseCodexEvents(stdout)
			switch {
			case o.Completed && !o.Failed && code == 0:
				return ProbeVerdict{OK: true, Detail: "turn.completed"}
			case o.Failed:
				return ProbeVerdict{Class: o.Class, Detail: "turn.failed"}
			}
			return ProbeVerdict{Class: decide.ErrUnknown, Detail: fmt.Sprintf("exit %d without turn.completed", code)}
		},
	},
}

// NewCodex returns a headless Codex adapter storing turn files in dir.
func NewCodex(cfg config.Codex, dir string) *Headless {
	h, _ := New(config.Agent{Adapter: codexSpec.Name, Codex: &cfg}, dir)
	return h
}

// CodexOutcome summarises a `codex exec --json` event stream.
type CodexOutcome struct {
	Evidence  evidence.Detail
	ThreadID  string
	Completed bool
	Failed    bool
	Class     decide.ErrorClass
	// Usage is the token counts of the last turn.completed event's usage
	// object, or nil when it had none or no count in it was usable.
	Usage *adapter.TurnUsage
}

// ParseCodexEvents reads the JSONL event stream.
func ParseCodexEvents(stdout []byte) CodexOutcome {
	out := CodexOutcome{Class: decide.ErrUnknown}
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Error    *struct {
				Code   string `json:"code"`
				Type   string `json:"type"`
				Status *int   `json:"status"`
			} `json:"error"`
			Code  string          `json:"code"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "thread.started":
			out.ThreadID = e.ThreadID
		case "turn.completed":
			out.Completed, out.Failed = true, false
			out.Usage = codexUsage(e.Usage)
			out.Evidence = evidence.Clean(evidence.Detail{Source: "codex_events"})
		case "turn.failed", "error":
			out.Failed = true
			codes := []string{e.Code}
			detail := evidence.Detail{Source: "codex_events", Code: e.Code}
			if e.Error != nil {
				detail.Category = e.Error.Type
				detail.HTTPStatus = e.Error.Status
				if evidence.Code(detail.Code) == "unavailable" {
					detail.Code = e.Error.Code
				}
				codes = append(codes, e.Error.Code, e.Error.Type)
				if e.Error.Status != nil {
					codes = append(codes, fmt.Sprint(*e.Error.Status))
				}
			}
			if out.Class == decide.ErrUnknown {
				out.Evidence = evidence.Clean(detail)
			}
			// A later event's class replaces an earlier one, as before, except
			// that nothing replaces a refusal or restriction: in any field of
			// any event of the turn, it outranks every other class, and a
			// restriction outranks a refusal.
			if cl := adapter.Classify(codes...); cl != decide.ErrUnknown && (!decide.Declined(out.Class) || cl != out.Class && adapter.Prefer(out.Class, cl) == cl) {
				out.Class = cl
				out.Evidence = evidence.Clean(detail)
			}
		}
	}
	return out
}

// codexUsage reads the typed usage object of turn.completed. A count is kept
// only when it is a plain JSON integer in a sane range; anything else is
// absent, and a usage object with no usable count is no reading at all.
// It is decoded apart from the event, so a malformed usage value can never
// make turn.completed itself unreadable.
func codexUsage(raw json.RawMessage) *adapter.TurnUsage {
	var u map[string]json.RawMessage
	if json.Unmarshal(raw, &u) != nil {
		return nil
	}
	var out adapter.TurnUsage
	for key, dst := range map[string]**int64{
		"input_tokens":             &out.Input,
		"cached_input_tokens":      &out.CachedInput,
		"cache_write_input_tokens": &out.CacheWriteInput,
		"output_tokens":            &out.Output,
		"reasoning_output_tokens":  &out.ReasoningOutput,
	} {
		*dst = tokenCount(u[key])
	}
	if out.Input == nil && out.CachedInput == nil && out.CacheWriteInput == nil && out.Output == nil && out.ReasoningOutput == nil {
		return nil
	}
	return &out
}

// MaxTurnTokens is the largest token count a turn reading keeps. A larger
// figure is not a sane count for one turn, and is treated as absent.
const MaxTurnTokens = 10_000_000_000

// tokenCount reads one count: digits only (no sign, fraction, exponent,
// string or null), at most MaxTurnTokens. Otherwise nil.
func tokenCount(raw json.RawMessage) *int64 {
	if len(raw) == 0 || len(raw) > 11 {
		return nil
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return nil
		}
	}
	if len(raw) > 1 && raw[0] == '0' {
		return nil
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n > MaxTurnTokens {
		return nil
	}
	return &n
}

// codexSettings reads the loop's codex settings.
func codexSettings(a config.Agent) (Settings, bool) {
	c := a.Codex
	if c == nil {
		return Settings{}, false
	}
	set := Settings{CLI: c.CLI, Workdir: c.Workdir, Model: c.Model, Env: c.Env, ExtraArgs: c.ExtraArgs, Options: map[string]string{}}
	if c.Sandbox != "" {
		set.Options["sandbox"] = c.Sandbox
	}
	if c.SkipGitRepoCheck {
		set.Options["skip_git_repo_check"] = "true"
	}
	return set, true
}

// codexArgv builds the codex argv for one turn. The prompt is read from stdin.
func codexArgv(set Settings, session string) []string {
	args := []string{set.CLI, "exec"}
	if session != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json")
	if set.Model != "" {
		args = append(args, "-m", set.Model)
	}
	if set.Options["skip_git_repo_check"] != "" {
		args = append(args, "--skip-git-repo-check")
	}
	if sandbox := set.Options["sandbox"]; sandbox != "" {
		if session != "" {
			// resume has no --sandbox flag; set it as a config override.
			args = append(args, "-c", fmt.Sprintf("sandbox_mode=%q", sandbox))
		} else {
			args = append(args, "-s", sandbox)
		}
	}
	args = append(args, set.ExtraArgs...)
	if session != "" {
		args = append(args, session)
	}
	return append(args, "-")
}
