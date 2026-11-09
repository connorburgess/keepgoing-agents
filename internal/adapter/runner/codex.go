package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// Codex runs `codex exec --json` headless, one turn per Send, resuming the
// same session. Status comes from the event stream's event types and any
// structured error code, plus the exit code.
type Codex struct {
	cfg config.Codex
	dir Dir
}

// NewCodex returns a headless Codex adapter storing turn files in dir.
func NewCodex(cfg config.Codex, dir string) *Codex { return &Codex{cfg: cfg, dir: Dir(dir)} }

// Name implements adapter.Adapter.
func (c *Codex) Name() string { return "codex" }

// CodexOutcome summarises a `codex exec --json` event stream.
type CodexOutcome struct {
	ThreadID  string
	Completed bool
	Failed    bool
	Class     decide.ErrorClass
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
			Code string `json:"code"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "thread.started":
			out.ThreadID = e.ThreadID
		case "turn.completed":
			out.Completed, out.Failed = true, false
		case "turn.failed", "error":
			out.Failed = true
			codes := []string{e.Code}
			if e.Error != nil {
				codes = append(codes, e.Error.Code, e.Error.Type)
				if e.Error.Status != nil {
					codes = append(codes, fmt.Sprint(*e.Error.Status))
				}
			}
			for _, code := range codes {
				if cl := adapter.ClassifyCode(code); cl != decide.ErrUnknown {
					out.Class = cl
					break
				}
			}
		}
	}
	return out
}

// Observe implements adapter.Adapter.
func (c *Codex) Observe(ctx context.Context) (decide.Observation, error) {
	st, err := c.dir.Status()
	if err != nil {
		return decide.Observation{State: decide.Unknown}, err
	}
	switch st.Phase {
	case NeverRun:
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone}, nil
	case Running:
		return decide.Observation{State: decide.Active, ErrorClass: decide.ErrNone}, nil
	case Crashed:
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, nil
	}
	o := ParseCodexEvents(st.Stdout)
	c.dir.SaveSession(o.ThreadID)
	if o.Completed && !o.Failed && st.ExitCode == 0 {
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone}, nil
	}
	obs := decide.Observation{State: decide.Error, ErrorClass: o.Class}
	// Codex's event stream reports a usage limit only as prose. Ask the
	// app-server for the structured snapshot instead.
	if obs.ErrorClass == decide.ErrUnknown {
		if l, err := ReadCodexLimits(ctx, c.cfg.CLI, envList(c.cfg.Env)); err == nil && l.Blocked {
			obs.ErrorClass, obs.ResetsAt = decide.ErrRateLimit, l.ResetsAt
		}
	}
	return obs, nil
}

// Args builds the codex argv for one turn. The prompt is read from stdin.
func (c *Codex) Args() []string {
	args := []string{c.cfg.CLI, "exec"}
	session := c.dir.Session()
	if session != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json")
	if c.cfg.Model != "" {
		args = append(args, "-m", c.cfg.Model)
	}
	if c.cfg.SkipGitRepoCheck {
		args = append(args, "--skip-git-repo-check")
	}
	if c.cfg.Sandbox != "" {
		if session != "" {
			// resume has no --sandbox flag; set it as a config override.
			args = append(args, "-c", fmt.Sprintf("sandbox_mode=%q", c.cfg.Sandbox))
		} else {
			args = append(args, "-s", c.cfg.Sandbox)
		}
	}
	args = append(args, c.cfg.ExtraArgs...)
	if session != "" {
		args = append(args, session)
	}
	return append(args, "-")
}

// Send starts a turn. A running turn cannot be steered or queued into.
func (c *Codex) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	st, err := c.dir.Status()
	if err != nil {
		return err
	}
	if st.Phase == Running {
		return adapter.ErrUnsupported{Op: fmt.Sprintf("sending to a running headless turn (%s mode)", mode)}
	}
	if st.Phase == Finished {
		c.dir.SaveSession(ParseCodexEvents(st.Stdout).ThreadID)
	}
	return c.dir.Start(c.Args(), c.cfg.Workdir, prompt, envList(c.cfg.Env))
}

// Stop implements adapter.Adapter.
func (c *Codex) Stop(ctx context.Context) error { return c.dir.Stop() }
