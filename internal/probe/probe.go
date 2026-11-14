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
	"path/filepath"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/envvar"
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
	// per candidate by the health registry, not with each result. The timing
	// preview reads it, but no supervision action is decided from it.
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
	// Each is set under both of its names, so a probe command written for
	// either reads it.
	env := append(append(envvar.ProbeTier.Pairs(string(tier)), envvar.ProbeModel.Pairs(model)...), envvar.Candidate.Pairs(c.ID)...)
	for k, v := range c.ProbeEnv() {
		env = append(env, k+"="+v)
	}
	// An agent on the runner contract, built in or defined in configuration,
	// is probed as its specification says.
	spec, known := probeSpec(c)
	cli, stdin := probeCLI(c, spec), ""
	switch {
	case known:
		req := runner.ProbeRequest{CLI: cli, Model: model, Prompt: Prompt}
		if spec.Prompt == runner.PromptFile {
			// The prompt goes in a private file, never in an argument.
			dir, err := os.MkdirTemp("", "keepgoing-probe-prompt-*")
			if err != nil {
				res.ErrorClass, res.Detail = string(decide.ErrUnknown), "not runnable"
				return res
			}
			defer os.RemoveAll(dir)
			req.PromptFile = filepath.Join(dir, "prompt")
			if err := os.WriteFile(req.PromptFile, []byte(Prompt), 0o600); err != nil {
				res.ErrorClass, res.Detail = string(decide.ErrUnknown), "not runnable"
				return res
			}
		}
		argv, stdin = spec.Probe.Command(req)
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
	// A specification that bounds its output has the probe's bounded as it
	// is read: one byte over the limit is kept to show it was exceeded.
	var limit int64
	if known {
		limit = spec.MaxOutput
	}
	stdout, code, err := run(ctx, argv, workdir, env, stdin, limit)
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
	if spec.Probe.Usage != nil {
		res.Usage = newUsage(res.At, spec.Probe.UsageSource, res.UsageIdentity, usageWindows(spec.Probe.Usage(stdout)))
	}
	// Read display-only usage after a successful probe too. Unknown failures
	// still use the structured snapshot for limit classification. The lookup
	// shares the probe deadline (and Codex bounds it to 20 seconds); a failed
	// lookup never changes the probe verdict or fabricates a fresh reading.
	if l := spec.Limits; l != nil && ((!res.OK && res.ErrorClass == string(decide.ErrUnknown)) || (res.OK && l.UsageSource != "")) {
		limits, err := l.Read(ctx, cli, env)
		if err == nil && l.UsageSource != "" {
			res.Usage = newUsage(res.At, l.UsageSource, res.UsageIdentity, CodexUsage(limits))
		}
		if err == nil && !res.OK && limits.Blocked {
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

// probeSpec returns the specification a candidate's probe kind names: a
// registered agent, or the candidate's own definition.
func probeSpec(c config.Candidate) (*runner.Spec, bool) {
	if c.Probe.Kind == config.CustomAdapter {
		spec, _, ok := runner.Resolve(c.Agent)
		return spec, ok && spec.Probe.Command != nil
	}
	return runner.SpecFor(c.Probe.Kind)
}

// probeCLI is the executable a probe runs: the one configured for the probe
// or the candidate, else the specification's default.
func probeCLI(c config.Candidate, spec *runner.Spec) string {
	if cli := c.ProbeCLI(); cli != "" || spec == nil {
		return cli
	}
	return spec.DefaultCLI
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

// limitedBuffer keeps at most limit+1 bytes when limit is positive and
// discards the rest, so a probe cannot make the supervisor hold its output.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int64
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return b.buf.Write(p)
	}
	if room := b.limit + 1 - int64(b.buf.Len()); room > 0 {
		b.buf.Write(p[:min(int64(len(p)), room)])
	}
	return len(p), nil
}

func run(ctx context.Context, argv []string, dir string, env []string, stdin string, limit int64) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	out := &limitedBuffer{limit: limit}
	stdout := &out.buf
	cmd.Stdout = out
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
