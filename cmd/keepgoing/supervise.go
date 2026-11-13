package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/bb"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/factory"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
	"github.com/connorburgess/keepgoing-agents/internal/window"
)

func validate(e env, cfg *config.Config, path string, takeOver ...bool) int {
	if err := checkTransferSources(cfg, false); err != nil {
		return ownershipError(e, err)
	}
	if err := validateOwner(cfg.Loop.StateDir, len(takeOver) > 0 && takeOver[0]); err != nil {
		return ownershipError(e, err)
	}
	l := cfg.Loop
	if len(takeOver) > 0 && takeOver[0] {
		fmt.Fprintln(e.stdout, "ownership: validation only; use enable --take-over to transfer the claim")
	}
	fmt.Fprintf(e.stdout, "ok: %s (loop %s, %s)\n", path, l.Name, l.Describe())
	if fb := cfg.FallbackSet; fb != nil {
		fmt.Fprintf(e.stdout, "ok: %s (%d candidates, min_healthy %d across %s)\n", l.Fallbacks, len(fb.Candidates), fb.Redundancy.MinHealthy, fb.Redundancy.Span)
	}
	if limits := l.Limits.Configured(); len(limits) > 0 {
		fmt.Fprintf(e.stdout, "bounds: %s\n", strings.Join(limits, ", "))
	} else {
		e.errf("warning: %s", config.UnboundedWarning)
	}
	if w := l.TickTimeoutWarning(); w != "" {
		e.errf("warning: %s", w)
	}
	if w := l.NetworkCheckWarning(); w != "" {
		e.errf("warning: %s", w)
	}
	// A defined agent's own mapping decides what moves work to a fallback,
	// so every such mapping is said in words.
	defined := []struct {
		where string
		agent config.Agent
	}{{"primary", l.Agent}}
	if fb := cfg.FallbackSet; fb != nil {
		for _, c := range fb.Candidates {
			defined = append(defined, struct {
				where string
				agent config.Agent
			}{c.ID, c.Agent})
		}
	}
	for _, d := range defined {
		if d.agent.Custom == nil {
			continue
		}
		for _, move := range d.agent.Custom.FailoverMappings(l.Errors.FailoverOn) {
			fmt.Fprintf(e.stdout, "failover (%s, defined agent %s): %s\n", d.where, d.agent.Custom.Name, move)
		}
	}
	if l.Report != nil {
		fmt.Fprintf(e.stdout, "report: %s format, %s, events %s\n", l.Report.Format, l.Report.Scheme, strings.Join(l.Report.Events, ","))
	}
	return exitOK
}

// tickOnce runs one pass under the tick lock.
func tickOnce(e env, cfg *config.Config, now time.Time, dryRun, fixedNow bool, leases ...*ownerLease) int {
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		e.errf("another tick holds the lock; skipping")
		return exitSkipped
	}
	if err != nil {
		e.errf("lock: %v", err)
		return exitFailed
	}
	defer l.Release()
	if err := checkOwnership(cfg.Loop.StateDir, firstLease(leases)); err != nil {
		return ownershipError(e, err)
	}
	if err := checkTransferSources(cfg, true); err != nil {
		e.errf("%v; no tick performed; reload and retry", err)
		return exitSkipped
	}
	opts := supervise.Options{Version: version, DryRun: dryRun, Reporter: reporter(e, cfg), Routine: suppliedRoutine(e.ctx), RoutineIgnored: e.ctx.Value(routineIgnoredKey{}) != nil}
	if fixedNow {
		opts.Now = now
	}
	res, err := supervise.Tick(e.ctx, cfg, opts)
	out, _ := json.Marshal(res)
	fmt.Fprintln(e.stdout, string(out))
	if err != nil {
		e.errf("tick: %v", err)
		if errors.Is(err, lock.ErrHeld) {
			return exitSkipped
		}
		// The exit code says whether keepgoing ran and recorded the check, not
		// whether the session, provider or repository was healthy. A recorded
		// check that could not observe or act has outcome "failed" in its
		// output and in status, and exits 0, so an external scheduler that
		// counts non-zero exits does not disable supervision over an outage.
		if res.Recorded {
			return exitOK
		}
		return exitFailed
	}
	if res.Yielded {
		return exitSkipped
	}
	return exitOK
}

// daemon ticks on an interval. Between ticks it watches control.json, so a
// stop or pause requests a check within one control_poll rather than waiting
// for the next interval. Provider work requires a verified config snapshot.
// SIGINT or SIGTERM ends the daemon at once; a tick in flight is cancelled
// and its child commands are killed.
func daemon(e env, cfg *config.Config, cfgPath string, dryRun bool, takeOver ...bool) int {
	if err := checkTransferSources(cfg, false); err != nil {
		return ownershipError(e, err)
	}
	l, err := lock.AcquireNamed(cfg.Loop.StateDir, lock.Daemon)
	if errors.Is(err, lock.ErrHeld) {
		e.errf("another daemon is running for %s", cfg.Loop.Name)
		return exitSkipped
	}
	if err != nil {
		e.errf("lock: %v", err)
		return exitFailed
	}
	defer l.Release()
	lease, err := bindDaemonOwner(cfg.Loop.StateDir, len(takeOver) > 0 && takeOver[0])
	if err != nil {
		return ownershipError(e, err)
	}
	monitor, monitorErr := daemoninfo.Start(cfg.Loop.StateDir, version, dryRun)
	if monitorErr != nil {
		e.errf("warning: live daemon diagnostics unavailable; supervision will continue")
	} else {
		defer monitor.Close()
	}
	return daemonLoop(e, cfg, cfgPath, dryRun, lease, monitor)
}

// The caller owns daemon.lock and the diagnostic endpoint for this entire loop.
func daemonLoop(e env, cfg *config.Config, cfgPath string, dryRun bool, lease *ownerLease, monitor *daemoninfo.Server) int {
	state.Append(cfg.Loop.StateDir, state.Event{Loop: cfg.Loop.Name, Event: "daemon", Reason: "started"})
	defer state.Append(cfg.Loop.StateDir, state.Event{Loop: cfg.Loop.Name, Event: "daemon", Reason: "stopped"})
	for {
		// Reload so edits to keepgoing.yaml or fallbacks.yaml apply without
		// a restart. A broken edit retains the timing/diagnostic context,
		// but the source guard suppresses provider work until it is repaired.
		if fresh, err := config.Load(cfgPath); err != nil {
			e.errf("config reload failed; retaining timing and diagnostics, provider work requires verified sources: %v", err)
		} else if fresh.Loop.StateDir != cfg.Loop.StateDir {
			e.errf("config reload changes state_dir; restart the daemon to apply it; retaining timing and diagnostics, skipping provider work")
		} else {
			cfg = fresh
		}
		if e.ctx.Err() != nil {
			return exitOK
		}
		if err := checkOwnership(cfg.Loop.StateDir, lease); err != nil {
			return ownershipError(e, err)
		}
		// Take the control signature before the pass, so a stop or pause
		// written while the pass runs starts the next pass immediately.
		sig := controlSignature(cfg.Loop.StateDir)
		monitor.Update("checking", nil)
		tickOnce(e, cfg, time.Time{}, dryRun, false, lease)
		if err := checkOwnership(cfg.Loop.StateDir, lease); err != nil {
			return ownershipError(e, err)
		}
		if !waitForNext(e.ctx, cfg, sig, monitor, lease) {
			return exitOK
		}
	}
}

// waitForNext returns false when the daemon should exit.
func waitForNext(ctx context.Context, cfg *config.Config, sig string, monitor *daemoninfo.Server, leases ...*ownerLease) bool {
	return waitForNextWithPoll(ctx, cfg, sig, monitor, func(ctx context.Context) bool {
		return pollWake(ctx, cfg, leases...)
	})
}

// Keep timer/control behavior testable independently of subprocess latency.
func waitForNextWithPoll(ctx context.Context, cfg *config.Config, sig string, monitor *daemoninfo.Server, poll func(context.Context) bool) bool {
	if controlSignature(cfg.Loop.StateDir) != sig {
		return ctx.Err() == nil
	}
	deadline := time.Now().Add(cfg.Loop.Daemon.Interval.Duration)
	monitor.Update("waiting", &deadline)
	t := time.NewTicker(cfg.Loop.Daemon.ControlPoll.Duration)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case now := <-t.C:
			// Both cases can be ready at once and select picks at random. Never
			// poll, and so never consume a wake notice, after cancellation.
			if ctx.Err() != nil {
				return false
			}
			if now.After(deadline) || controlSignature(cfg.Loop.StateDir) != sig {
				return true
			}
			if poll(ctx) || controlSignature(cfg.Loop.StateDir) != sig {
				return ctx.Err() == nil
			}
		}
	}
}

// Share the tick lock with manual ticks and retargets, and bound the whole
// notification poll to one control cycle so a slow CLI cannot delay controls
// by the ordinary adapter timeout. Lock contention waits for a later cycle.
func pollWake(ctx context.Context, cfg *config.Config, leases ...*ownerLease) bool {
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		return false
	}
	defer l.Release()
	if err := checkOwnership(cfg.Loop.StateDir, firstLease(leases)); err != nil {
		return len(leases) > 0
	} // Return to the daemon's exit check.
	if checkTransferSources(cfg, false) != nil {
		return false // Wait for the next ordinary reload; never poll the stale target.
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Loop.Daemon.ControlPoll.Duration)
	defer cancel()
	return supervise.PollWake(ctx, cfg, nil)
}

func controlSignature(stateDir string) string {
	return controlSignatureWithOpen(stateDir, control.OpenSnapshot)
}

func controlSignatureWithOpen(stateDir string, open func(string) (*os.File, error)) string {
	path := filepath.Join(stateDir, control.FileName)
	info, err := os.Lstat(path)
	if err != nil {
		return "missing"
	}
	metadata := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	if !info.Mode().IsRegular() {
		return metadata + ":unavailable"
	}
	f, err := open(stateDir)
	if err != nil {
		return metadata + ":unavailable"
	}
	defer f.Close()
	// A cooperating control writer replaces the whole regular file atomically.
	// Read metadata from the opened file, then hash only that observed size with
	// bounded memory. The digest stays local; it is never a diagnostic payload.
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return metadata + ":unavailable"
	}
	metadata = fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	h := sha256.New()
	if _, err := io.CopyN(h, f, info.Size()); err != nil {
		return metadata + ":unavailable"
	}
	return fmt.Sprintf("%s:%x", metadata, h.Sum(nil))
}

type statusOut struct {
	PromptSend        promptSendStatus         `json:"promptSend"`
	LimitWait         limitWaitStatus          `json:"limitWait"`
	TurnRetry         turnRetryStatus          `json:"turnRetry"`
	ChildErrorReport  childReportStatus        `json:"childErrorReport"`
	NewThreads        newThreadStatus          `json:"newThreads"`
	Goal              *control.GoalSummary     `json:"goal,omitempty"`
	GoalError         string                   `json:"goalError,omitempty"`
	Notifications     report.DeliveryStatus    `json:"notifications"`
	ProgressAllowance *progressAllowanceStatus `json:"progressAllowance,omitempty"`
	SendAllowance     *sendAllowanceStatus     `json:"sendAllowance,omitempty"`
	Loop              string                   `json:"loop"`
	Primary           string                   `json:"primary"`
	Target            string                   `json:"target"`
	Control           control.File             `json:"control"`
	ControlState      string                   `json:"controlState"`
	// Unreadable lists the records status could not read. The rest of the
	// output is what it could; the command still exits 1.
	Unreadable      []unreadableRecord `json:"unreadable,omitempty"`
	Deadline        string             `json:"deadline,omitempty"`
	DeadlineReached bool               `json:"deadlineReached"`
	WorkBounds      workBoundsStatus   `json:"workBounds"`
	TickBudget      tickBudgetStatus   `json:"tickBudget"`
	// TakeoverBriefs summarizes rejected handoff and switch-back briefs.
	TakeoverBriefs takeoverBriefsStatus `json:"takeoverBriefs"`
	Window         string               `json:"window"`
	InWindow       bool                 `json:"inWindow"`
	State          state.File           `json:"state"`
	Redundancy     *health.Summary      `json:"redundancy,omitempty"`
	Usage          []usageView          `json:"usage"`
	StateDir       string               `json:"stateDir"`
	Version        string               `json:"version"`
	// Structured identities for tools that match a loop to a session, such
	// as an agent workspace showing keepgoing's state beside a thread.
	PrimaryAgent     agentRef       `json:"primaryAgent"`
	TargetAgent      agentRef       `json:"targetAgent"`
	TargetConfigured bool           `json:"targetConfigured"`
	TaskSource       string         `json:"taskSource"`
	TaskFile         string         `json:"taskFile,omitempty"`
	Runtime          runtimeStatus  `json:"runtime"`
	TargetSession    *sessionStatus `json:"targetSession,omitempty"`
	Daemon           daemonStatus   `json:"daemon"`
}

// newThreadStatus reports the BB threads this loop has created for handoffs to
// fallbacks with bb.new_thread. Active is when the active target's thread was
// created, if keepgoing created it. Pending means a creation's outcome is not
// yet known.
type newThreadStatus struct {
	Created int        `json:"created"`
	Max     int        `json:"max"`
	Pending bool       `json:"pending"`
	Active  *time.Time `json:"activeCreatedAt,omitempty"`
	// NextAllowedAt is the earliest another thread may be created: one hour
	// after the last.
	NextAllowedAt *time.Time `json:"nextAllowedAt,omitempty"`
}

// takeoverBriefsStatus reports recorded rejections against errors.max_reprompts.
// Capped lists fallbacks that will not be offered a handoff brief again this
// error episode. SwitchBackStopped means automatic switch-back has ended until
// resume, retarget or the next handoff.
type takeoverBriefsStatus struct {
	Cap                int      `json:"cap"`
	Capped             []string `json:"capped"`
	SwitchBackRejected int      `json:"switchBackRejected"`
	SwitchBackStopped  bool     `json:"switchBackStopped"`
}

// workBoundsStatus reports which loop-wide limits the loaded configuration
// sets. It does not say whether one has been reached; see the allowance fields.
// tickBudgetStatus is the configured time budget of one check. Grown means
// tick_timeout was omitted and raised above its default to fit the fallback
// probe timeout. Warning is set when a slow check may outlast a five-minute
// scheduler timeout.
type tickBudgetStatus struct {
	Timeout string `json:"timeout"`
	Grown   bool   `json:"grown"`
	Warning string `json:"warning,omitempty"`
}

type workBoundsStatus struct {
	Limits    []string `json:"limits"`
	Unbounded bool     `json:"unbounded"`
	Warning   string   `json:"warning,omitempty"`
}

// progressAllowanceStatus reports historical evidence without querying Git.
type progressAllowanceStatus struct {
	Timeout         string     `json:"timeout"`
	ChangedAt       *time.Time `json:"changedAt,omitempty"`
	ObservedAt      *time.Time `json:"observedAt,omitempty"`
	Due             bool       `json:"due"`
	Error           string     `json:"error,omitempty"`
	BaselinePending bool       `json:"baselinePending,omitempty"`
}

// sendAllowanceStatus omits unknown counts when accounting cannot be read.
type sendAllowanceStatus struct {
	Max       int64  `json:"max"`
	Used      *int64 `json:"used,omitempty"`
	Remaining *int64 `json:"remaining,omitempty"`
	Reached   bool   `json:"reached"`
	Error     string `json:"error,omitempty"`
}

// agentRef identifies the session an agent definition points at.
type agentRef struct {
	Group    string `json:"group,omitempty"`
	Adapter  string `json:"adapter"`
	Provider string `json:"provider,omitempty"`
	BBThread string `json:"bbThread,omitempty"`
	Workdir  string `json:"workdir,omitempty"`
	Model    string `json:"model,omitempty"`
}

func refOf(a config.Agent) agentRef {
	r := agentRef{Adapter: a.Adapter, Provider: a.Provider, Group: a.Group, Model: a.Model()}
	switch {
	case a.BB != nil:
		r.BBThread = a.BB.Thread
	case a.Cursor != nil:
		r.Workdir = a.Cursor.Workdir
	case a.Claude != nil:
		r.Workdir = a.Claude.Workdir
	case a.Codex != nil:
		r.Workdir = a.Codex.Workdir
	}
	return r
}

func status(e env, cfg *config.Config, now time.Time, asJSON bool, staleAfter time.Duration) int {
	out, err := readStatus(e.ctx, cfg, now, staleAfter, inspectDaemon)
	if err != nil && len(out.Unreadable) == 0 {
		e.errf("%v", err)
		return exitFailed
	}
	code := exitOK
	if err != nil {
		code = exitFailed
		for _, u := range out.Unreadable {
			e.errf("%s could not be read (%s). %s", u.File, u.Problem, u.Remedy)
		}
	}
	defer func() {
		if code != exitOK && !asJSON {
			fmt.Fprintf(e.stdout, "unreadable records: %d; the lines above show what could still be read\n", len(out.Unreadable))
		}
	}()
	ctl, st := out.Control, out.State
	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
		return code
	}
	w := e.stdout
	fmt.Fprintf(w, "loop:      %s\n", out.Loop)
	if ctl.PendingTransfer != nil {
		fmt.Fprintf(w, "transfer: pending %s (%s -> %s); use transfer recover\n", ctl.PendingTransfer.ID, ctl.PendingTransfer.From, ctl.PendingTransfer.To)
	}
	if ctl.BBTarget != "" {
		fmt.Fprintf(w, "bound BB target: %s\n", ctl.BBTarget)
	}
	if out.Goal != nil {
		printGoal(e, *out.Goal, false)
	}
	if out.GoalError != "" {
		fmt.Fprintf(w, "goal: unavailable (%s)\n", out.GoalError)
	}
	printPromptSend(w, out.PromptSend)
	if n := out.NewThreads; n.Created > 0 || n.Pending {
		fmt.Fprintf(w, "new threads: keepgoing has used %d of at most %d BB thread creation(s) for handoffs", n.Created, n.Max)
		if n.Active != nil {
			fmt.Fprintf(w, "; the active target is one of them, created %s", when(*n.Active))
		}
		if n.Pending {
			fmt.Fprint(w, "; one creation could not be confirmed: nothing is created or handed off until a person settles it with retarget")
		}
		fmt.Fprintln(w)
	}
	printLimitWait(w, out.LimitWait)
	printTurnRetry(w, out.TurnRetry)
	printChildReport(w, out.ChildErrorReport)
	fmt.Fprintf(w, "task:      %s", out.TaskSource)
	if out.TaskFile != "" {
		fmt.Fprintf(w, " (%s)", out.TaskFile)
	}
	fmt.Fprintln(w, "; task text withheld")
	fmt.Fprintf(w, "primary:   %s\n", out.Primary)
	fmt.Fprintf(w, "target:    %s", out.Target)
	if st.Handoffs > 0 {
		fmt.Fprintf(w, " (%d handoffs, last %s)", st.Handoffs, when(st.LastHandoffAt))
	}
	fmt.Fprintln(w)
	if s := out.TargetSession; s != nil {
		fmt.Fprintf(w, "session:   %s", s.State)
		if s.ID != "" {
			fmt.Fprintf(w, " (%s)", s.ID)
		}
		fmt.Fprintf(w, "; %s\n", s.Message)
	}
	if out.TargetConfigured {
		fmt.Fprintf(w, "account:   quota group %s (identity not verified)\n", out.TargetAgent.Group)
		if out.TargetAgent.BBThread != "" {
			fmt.Fprintf(w, "thread:    %s\n", out.TargetAgent.BBThread)
		}
	} else {
		fmt.Fprintln(w, "target configuration: missing or disabled; run doctor before resuming")
	}
	if ctl.Ownership != nil {
		fmt.Fprintf(w, "owner:     %s (generation %d)\n", ctl.Ownership.ThreadID, ctl.Ownership.Generation)
		if p := ctl.Ownership.Previous; p != nil {
			fmt.Fprintf(w, "previous owner: %s (%s)\n", p.ThreadID, p.Status)
		}
	}
	fmt.Fprintf(w, "control:   %s", out.ControlState)
	if ctl.Note != "" {
		fmt.Fprintf(w, " (%s)", ctl.Note)
	}
	fmt.Fprintln(w)
	printDeliveryStatus(w, out.Notifications)
	fmt.Fprintf(w, "window:    %s\n", out.Window)
	tickBudget := out.TickBudget.Timeout
	if out.TickBudget.Grown {
		tickBudget += fmt.Sprintf(" (tick_timeout is not set; raised from the %s default to fit fallbacks probe.timeout)", config.DefaultTickTimeout)
	}
	fmt.Fprintf(w, "check budget: %s\n", tickBudget)
	if out.TickBudget.Warning != "" {
		fmt.Fprintf(w, "check budget: warning: %s\n", out.TickBudget.Warning)
	}
	if out.WorkBounds.Unbounded {
		fmt.Fprintf(w, "bounds:    warning: %s\n", out.WorkBounds.Warning)
	} else {
		fmt.Fprintf(w, "bounds:    %s\n", strings.Join(out.WorkBounds.Limits, ", "))
	}
	if a := out.SendAllowance; a != nil {
		if a.Error != "" {
			fmt.Fprintf(w, "sends:     unavailable (%s)\n", a.Error)
		} else {
			fmt.Fprintf(w, "sends:     %d/%d attempts reserved; %d remaining (reached: %t)\n", *a.Used, a.Max, *a.Remaining, a.Reached)
		}
	}
	if a := out.ProgressAllowance; a != nil {
		switch {
		case a.Error != "":
			fmt.Fprintf(w, "progress:  unavailable (%s)\n", a.Error)
		case a.BaselinePending:
			fmt.Fprintf(w, "progress:  %s timeout; awaiting first verified baseline\n", a.Timeout)
		default:
			fmt.Fprintf(w, "progress:  %s timeout; last verified change %s (due: %t; next tick rechecks Git)\n", a.Timeout, a.ChangedAt.Format(time.RFC3339), a.Due)
		}
	}
	if out.Deadline != "" {
		fmt.Fprintf(w, "deadline:  %s (reached: %t; evaluated on next check)\n", out.Deadline, out.DeadlineReached)
	}
	fmt.Fprintf(w, "activity:  %s (source: %s)\nreason:    %s\n", out.Runtime.State, out.Runtime.Source, out.Runtime.Reason)
	fmt.Fprintf(w, "history:   %s (stale after %s)\n", out.Runtime.Freshness, out.Runtime.StaleAfter)
	if c := st.LastCheck; c != nil {
		fmt.Fprintf(w, "decision:  %s; outcome %s; target %s\n", c.Action, c.Outcome, c.Target)
		fmt.Fprintf(w, "decision reason: %s\n", c.Reason)
		fmt.Fprintf(w, "observed:  %s; target %s (historical)\n", c.ObservedState, c.ObservedTarget)
		if c.RetrySignal == "declined" {
			fmt.Fprintln(w, "retry signal: declined; BB's retry plugin recorded that it will not retry the failed turn and had nothing pending, so the provider retry notice was not waited out and ordinary error handling applied")
		}
		if c.RetrySignal == "expired" {
			fmt.Fprintln(w, "retry signal: expired; a stale provider retry notice was not honored, so ordinary error or stall handling applied")
		}
		if c.ErroredChildren > 0 {
			count := fmt.Sprintf("%d", c.ErroredChildren)
			if c.ErroredChildrenPartial {
				count = "at least " + count + " (child discovery was incomplete)"
			}
			fmt.Fprintf(w, "children:  %s direct child thread(s) in error at that check (historical); keepgoing does not retry, message or stop them\n", count)
		}
		if c.ChildErrorNoticeStopped {
			fmt.Fprintf(w, "child notice: not accepted after %d attempts; keepgoing has stopped sending it until another child fails or resume\n", st.ChildErrorNoticeAttempts)
		}
		if c.DryRun {
			fmt.Fprintln(w, "dry run:   true; the decision was not executed")
		}
	} else {
		fmt.Fprintln(w, "decision:  unavailable; no detailed check record")
	}
	switch {
	case st.NetworkDownSince.IsZero():
	case st.NetworkDistrusted:
		fmt.Fprintf(w, "network:   the connectivity check has failed since %s, but the provider answered a probe, so it is not believed and the ordinary rules apply. Run doctor to test loop.network\n", st.NetworkDownSince.Format(time.RFC3339))
	default:
		reported := "not reported yet"
		if !st.NetworkReportedAt.IsZero() {
			reported = "reported " + st.NetworkReportedAt.Format(time.RFC3339)
		} else if !st.NetworkVerifiedAt.IsZero() {
			reported = "provider last tested " + st.NetworkVerifiedAt.Format(time.RFC3339) + ", report not delivered yet"
		}
		fmt.Fprintf(w, "network:   down at the last connectivity check, failing since %s (%s); work that needs the network waits\n", st.NetworkDownSince.Format(time.RFC3339), reported)
	}
	if warning := cfg.Loop.NetworkCheckWarning(); warning != "" {
		fmt.Fprintf(w, "network:   warning: %s\n", warning)
	}
	if st.FailedChecks > 0 {
		fmt.Fprintf(w, "failed checks: %d in a row since %s; checks are running but could not observe or act (see the decision reason and the run log)\n", st.FailedChecks, st.FailedSince.Format(time.RFC3339))
	}
	fmt.Fprintf(w, "build:     %s (last recorded check versus this process)\n", out.Runtime.BinaryComparison)
	fmt.Fprintf(w, "daemon:    %s; %s\n", out.Daemon.State, out.Daemon.Message)
	if out.Daemon.State == "responsive" {
		fmt.Fprintf(w, "daemon phase: %s; build %s; dry run %t\n", out.Daemon.Phase, out.Daemon.BinaryComparison, out.Daemon.DryRun)
	}
	fmt.Fprintf(w, "next check: %s\n", out.Runtime.NextCheck)
	fmt.Fprintf(w, "last check: %s\n", when(st.CheckedAt))
	fmt.Fprintf(w, "progress:  %s\n", when(st.LastProgressAt))
	fmt.Fprintf(w, "last sent: %s\n", when(st.LastWakeAt))
	if st.Reprompts > 0 {
		fmt.Fprintf(w, "reprompts: %d (last %s)\n", st.Reprompts, when(st.LastRepromptAt))
	}
	if r := out.Redundancy; r != nil {
		verdict := "handoffs allowed"
		if !r.Meets {
			verdict = "handoffs refused"
		}
		fmt.Fprintf(w, "fallbacks: %d healthy, %d distinct by %s, need %d: %s\n", len(r.Healthy), r.Distinct, r.Span, r.Min, verdict)
	}
	if b := out.TakeoverBriefs; len(b.Capped) > 0 {
		fmt.Fprintf(w, "handoff briefs: %s rejected %d and will not be tried again this error episode; resume or retarget clears this\n", strings.Join(b.Capped, ", "), b.Cap)
	}
	if b := out.TakeoverBriefs; b.SwitchBackStopped {
		fmt.Fprintf(w, "switch-back: stopped after the primary rejected %d briefs; resume, retarget or the next handoff restarts it\n", b.SwitchBackRejected)
	}
	printUsage(w, "usage:     ", out.Usage)
	fmt.Fprintf(w, "state dir: %s\n", out.StateDir)
	return code
}

// readStatus builds the status report. It reads local records and the existing
// daemon endpoint through query; it never queries a provider or writes.
func readStatus(ctx context.Context, cfg *config.Config, now time.Time, staleAfter time.Duration, query clientDaemonQuery) (statusOut, error) {
	l := cfg.Loop
	// An unreadable control or state record does not stop status: it says
	// which, and what to do, and shows what it can. The error is still
	// returned, so callers that need both records (the dock) skip the loop.
	var unreadable []unreadableRecord
	ctl, err := control.Load(l.StateDir)
	if err != nil {
		unreadable = append(unreadable, unreadableControl(err))
		ctl = control.File{}
	}
	st, stateErr := state.Load(l.StateDir)
	if stateErr != nil {
		unreadable = append(unreadable, unreadableState(stateErr))
		st = state.File{}
	}
	in, why, err := window.Inside(l.Window, now)
	if err != nil {
		return statusOut{}, err
	}
	if ctl.BBTarget != "" && !transferThreadID.MatchString(ctl.BBTarget) {
		ctl.BBTarget = "unavailable"
	}
	if ctl.PendingTransfer != nil {
		p := control.PendingBBTransfer{}
		if ctl.PendingTransfer.Valid() {
			p = *ctl.PendingTransfer
		}
		p.Before, p.After = "", ""
		ctl.PendingTransfer = &p
	}
	out := statusOut{Loop: l.Name, Primary: l.Describe(), Target: st.Target(), Control: ctl, ControlState: ctl.Summary(), Window: why, InWindow: in, State: st, StateDir: l.StateDir, Version: version, Unreadable: unreadable}
	for _, u := range unreadable {
		switch u.File {
		case control.FileName:
			out.ControlState = "unavailable"
		case state.FileName:
			out.Target = "unavailable"
		}
	}
	if g, err := control.LoadGoal(l.StateDir); err != nil {
		out.GoalError = err.Error()
	} else if g != nil {
		summary := g.Summary()
		out.Goal = &summary
	}
	out.PromptSend = describePromptSend(st.LastPromptSend, out.Goal)
	out.LimitWait = describeLimitWait(st.LimitWaitReport, l, now)
	out.TurnRetry = describeTurnRetry(st.LastTurnRetry)
	out.ChildErrorReport = describeChildReport(st.ChildErrorReport, l)
	if l.Limits.MaxSends > 0 {
		out.SendAllowance = &sendAllowanceStatus{Max: l.Limits.MaxSends}
		used, err := state.LoadSends(l.StateDir)
		if err != nil {
			out.SendAllowance.Error = err.Error()
		} else {
			remaining := max(int64(0), l.Limits.MaxSends-used)
			out.SendAllowance.Used, out.SendAllowance.Remaining = &used, &remaining
			out.SendAllowance.Reached = used >= l.Limits.MaxSends
		}
	}
	if timeout := l.Limits.NoProgressTimeout.Duration; timeout > 0 {
		a := &progressAllowanceStatus{Timeout: timeout.String()}
		out.ProgressAllowance = a
		p, err := state.LoadProgress(l.StateDir)
		switch {
		case err != nil:
			a.Error = err.Error()
		case p == nil:
			a.BaselinePending = true
		case now.Before(p.ObservedAt):
			a.Error = "host clock precedes the last verified progress observation"
		case p.Sources != state.ProgressSources(l.Progress.Repositories):
			a.Error = "repository list changed; next tick must rebase without replenishing time"
		default:
			a.ChangedAt, a.ObservedAt, a.Due = &p.ChangedAt, &p.ObservedAt, p.Reached(now, timeout)
		}
	}
	out.Deadline, out.DeadlineReached = l.Limits.Deadline, l.Limits.DeadlineReached(now)
	out.TakeoverBriefs = takeoverBriefsStatus{Cap: l.Errors.MaxReprompts, Capped: []string{}, SwitchBackRejected: st.SwitchBackRejected,
		SwitchBackStopped: l.SwitchBack.Enabled && st.Target() != state.Primary && st.SwitchBackRejected >= l.Errors.MaxReprompts}
	for id, r := range st.HandoffBriefs {
		if r.Failed >= l.Errors.MaxReprompts {
			out.TakeoverBriefs.Capped = append(out.TakeoverBriefs.Capped, id)
		}
	}
	sort.Strings(out.TakeoverBriefs.Capped)
	out.WorkBounds = workBoundsStatus{Limits: l.Limits.Configured()}
	out.TickBudget = tickBudgetStatus{Timeout: l.TickTimeout.Duration.String(), Grown: l.TickTimeoutGrown(), Warning: l.TickTimeoutWarning()}
	if len(out.WorkBounds.Limits) == 0 {
		out.WorkBounds.Unbounded, out.WorkBounds.Warning = true, config.UnboundedWarning
	}
	out.PrimaryAgent = refOf(l.Agent)
	if st.Target() == state.Primary {
		out.TargetAgent, out.TargetConfigured = refOf(l.Agent), true
	} else if c, ok := cfg.FallbackSet.Find(st.Target()); ok && !c.Disabled {
		// A fallback that starts a new thread at handoff is a target only
		// once its thread exists.
		if a, known := st.CandidateAgent(c); known {
			out.TargetAgent = refOf(a)
			out.TargetConfigured = true
		}
	}
	out.NewThreads = newThreadStatus{Created: st.NewThreadsCreated, Max: l.Limits.MaxNewThreads, Pending: st.NewThreadIntent != nil}
	if !st.LastNewThreadAt.IsZero() {
		next := st.LastNewThreadAt.Add(config.MinNewThreadInterval)
		out.NewThreads.NextAllowedAt = &next
	}
	if t, ok := st.NewThreads[st.Target()]; ok && out.TargetConfigured {
		created := t.CreatedAt
		out.NewThreads.Active = &created
	}
	out.TaskSource = "inline"
	if l.PromptFile != "" {
		out.TaskSource, out.TaskFile = "file", l.PromptFile
	}
	out.Notifications = report.InspectDeliveries(l.StateDir)
	out.Runtime = describeRuntime(ctl, st, in, out.TargetConfigured, now, staleAfter, version)
	for _, u := range unreadable {
		// What the loop is doing cannot be known without its controls, and
		// an empty record would read as "disabled".
		if u.File == control.FileName {
			out.Runtime.State, out.Runtime.Source = "unavailable", "unavailable"
			out.Runtime.Reason = "control.json could not be read"
		}
	}
	queryCtx, cancel := context.WithTimeout(ctx, time.Second)
	out.Daemon = query(queryCtx, l.StateDir, version, false)
	cancel()
	if out.Daemon.State == "responsive" {
		out.Runtime.NextCheck = out.Daemon.NextCheck
	}
	if out.TargetConfigured {
		out.TargetSession = savedSession(l.StateDir, st.Target(), out.TargetAgent.Adapter)
	}
	out.Usage = []usageView{}
	if fb := cfg.FallbackSet; fb != nil {
		reg, err := health.Load(l.StateDir)
		// Readings exist only for probed targets, so they are listed only when
		// fallbacks are configured. Unreadable history shows as unavailable.
		for _, c := range append([]config.Candidate{l.PrimaryCandidate()}, fb.Candidates...) {
			out.Usage = append(out.Usage, usageOf(reg, c, now))
		}
		if err == nil {
			group := l.Group
			if c, ok := fb.Find(st.Target()); ok {
				group = c.Group
			}
			s := health.Evaluate(fb, reg, now, st.Target(), group)
			out.Redundancy = &s
		}
	}
	return out, unreadableError(out.Unreadable)
}

func when(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}

func controlCmd(e env, cfg *config.Config, cmd, note string, takeOver ...bool) int {
	if cmd == "enable" || cmd == "resume" {
		l, err := lock.Acquire(cfg.Loop.StateDir)
		if err != nil {
			return ownershipError(e, err)
		}
		defer l.Release()
		if err := checkTransferSources(cfg, false); err != nil {
			return ownershipError(e, err)
		}
	}
	var completionDeclaredAt time.Time
	fn := map[string]func(*control.File){
		"enable":  func(f *control.File) { f.Enabled = true },
		"disable": func(f *control.File) { f.Enabled = false },
		"pause":   func(f *control.File) { f.Paused = true },
		"resume":  func(f *control.File) { f.Paused, f.Stop, f.AwaitingUser, f.Completed = false, false, false, false },
		"stop":    func(f *control.File) { f.Stop = true },
		"complete": func(f *control.File) {
			if !f.Completed {
				completionDeclaredAt = time.Now().UTC()
			}
			f.Completed = true
		},
		"await-user": func(f *control.File) { f.AwaitingUser = true },
	}[cmd]
	f, err := updateControlWithHistory(cfg.Loop.StateDir, cmd, func(f *control.File) error {
		caller := os.Getenv("BB_THREAD_ID")
		transfer := cmd == "enable" && caller != "" && f.Ownership != nil && f.Ownership.ThreadID != caller
		if cmd == "enable" {
			if err := control.ClaimOwner(f, os.Getenv("BB_THREAD_ID"), len(takeOver) > 0 && takeOver[0]); err != nil {
				return err
			}
		}
		if cmd == "resume" {
			if err := control.CheckOwner(*f, os.Getenv("BB_THREAD_ID"), false); err != nil {
				return err
			}
		}
		if !transfer {
			fn(f)
		} // Transferring ownership preserves disabled too.
		if note != "" {
			f.Note = note
		} else if cmd == "resume" {
			f.Note = ""
		}
		return nil
	}, suppliedRoutine(e.ctx))
	historyFailed := errors.Is(err, errControlHistory)
	if historyFailed {
		e.errf("warning: %v", err)
	}
	if err != nil && !historyFailed {
		e.errf("%v", err)
		if errors.Is(err, lock.ErrHeld) {
			return exitSkipped
		}
		return exitFailed
	}
	if cmd == "resume" {
		// Resume is the operator saying "try again": a fallback or primary
		// that was repaired gets fresh takeover briefs. The tick lock is held.
		if st, err := state.Load(cfg.Loop.StateDir); err != nil {
			e.errf("warning: could not clear rejected takeover briefs: %v", err)
		} else if len(st.HandoffBriefs) > 0 || st.SwitchBackRejected > 0 || st.ChildErrorNoticeAttempts > 0 {
			st.ClearBriefs()
			st.ChildErrorNoticeAttempts = 0 // a child-error notice that was given up is tried again
			if err := state.Save(cfg.Loop.StateDir, st); err != nil {
				e.errf("warning: could not clear rejected takeover briefs: %v", err)
			}
		}
	}
	if err := state.Append(cfg.Loop.StateDir, state.Event{Loop: cfg.Loop.Name, Event: "control", Action: cmd, Reason: f.Summary()}); err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	if !completionDeclaredAt.IsZero() && cfg.Loop.Report != nil && cfg.Loop.Report.ReportsEvent("complete") {
		// Completed was persisted under the control lock before any delivery.
		// Repeated commands (including after an uncertain delivery) cannot replay it.
		event := report.Event{Type: "complete", Loop: cfg.Loop.Name, At: completionDeclaredAt,
			Text: cfg.Loop.Name + ": completion declared; keepgoing has not independently verified success.",
			Data: map[string]any{"basis": "explicit_declaration", "verification": "not_recorded"}}
		if err := reporter(e, cfg).Report(e.ctx, event); err != nil {
			e.errf("warning: completion hold saved; report delivery unconfirmed (no automatic retry): %v", err)
		}
	}
	if cmd == "await-user" {
		text := cfg.Loop.Name + ": needs a person"
		if f.Note != "" {
			text += ": " + f.Note
		}
		if err := reporter(e, cfg).Report(e.ctx, report.Event{Type: "await_user", Loop: cfg.Loop.Name, Text: text, At: time.Now(), Data: map[string]any{"note": f.Note}}); err != nil {
			e.errf("warning: report not delivered: %v", err)
		}
	}
	fmt.Fprintf(e.stdout, "%s: %s\n", cfg.Loop.Name, f.Summary())
	if cmd == "enable" && f.Ownership != nil {
		fmt.Fprintf(e.stdout, "owner: %s (generation %d)\n", f.Ownership.ThreadID, f.Ownership.Generation)
	}
	if historyFailed {
		return exitFailed
	}
	return exitOK
}

// retargetFind looks a created thread up by its generated title. Swapped in tests.
var retargetFind = func(ctx context.Context, cli string, p bb.Place, title string) ([]string, error) {
	return bb.New(config.BB{CLI: cli}).FindByTitle(ctx, p, title)
}

// retarget points the loop at a candidate, or back at the primary, without
// sending anything. The next tick supervises the new target.
func retarget(e env, cfg *config.Config, id, note string, discardPending bool) int {
	if id != state.Primary {
		c, ok := cfg.FallbackSet.Find(id)
		if !ok {
			e.errf("no fallback candidate %q", id)
			return exitUsage
		}
		if c.Disabled {
			e.errf("fallback candidate %q is disabled", id)
			return exitUsage
		}
	}
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		e.errf("a tick is running; try again in a moment")
		return exitSkipped
	}
	if err != nil {
		e.errf("lock: %v", err)
		return exitFailed
	}
	defer l.Release()
	if err := checkOwnership(cfg.Loop.StateDir, nil); err != nil {
		return ownershipError(e, err)
	}
	if err := checkTransferSources(cfg, false); err != nil {
		e.errf("%v; no retarget performed; reload and retry", err)
		return exitSkipped
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	// A thread creation whose outcome is unknown is looked for before the
	// loop is pointed anywhere, so an existing thread is never orphaned
	// unrecorded. Not finding it proves nothing, so giving up on it takes an
	// explicit flag. It stays counted against the cap either way.
	if st.NewThreadIntent == nil && discardPending {
		e.errf("--discard-pending-thread: this loop has no BB thread creation with an unknown outcome")
		return exitUsage
	}
	if in := st.NewThreadIntent; in != nil {
		cli := config.DefaultBBCLI
		if c, ok := cfg.FallbackSet.Find(in.Candidate); ok && c.BB != nil {
			cli = c.BB.CLI
		}
		ids, err := retargetFind(e.ctx, cli, bb.Place{Project: in.Project, Environment: in.Environment}, in.Title)
		const first = "Find it in BB and stop or archive it first: once discarded, keepgoing no longer knows about it, and a later handoff can start another conversation on the same task."
		switch {
		case err == nil && len(ids) == 1:
		case !discardPending && err == nil && len(ids) == 0:
			e.errf("no BB thread titled %q is listed, which does not prove none was created for the handoff: it may have been renamed or archived. No retarget performed. %s Then repeat with --discard-pending-thread.", in.Title, first)
			return exitFailed
		case !discardPending:
			e.errf("a BB thread titled %q may have been created for a handoff, and BB could not say whether exactly one exists; no retarget performed. Check BB and try again. If it cannot be settled that way: %s Then repeat with --discard-pending-thread.", in.Title, first)
			return exitFailed
		}
		switch {
		case err == nil && len(ids) == 1:
			if st.NewThreads == nil {
				st.NewThreads = map[string]state.CreatedThread{}
			}
			st.NewThreads[in.Candidate] = state.CreatedThread{Thread: ids[0], Title: in.Title, CreatedAt: time.Now().UTC(), From: in.From, Continues: in.Continues}
			fmt.Fprintf(e.stdout, "%s: found the BB thread created earlier for %s; it is now on record\n", cfg.Loop.Name, in.Candidate)
		default:
			fmt.Fprintf(e.stdout, "%s: the pending creation of a BB thread titled %q is discarded and stays counted. keepgoing no longer knows about that thread: if it exists and was not stopped or archived, a later handoff can start another conversation on the same task\n", cfg.Loop.Name, in.Title)
		}
		st.NewThreadIntent = nil
	}
	from := st.Target()
	agent := cfg.Loop.Agent
	if c, ok := cfg.FallbackSet.Find(id); ok {
		var known bool
		if agent, known = st.CandidateAgent(c); !known {
			e.errf("fallback candidate %q starts a new BB thread at a handoff and has none yet; retarget never creates a thread", id)
			return exitUsage
		}
	}
	if a, err := factory.New(agent, cfg.Loop.StateDir, id); err == nil {
		if act, ok := a.(adapter.Activator); ok {
			if err := act.Activate(e.ctx); err != nil {
				e.errf("retarget: could not apply %s's model, so the target is unchanged: %v", id, err)
				return exitFailed
			}
		}
	}
	st.ActiveTarget = id
	st.Reprompts, st.LastRepromptAt, st.StopRequestedSent = 0, time.Time{}, false
	st.ClearBriefs()      // an operator's retarget starts a new episode
	st.ClearChildErrors() // children belong to the previous target

	if err := state.Save(cfg.Loop.StateDir, st); err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	state.Append(cfg.Loop.StateDir, state.Event{Loop: cfg.Loop.Name, Event: "retarget", Reason: note, Target: id, Extra: map[string]any{"from": from}})
	fmt.Fprintf(e.stdout, "%s: target %s (was %s)\n", cfg.Loop.Name, id, from)
	// The bounded history keeps hashed aliases only; the note stays in runs.jsonl.
	// Retargeting to the active target moves nothing and is not recorded as a move.
	if from == id {
		return exitOK
	}
	// The target is already saved, so a history failure is reported, not undone.
	if err := incident.Append(cfg.Loop.StateDir, incident.Record{Kind: "retarget", CheckStartedAt: time.Now().UTC(), Build: version,
		TargetKey: incident.TargetKey(from), TargetKeyAfter: incident.TargetKey(id)}); err != nil {
		e.errf("retarget: target saved; incident history write failed; diagnostic evidence is incomplete")
		return exitFailed
	}
	return exitOK
}

// unreadableRecord is a record status could not read, and what to do.
type unreadableRecord struct {
	File    string `json:"file"`
	Problem string `json:"problem"`
	Remedy  string `json:"remedy"`
}

func unreadableControl(err error) unreadableRecord {
	return unreadableRecord{File: control.FileName, Problem: recordProblem(err, control.FileName),
		Remedy: "Checks send nothing, and pause, stop and resume cannot be saved, until it is repaired. Stop the daemon, service or scheduler, then restore control.json from a copy of the state directory, or move it aside: a loop with no control.json is disabled, and the next enable, pause or stop writes a new one, without the earlier holds, note and owner."}
}

func unreadableState(err error) unreadableRecord {
	return unreadableRecord{File: state.FileName, Problem: recordProblem(err, state.FileName),
		Remedy: "Checks fail and send nothing until it is repaired. Stop the daemon, service or scheduler, then restore state.json from a copy of the state directory, or move it aside: the next check starts a new one, without the re-prompt and nudge counts, the active target or the record of the last check and send."}
}

// recordProblem is the error without the file name it usually begins with.
func recordProblem(err error, file string) string {
	return strings.TrimPrefix(err.Error(), file+": ")
}

func unreadableError(u []unreadableRecord) error {
	if len(u) == 0 {
		return nil
	}
	names := make([]string, len(u))
	for i, r := range u {
		names[i] = r.File
	}
	return fmt.Errorf("unreadable records: %s", strings.Join(names, ", "))
}
