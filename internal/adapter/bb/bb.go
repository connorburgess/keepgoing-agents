// Package bb supervises a BB thread through the bb CLI's JSON output.
package bb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// Adapter drives one BB thread.
type Adapter struct {
	cfg     config.BB
	Timeout time.Duration
	// run and now are swapped in tests.
	run func(ctx context.Context, stdin string, args ...string) ([]byte, error)
	now func() time.Time
}

// New returns an adapter for the thread.
func New(cfg config.BB) *Adapter {
	if cfg.CLI == "" {
		cfg.CLI = config.DefaultBBCLI
	}
	a := &Adapter{cfg: cfg, Timeout: 30 * time.Second, now: time.Now}
	a.run = a.exec
	return a
}

// Name implements adapter.Adapter.
func (a *Adapter) Name() string { return "bb" }

type envelope struct {
	OK    *bool `json:"ok"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

type showResponse struct {
	Thread *struct {
		Status         string `json:"status"`
		Queued         int    `json:"queuedMessageCount"`
		BackgroundJobs int    `json:"activeBackgroundAgentCount"`
		ArchivedAt     any    `json:"archivedAt"`
		DeletedAt      any    `json:"deletedAt"`
	} `json:"thread"`
}

// Observe reads `bb thread show --json`, then the newest log events for
// structured error and rate-limit information. Message text is never read.
func (a *Adapter) Observe(ctx context.Context) (decide.Observation, error) {
	unknown := decide.Observation{State: decide.Unknown}
	out, err := a.run(ctx, "", "thread", "show", a.cfg.Thread, "--json")
	if code, ok := envelopeError(out); ok {
		if code == "thread_not_found" {
			return decide.Observation{State: decide.Unknown, Gone: true}, nil
		}
		return unknown, fmt.Errorf("bb thread show: %s", code)
	}
	if err != nil {
		return unknown, fmt.Errorf("bb thread show: %w", err)
	}
	var resp showResponse
	if err := json.Unmarshal(out, &resp); err != nil || resp.Thread == nil {
		return unknown, errors.New("bb thread show: unexpected JSON")
	}
	t := resp.Thread
	obs := decide.Observation{
		State:      adapter.ParseState(t.Status),
		ErrorClass: decide.ErrNone,
		Queued:     t.Queued,
		Background: t.BackgroundJobs,
		Gone:       t.ArchivedAt != nil || t.DeletedAt != nil,
	}
	if obs.Gone {
		return obs, nil
	}
	// A pending permission prompt, question, or form means the thread is
	// waiting on a person, whatever its status says.
	if obs.State != decide.Stopping {
		obs.AwaitingInput = a.pendingInteraction(ctx)
	}
	if obs.State != decide.Error && obs.State != decide.Idle {
		return obs, nil
	}
	sig := a.signals(ctx)
	if obs.State == decide.Error {
		obs.ErrorClass = sig.class
		obs.Retrying = sig.willRetry
	}
	// A blocked subscription window on an idle thread means the next turn
	// would fail; treat it as a rate-limit error so it is handled the same way.
	if sig.blocked && sig.resetsAt.After(a.now()) {
		if obs.State == decide.Idle {
			obs.State = decide.Error
		}
		if obs.ErrorClass == decide.ErrUnknown || obs.ErrorClass == decide.ErrNone || obs.ErrorClass == decide.ErrNetwork {
			obs.ErrorClass = decide.ErrRateLimit
		}
	}
	if obs.ErrorClass == decide.ErrRateLimit && sig.resetsAt.After(a.now()) {
		obs.ResetsAt = sig.resetsAt
	}
	return obs, nil
}

type event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type signals struct {
	class     decide.ErrorClass
	willRetry bool
	blocked   bool
	resetsAt  time.Time
}

// signals scans the newest events. Errors count only after the latest turn
// started, so a stale error from an earlier turn is ignored.
func (a *Adapter) signals(ctx context.Context) signals {
	s := signals{class: decide.ErrUnknown}
	out, err := a.run(ctx, "", "thread", "log", a.cfg.Thread, "--json", "--limit", "60")
	if err != nil {
		return s
	}
	var events []event
	if json.Unmarshal(out, &events) != nil {
		return s
	}
	start := 0
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "turn/started" || events[i].Type == "client/turn/requested" {
			start = i
			break
		}
	}
	gotError := false
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		switch {
		case e.Type == "provider/rateLimits/updated" && !s.blocked && s.resetsAt.IsZero():
			s.blocked, s.resetsAt = rateLimits(e.Data)
		case i >= start && !gotError && e.Type == "provider/error":
			var d struct {
				WillRetry *bool `json:"willRetry"`
				Info      struct {
					Category     string `json:"category"`
					HTTPStatus   *int   `json:"httpStatusCode"`
					ProviderCode string `json:"providerCode"`
				} `json:"errorInfo"`
			}
			if json.Unmarshal(e.Data, &d) == nil {
				gotError = true
				s.willRetry = d.WillRetry != nil && *d.WillRetry
				s.class = firstKnown(d.Info.Category, httpCode(d.Info.HTTPStatus), d.Info.ProviderCode)
			}
		case i >= start && !gotError && (e.Type == "system/error" || strings.HasSuffix(e.Type, "/failed")):
			var d struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(e.Data, &d) == nil {
				gotError = true
				s.class = adapter.ClassifyCode(d.Code)
			}
		}
	}
	return s
}

func rateLimits(raw json.RawMessage) (bool, time.Time) {
	var d struct {
		RateLimits struct {
			Status  string `json:"status"`
			Windows []struct {
				Status     string `json:"status"`
				ResetsAtMs int64  `json:"resetsAtMs"`
			} `json:"windows"`
		} `json:"rateLimits"`
	}
	if json.Unmarshal(raw, &d) != nil || d.RateLimits.Status != "blocked" {
		return false, time.Time{}
	}
	var latest time.Time
	for _, w := range d.RateLimits.Windows {
		if w.Status == "blocked" && w.ResetsAtMs > 0 {
			if t := time.UnixMilli(w.ResetsAtMs); t.After(latest) {
				latest = t
			}
		}
	}
	return true, latest
}

func httpCode(p *int) string {
	if p == nil {
		return ""
	}
	return fmt.Sprint(*p)
}

// firstKnown classifies the first code that maps to a known class.
func firstKnown(codes ...string) decide.ErrorClass {
	for _, c := range codes {
		if cl := adapter.ClassifyCode(c); cl != decide.ErrUnknown {
			return cl
		}
	}
	return decide.ErrUnknown
}

// pendingInteraction reports whether the thread has an unresolved
// interaction. It reads `bb thread interactions list --json`; if that fails
// it falls back to the newest system/interaction/lifecycle events. Only the
// structured status field is read, never the interaction's text.
func (a *Adapter) pendingInteraction(ctx context.Context) bool {
	if out, err := a.run(ctx, "", "thread", "interactions", "list", a.cfg.Thread, "--json"); err == nil {
		var items []struct {
			Status *string `json:"status"`
		}
		if json.Unmarshal(out, &items) == nil {
			for _, it := range items {
				if it.Status == nil || *it.Status == "pending" {
					return true
				}
			}
			return false
		}
	}
	out, err := a.run(ctx, "", "thread", "log", a.cfg.Thread, "--json", "--limit", "60")
	if err != nil {
		return false
	}
	var events []struct {
		Type string `json:"type"`
		Data struct {
			Interaction struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"interaction"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &events) != nil {
		return false
	}
	latest := map[string]string{}
	for _, e := range events { // oldest first, so the last status per id wins
		if e.Type == "system/interaction/lifecycle" && e.Data.Interaction.ID != "" {
			latest[e.Data.Interaction.ID] = e.Data.Interaction.Status
		}
	}
	for _, status := range latest {
		if status == "pending" {
			return true
		}
	}
	return false
}

// Send implements adapter.Adapter via `bb thread tell`.
func (a *Adapter) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	return a.call(ctx, prompt, "thread", "tell", a.cfg.Thread, "--message-file", "-", "--mode", string(mode), "--json")
}

// Stop implements adapter.Adapter via `bb thread stop`.
func (a *Adapter) Stop(ctx context.Context) error {
	return a.call(ctx, "", "thread", "stop", a.cfg.Thread, "--json")
}

// Activate applies the configured model and reasoning level before the
// first message after a handoff.
func (a *Adapter) Activate(ctx context.Context) error {
	if a.cfg.Model == "" && a.cfg.Reasoning == "" {
		return nil
	}
	args := []string{"thread", "update", a.cfg.Thread, "--json"}
	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	}
	if a.cfg.Reasoning != "" {
		args = append(args, "--reasoning-level", a.cfg.Reasoning)
	}
	return a.call(ctx, "", args...)
}

func (a *Adapter) call(ctx context.Context, stdin string, args ...string) error {
	out, err := a.run(ctx, stdin, args...)
	what := "bb " + strings.Join(args[:2], " ")
	if code, ok := envelopeError(out); ok {
		return fmt.Errorf("%s: %s", what, code)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func envelopeError(out []byte) (string, bool) {
	var env envelope
	if json.Unmarshal(out, &env) != nil || env.OK == nil || *env.OK || env.Error == nil {
		return "", false
	}
	return env.Error.Code, true
}

func (a *Adapter) exec(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.cfg.CLI, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// Only the JSON envelope on stdout is trusted; stderr may hold prose.
	err := cmd.Run()
	return stdout.Bytes(), err
}
