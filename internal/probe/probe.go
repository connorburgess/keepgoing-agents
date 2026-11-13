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
	// TimedOut: the probe was stopped at its timeout without an answer.
	TimedOut            bool       `json:"timedOut,omitempty"`
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
	// UsageIdentity fingerprints the account the probe ran against, set on
	// every result so a reading from another account can be discarded.
	UsageIdentity string `json:"-"`
	// Raw is the probe's stdout, capped. It is never part of health.json and
	// never read for a decision; KeepFailure saves it so unrecognised error
	// shapes can be mapped later.
	Raw []byte `json:"-"`
}

// rawCap bounds the stdout a result carries.
const rawCap = 64 << 10

// capture bounds the output kept for a failed probe. It keeps the end, where
// a stream's result or error event is, not the beginning. An agent whose
// output opens with a description of the local installation has that dropped
// first (its specification's Trim).
func capture(kind string, stdout []byte) []byte {
	if spec, ok := runner.SpecFor(kind); ok && spec.Probe.Trim != nil {
		stdout = spec.Probe.Trim(stdout)
	}
	if len(stdout) > rawCap {
		stdout = stdout[len(stdout)-rawCap:]
	}
	return stdout
}

// Run probes one candidate. exact selects the target model over the cheap
// probe model.
func Run(ctx context.Context, c config.Candidate, tier Tier, timeout time.Duration) Result {
	model := c.ProbeModel(tier == Exact)
	res := Result{At: time.Now().UTC(), Tier: tier, Kind: c.Probe.Kind, Model: model, UsageIdentity: UsageIdentity(c)}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var argv []string
	env := []string{"KEEPGOING_PROBE_TIER=" + string(tier), "KEEPGOING_PROBE_MODEL=" + model, "KEEPGOING_CANDIDATE=" + c.ID}
	for k, v := range c.ProbeEnv() {
		env = append(env, k+"="+v)
	}
	// An agent on the runner contract is probed as its specification says.
	spec, known := runner.SpecFor(c.Probe.Kind)
	switch {
	case known:
		argv = spec.Probe.Argv(c.ProbeCLI(), model, Prompt)
	case c.Probe.Kind == "command":
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
	res.Raw = capture(c.Probe.Kind, stdout)
	res.LatencyMs = time.Since(start).Milliseconds()
	res.ExitCode = code
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ErrorClass, res.Detail, res.TimedOut = string(decide.ErrUnknown), "timeout", true
		return res
	case errors.Is(err, exec.ErrNotFound) || code == -1:
		res.ErrorClass, res.Detail = string(decide.ErrUnknown), "not runnable"
		return res
	}
	if !known {
		judgeCommand(&res, stdout, code, c.Probe.ExitCodes)
		return res
	}
	v := spec.Probe.Judge(stdout, code)
	res.OK, res.Detail, res.CredentialExpiresAt = v.OK, v.Detail, v.CredentialExpiresAt
	if !v.OK {
		res.ErrorClass = string(v.Class)
	}
	usage := usageReaders[spec.Name]
	if usage.fromOutput != nil {
		res.Usage = newUsage(res.At, usage.source, res.UsageIdentity, usage.fromOutput(stdout))
	}
	// An agent that reports a usage limit only as prose is asked for its
	// structured snapshot instead.
	if l := spec.Limits; l != nil && !res.OK && res.ErrorClass == string(decide.ErrUnknown) {
		limits, err := l.Read(ctx, c.ProbeCLI(), env)
		if err == nil && usage.fromLimits != nil {
			res.Usage = newUsage(res.At, usage.source, res.UsageIdentity, usage.fromLimits(limits))
		}
		if err == nil && limits.Blocked {
			res.ErrorClass = string(decide.ErrRateLimit)
			res.Detail = l.ProbeDetail + limits.ReachedType
			if !limits.ResetsAt.IsZero() {
				t := limits.ResetsAt
				res.ResetsAt = &t
			}
		}
	}
	return res
}

// usageReaders says where each agent's usage reading comes from. The reading
// types live in this package, so this table does too; it is keyed by the
// specification's name.
var usageReaders = map[string]struct {
	source     string
	fromOutput func(stdout []byte) []UsageWindow
	fromLimits func(runner.CodexLimits) []UsageWindow
}{
	"claude": {source: UsageSourceClaude, fromOutput: ClaudeUsage},
	"codex":  {source: UsageSourceCodex, fromLimits: CodexUsage},
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

func run(ctx context.Context, argv []string, dir string, env []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader("")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// A killed probe whose children still hold its output must not hold the
	// check open past the timeout.
	cmd.WaitDelay = 2 * time.Second
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
