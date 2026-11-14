// Package bb supervises a BB thread through the bb CLI's JSON output.
package bb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// Adapter drives one BB thread.
type Adapter struct {
	cfg      config.BB
	stateDir string  // where bb-cursor.json lives; empty keeps it in memory
	mem      *Cursor // last cursor, for adapters without a state dir
	Timeout  time.Duration
	// run and now are swapped in tests.
	run func(ctx context.Context, stdin string, args ...string) ([]byte, error)
	now func() time.Time
	// activeChildIDs is the verified active children of the last childWork
	// call, kept beside its count for the run graph.
	activeChildIDs []string
	// cliPath is the absolute path of the bb executable this adapter last
	// started: where the system found the configured command.
	cliPath string
	// hostKills counts bb commands ended by a signal this adapter did not send.
	hostKills int
}

// CLIPath implements adapter.CLIPather: the absolute path of the bb
// executable this adapter last started, or "" before it started one.
func (a *Adapter) CLIPath() string { return a.cliPath }

// New returns an adapter for the thread.
func New(cfg config.BB) *Adapter {
	if cfg.CLI == "" {
		cfg.CLI = config.DefaultBBCLI
	}
	a := &Adapter{cfg: cfg, Timeout: 30 * time.Second, now: time.Now}
	a.run = a.exec
	return a
}

// WithStateDir keeps the log cursor in dir so later ticks read only new events.
func (a *Adapter) WithStateDir(dir string) *Adapter {
	a.stateDir = dir
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
		Evidence:   evidence.Clean(evidence.Detail{Source: "bb_status"}),
		State:      adapter.ParseState(t.Status),
		ErrorClass: decide.ErrNone,
		Queued:     t.Queued,
		Background: t.BackgroundJobs,
		Gone:       t.ArchivedAt != nil || t.DeletedAt != nil,
	}
	if obs.Gone {
		return obs, nil
	}
	obs.ActiveChildren, obs.ChildrenUnverified, obs.AwaitingChildren, obs.ErroredChildren, obs.HealthyChildren = a.childWork(ctx)
	obs.ActiveChildIDs = a.activeChildIDs
	if len(obs.AwaitingChildren) > 0 {
		if obs.State == decide.Error {
			obs.ErrorClass = decide.ErrUnknown
		}
		return obs, nil // Child input already requires a hold; no parent log needed.
	}
	sig := a.signals(ctx)
	if sig.ok && sig.hasError {
		obs.Evidence = sig.evidence
	} else if !sig.ok {
		obs.Evidence = evidence.Clean(evidence.Detail{})
	}
	// A pending permission prompt, question, or form means the thread is
	// waiting on a person, whatever its status says.
	if obs.State != decide.Stopping {
		obs.AwaitingInput = a.pendingInteraction(ctx, sig)
	}
	if obs.State == decide.Active || obs.State == decide.Error || obs.State == decide.Idle {
		obs.RetryUnverified = !sig.ok || (obs.State == decide.Active && sig.retryUnknown)
		obs.RetryKnown = sig.ok && (!sig.hasError || !sig.retryUnknown || obs.State == decide.Idle)
	}
	if obs.State == decide.Active || obs.State == decide.Error {
		obs.Retrying = sig.ok && sig.willRetry
		// A notice counts only while there is fresh structured evidence behind
		// it, so a provider that stopped retrying without saying so cannot hold
		// the loop forever. See retryExpired.
		if obs.Retrying && a.retryExpired(obs.State, sig) {
			obs.Retrying, obs.RetryExpired = false, true
		}
		// A fresh notice on an errored thread is still dropped at once when
		// BB's retry plugin says it will not retry this failure. Nothing is
		// running on an errored thread, so no later notice can revive it.
		if obs.Retrying && obs.State == decide.Error && decide.RetryNoticeDead(a.retryService(ctx, sig.turnStartedMs)) {
			obs.Retrying, obs.RetryExpired, obs.RetryDeclined = false, true, true
		}
		if sig.ok && sig.hasError {
			obs.ErrorClass = sig.class
		}
	}
	if obs.State != decide.Error && obs.State != decide.Idle {
		return obs, nil
	}
	if obs.State == decide.Error {
		obs.ErrorClass = sig.class
	}
	// A blocked subscription window on an idle thread means the next turn
	// would fail; treat it as a rate-limit error so it is handled the same way.
	if sig.blocked && sig.resetsAt.After(a.now()) {
		if obs.State == decide.Idle {
			obs.State = decide.Error
		}
		if obs.ErrorClass == decide.ErrUnknown || obs.ErrorClass == decide.ErrNone || obs.ErrorClass == decide.ErrNetwork {
			obs.ErrorClass = decide.ErrRateLimit
			if sig.ok {
				obs.Evidence = evidence.Clean(evidence.Detail{Source: "bb_rate_limits", Code: "subscription_window_blocked"})
			}
		}
	}
	if obs.ErrorClass == decide.ErrRateLimit && sig.resetsAt.After(a.now()) {
		obs.ResetsAt = sig.resetsAt
	}
	return obs, nil
}

// DefaultRetrySignalTTL is how long a willRetry notice counts as a live retry
// without newer evidence. It bounds silence, not the length of a retry: each
// failed attempt of a genuine retry logs a newer notice and restarts the
// interval. A retry that BB's retry plugin has scheduled does not depend on
// it: that retry is a queued message, counted in queuedMessageCount, and an
// errored thread with queued messages is left alone by the decision engine.
// See docs/operations.md "Stale retry notices".
const DefaultRetrySignalTTL = 15 * time.Minute

// retryExpired reports whether a retry notice has gone stale at check time.
//
//   - Errored thread: nothing is running, so only the newest notice counts.
//   - Active thread: any newer provider or system event in the thread log
//     (tool output, another notice) shows the turn is still alive, so the
//     notice expires only after the log has been silent for the interval.
//     Requested input, including this supervisor's own nudges, is not thread
//     activity.
//   - Rate-limit notice with a structured reset: the interval starts at the
//     reset, not at the notice. Only a notice that is itself classified as a
//     rate limit qualifies: the last blocked window stays in the summary
//     across turns and must not lend its reset to a later network error.
//
// Evidence with no usable time cannot be shown to be fresh. A local clock
// that moved backwards yields a negative age, which counts as fresh.
func (a *Adapter) retryExpired(state decide.State, sig signals) bool {
	ttl := a.cfg.RetrySignalTTL.Duration
	if ttl <= 0 {
		ttl = DefaultRetrySignalTTL
	}
	at := sig.retryAt
	if state == decide.Active {
		at = sig.lastEventAt
	}
	if sig.class == decide.ErrRateLimit && sig.resetsAt.After(at) {
		at = sig.resetsAt
	}
	return at.IsZero() || a.now().Sub(at) > ttl
}

// CheckAt fixes the time this adapter compares event and reset times against,
// so one check uses one clock for its observation and its decision.
func (a *Adapter) CheckAt(t time.Time) { a.now = func() time.Time { return t } }

type signals struct {
	retryAt      time.Time // bounded time of the newest retry notice
	lastEventAt  time.Time // bounded time of the newest event of any kind
	evidence     evidence.Detail
	hasError     bool
	class        decide.ErrorClass
	willRetry    bool
	retryUnknown bool
	blocked      bool
	resetsAt     time.Time
	pending      bool
	ok           bool
	// turnStartedMs is BB's time of the latest turn start, or 0 when unknown.
	turnStartedMs int64
}

// signals reads the thread's new log events and returns the current error,
// rate-limit, and interaction picture. Only errors since the latest turn
// started count, so a stale error from an earlier turn is ignored.
func (a *Adapter) signals(ctx context.Context) signals {
	c, ok := a.scan(ctx)
	s := signals{class: decide.ErrUnknown, ok: ok, blocked: c.Blocked, resetsAt: c.ResetsAt, pending: len(c.Pending) > 0, lastEventAt: c.LastEventAt, turnStartedMs: c.TurnStartedMs}
	if c.Error != nil {
		s.hasError = true
		s.evidence = evidence.Clean(c.Error.Evidence)
		if s.evidence.Source == "unavailable" {
			s.evidence = evidence.Clean(evidence.Detail{Source: "bb_error_summary"})
		}
		s.class, s.willRetry = c.Error.Class, c.Error.WillRetry
		s.retryUnknown = !c.Error.RetryKnown
		s.retryAt = c.Error.At
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
// it uses the interaction lifecycle events summarised by the log cursor.
// Only the structured status field is read, never the interaction's text.
func (a *Adapter) pendingInteraction(ctx context.Context, sig signals) bool {
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
	return sig.pending
}

// Send implements adapter.Adapter via `bb thread tell`.
func (a *Adapter) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	return a.call(ctx, prompt, "thread", "tell", a.cfg.Thread, "--message-file", "-", "--mode", string(mode), "--json")
}

// Stop implements adapter.Adapter via `bb thread stop`.
func (a *Adapter) Stop(ctx context.Context) error {
	return a.call(ctx, "", "thread", "stop", a.cfg.Thread, "--json")
}

// retryReason is shown by BB on a queued retry row. It is fixed text; nothing
// from the task or the loop is sent with a retry.
const retryReason = "keepgoing: recover the failed turn"

// RetryFailedTurn implements adapter.TurnRetrier via `bb thread retry`, which
// re-submits the failed turn by reference: no new user message, and an
// attempt number that increments. Only the JSON result on stdout is read.
//
//	{"ok":true,"delivery":"sent"|"queued","attempt":N,...}
//	{"ok":false,"error":{"code":"no_failed_turn"|"retry_already_queued"|...}}
//
// BB applies no age limit: a failed turn stays retryable for as long as the
// thread is in error and its last turn request is stored. When the provider
// had already accepted that turn's input, BB continues it with a short nudge
// of its own instead of sending the input twice.
//
// Any other code, delivery or shape is an error: the caller must not treat an
// unreadable result as permission to send a prompt.
func (a *Adapter) RetryFailedTurn(ctx context.Context) (adapter.TurnRetry, error) {
	out, runErr := a.run(ctx, "", "thread", "retry", a.cfg.Thread, "--reason", retryReason, "--json")
	var res struct {
		OK       *bool  `json:"ok"`
		Delivery string `json:"delivery"`
		Attempt  int    `json:"attempt"`
		Error    *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(out, &res) != nil || res.OK == nil {
		if runErr != nil {
			return adapter.TurnRetry{}, fmt.Errorf("bb thread retry: %w", runErr)
		}
		return adapter.TurnRetry{}, errors.New("bb thread retry: unreadable result")
	}
	attempt := max(0, res.Attempt)
	if *res.OK {
		switch res.Delivery {
		case "sent":
			return adapter.TurnRetry{Outcome: adapter.TurnRetried, Attempt: attempt}, nil
		case "queued":
			return adapter.TurnRetry{Outcome: adapter.TurnRetryQueued, Attempt: attempt}, nil
		}
		return adapter.TurnRetry{}, errors.New("bb thread retry: unrecognised delivery")
	}
	code := ""
	if res.Error != nil {
		code = res.Error.Code
	}
	switch code {
	case "no_failed_turn":
		return adapter.TurnRetry{Outcome: adapter.TurnRetryNothing}, nil
	case "retry_already_queued":
		return adapter.TurnRetry{Outcome: adapter.TurnRetryAlreadyQueued}, nil
	case "unknown_command":
		// A BB release without `thread retry`: recover as before it existed.
		return adapter.TurnRetry{}, adapter.ErrUnsupported{Op: "turn retry"}
	}
	// Only a short code token is kept; BB's message may hold prose and is dropped.
	return adapter.TurnRetry{}, fmt.Errorf("bb thread retry: refused (%s)", codeToken(code))
}

// StillFailed implements adapter.TurnRetrier with one `bb thread show`. BB
// answers no_failed_turn both for a thread that failed before it dispatched a
// turn and for one that is no longer in error, for example because BB's own
// retry or a person restarted it a moment earlier. Only the first may be
// prompted; a prompt sent in auto mode to the second would steer a live turn.
func (a *Adapter) StillFailed(ctx context.Context) (bool, error) {
	out, err := a.run(ctx, "", "thread", "show", a.cfg.Thread, "--json")
	if code, ok := envelopeError(out); ok {
		return false, fmt.Errorf("bb thread show: %s", codeToken(code))
	}
	if err != nil {
		return false, fmt.Errorf("bb thread show: %w", err)
	}
	var resp showResponse
	if json.Unmarshal(out, &resp) != nil || resp.Thread == nil {
		return false, errors.New("bb thread show: unexpected JSON")
	}
	t := resp.Thread
	return adapter.ParseState(t.Status) == decide.Error && t.Queued == 0 && t.ArchivedAt == nil && t.DeletedAt == nil, nil
}

// codeToken keeps a code that looks like one and replaces anything else.
func codeToken(code string) string {
	if code == "" || len(code) > 48 {
		return "unknown"
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return "unknown"
		}
	}
	return code
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
	// A killed bb whose children still hold its output must not hold the
	// check open past the timeout.
	cmd.WaitDelay = 2 * time.Second
	// Only the JSON envelope on stdout is trusted; stderr may hold prose.
	err := cmd.Run()
	if cmd.Process != nil {
		// It started: cmd.Path is the executable the system resolved. A
		// structural fact about this machine, nothing read from its output.
		if p, perr := filepath.Abs(cmd.Path); perr == nil {
			a.cliPath = p
		}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == -1 && ctx.Err() == nil {
		// Ended by a signal that was not this adapter's time limit: the host
		// killed it, as it does when out of memory or badly overloaded. Go
		// reports -1 only for a signal, so on Windows this never fires.
		a.hostKills++
	}
	if err != nil && ctx.Err() != nil {
		// Stopped at the limit: whatever it printed, the command did not end
		// on its own.
		err = fmt.Errorf("%w: %w", errStopped, err)
	}
	return stdout.Bytes(), err
}

// HostKills implements adapter.KillReporter: how many bb commands of this
// adapter were ended by a signal it did not send.
func (a *Adapter) HostKills() int { return a.hostKills }

// errStopped marks a bb command that was killed at its time limit or because
// the check was cancelled.
var errStopped = errors.New("bb command stopped before it finished")
