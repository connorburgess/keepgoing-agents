// Package probe confirms that a fallback candidate actually works right now:
// the CLI is installed, logged in, online, the model is available, and the
// account is not rate limited. A probe is one tiny real request with a hard
// timeout and no tools.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// Tier says why a probe ran.
type Tier string

// Tiers.
const (
	Scheduled Tier = "scheduled" // cheap, periodic, capped per day
	Exact     Tier = "exact"     // the real target model, right before a handoff
	Manual    Tier = "manual"    // `keepgoing fallbacks check`
)

// Prompt is what every model probe asks.
const Prompt = "Reply with exactly: OK"

// Result is the evidence a probe leaves in the health registry.
type Result struct {
	At                  time.Time  `json:"at"`
	Tier                Tier       `json:"tier"`
	Kind                string     `json:"kind"`
	Model               string     `json:"model,omitempty"`
	OK                  bool       `json:"ok"`
	LatencyMs           int64      `json:"latencyMs"`
	ExitCode            int        `json:"exitCode"`
	ErrorClass          string     `json:"errorClass,omitempty"`
	Detail              string     `json:"detail,omitempty"` // a short code, never model output
	CredentialExpiresAt *time.Time `json:"credentialExpiresAt,omitempty"`
	// ResetsAt is when a reported usage limit lifts, when the provider says.
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
	// Usage is a usage reading the probe happened to carry. It is stored once
	// per candidate by the health registry, not with each result, and no
	// decision reads it.
	Usage *Usage `json:"-"`
	// Raw is the probe's stdout, capped. It is never part of health.json and
	// never read for a decision; KeepFailure saves it so unrecognised error
	// shapes can be mapped later.
	Raw []byte `json:"-"`
}

// rawCap bounds the stdout a result carries.
const rawCap = 64 << 10

// Run probes one candidate. exact selects the target model over the cheap
// probe model.
func Run(ctx context.Context, c config.Candidate, tier Tier, timeout time.Duration) Result {
	model := c.ProbeModel(tier == Exact)
	res := Result{At: time.Now().UTC(), Tier: tier, Kind: c.Probe.Kind, Model: model}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var argv []string
	env := []string{"KEEPGOING_PROBE_TIER=" + string(tier), "KEEPGOING_PROBE_MODEL=" + model, "KEEPGOING_CANDIDATE=" + c.ID}
	for k, v := range c.ProbeEnv() {
		env = append(env, k+"="+v)
	}
	switch c.Probe.Kind {
	case "claude":
		// stream-json carries the same final result object as json, plus the
		// usage events ClaudeUsage reads. Print mode requires --verbose with it.
		argv = []string{c.ProbeCLI(), "-p", Prompt, "--output-format", "stream-json", "--verbose", "--no-session-persistence",
			"--restricted", "--strict-mcp-config", "--tools", "", "--max-turns", "1"}
		if model != "" {
			argv = append(argv, "--model", model)
		}
	case "codex":
		argv = []string{c.ProbeCLI(), "exec", "--json", "--ephemeral", "--skip-git-repo-check", "-s", "read-only"}
		if model != "" {
			argv = append(argv, "-m", model)
		}
		argv = append(argv, Prompt)
	case "command":
		argv = c.Probe.Command
	default:
		res.ErrorClass, res.Detail = string(decide.ErrUnknown), "unknown probe kind"
		return res
	}
	workdir := c.ProbeWorkdir()
	if workdir == "" {
		dir, err := os.MkdirTemp("", "keepgoing-probe-*")
		if err == nil {
			defer os.RemoveAll(dir)
			workdir = dir
		}
	}
	start := time.Now()
	stdout, code, err := run(ctx, argv, workdir, env)
	res.Raw = stdout
	if len(res.Raw) > rawCap {
		res.Raw = res.Raw[:rawCap]
	}
	res.LatencyMs = time.Since(start).Milliseconds()
	res.ExitCode = code
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ErrorClass, res.Detail = string(decide.ErrUnknown), "timeout"
		return res
	case errors.Is(err, exec.ErrNotFound) || code == -1:
		res.ErrorClass, res.Detail = string(decide.ErrUnknown), "not runnable"
		return res
	}
	switch c.Probe.Kind {
	case "claude":
		judgeClaude(&res, stdout, code)
		res.Usage = newUsage(res.At, UsageSourceClaude, ClaudeUsage(stdout))
	case "codex":
		judgeCodex(&res, stdout, code)
		// Codex reports a usage limit only as prose in its event stream, so
		// ask its app-server for the structured rate-limit snapshot.
		if !res.OK && res.ErrorClass == string(decide.ErrUnknown) {
			l, err := runner.ReadCodexLimits(ctx, c.ProbeCLI(), env)
			if err == nil {
				res.Usage = newUsage(res.At, UsageSourceCodex, CodexUsage(l))
			}
			if err == nil && l.Blocked {
				res.ErrorClass = string(decide.ErrRateLimit)
				res.Detail = "account/rateLimits/read: " + l.ReachedType
				if !l.ResetsAt.IsZero() {
					t := l.ResetsAt
					res.ResetsAt = &t
				}
			}
		}
	case "command":
		judgeCommand(&res, stdout, code, c.Probe.ExitCodes)
	}
	return res
}

func judgeClaude(res *Result, stdout []byte, code int) {
	r, ok := runner.ParseClaudeResult(stdout)
	var extra struct {
		CredentialExpiresAt time.Time `json:"credential_expires_at"`
	}
	if json.Unmarshal(lastLine(stdout), &extra) == nil && !extra.CredentialExpiresAt.IsZero() {
		t := extra.CredentialExpiresAt
		res.CredentialExpiresAt = &t
	}
	switch {
	case !ok:
		res.ErrorClass, res.Detail = string(decide.ErrUnknown), "no result object"
	case !r.IsError && code == 0:
		res.OK, res.Detail = true, "subtype="+r.Subtype
	default:
		res.ErrorClass, res.Detail = string(r.Class()), "subtype="+r.Subtype
	}
}

func judgeCodex(res *Result, stdout []byte, code int) {
	o := runner.ParseCodexEvents(stdout)
	switch {
	case o.Completed && !o.Failed && code == 0:
		res.OK, res.Detail = true, "turn.completed"
	case o.Failed:
		res.ErrorClass, res.Detail = string(o.Class), "turn.failed"
	default:
		res.ErrorClass, res.Detail = string(decide.ErrUnknown), fmt.Sprintf("exit %d without turn.completed", code)
	}
}

// judgeCommand: exit 0 is healthy unless stdout says otherwise with
// {"ok": false, "errorClass": "..."}; a mapped non-zero exit sets the class.
func judgeCommand(res *Result, stdout []byte, code int, codes map[int]string) {
	var s struct {
		OK         *bool  `json:"ok"`
		ErrorClass string `json:"errorClass"`
	}
	parsed := json.Unmarshal(bytes.TrimSpace(stdout), &s) == nil
	if code == 0 && (!parsed || s.OK == nil || *s.OK) {
		res.OK, res.Detail = true, "exit 0"
		return
	}
	res.Detail = fmt.Sprintf("exit %d", code)
	res.ErrorClass = string(decide.ErrUnknown)
	if class, ok := codes[code]; ok && code != 0 {
		res.ErrorClass = class
	} else if parsed && s.ErrorClass != "" {
		if config.ValidErrorClass(s.ErrorClass) {
			res.ErrorClass = s.ErrorClass
		} else {
			res.ErrorClass = string(adapter.ClassifyCode(s.ErrorClass))
		}
	}
}

func lastLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	return lines[len(lines)-1]
}

func run(ctx context.Context, argv []string, dir string, env []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader("")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.Bytes(), ee.ExitCode(), err
	}
	return stdout.Bytes(), -1, err
}
