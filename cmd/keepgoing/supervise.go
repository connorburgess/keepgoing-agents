package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/factory"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
	"github.com/connorburgess/keepgoing-agents/internal/window"
)

func validate(e env, cfg *config.Config, path string, takeOver ...bool) int {
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
	if err := cfg.CheckSources(); err != nil {
		e.errf("%v; no tick performed; reload and retry", err)
		return exitSkipped
	}
	opts := supervise.Options{Version: version, DryRun: dryRun, Reporter: reporter(e, cfg)}
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
			if now.After(deadline) || controlSignature(cfg.Loop.StateDir) != sig {
				return true
			}
			if pollWake(ctx, cfg, leases...) || controlSignature(cfg.Loop.StateDir) != sig {
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
	if cfg.CheckSources() != nil {
		return false // Wait for the next ordinary reload; never poll the stale target.
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Loop.Daemon.ControlPoll.Duration)
	defer cancel()
	return supervise.PollWake(ctx, cfg, nil)
}

func controlSignature(stateDir string) string {
	info, err := os.Stat(filepath.Join(stateDir, control.FileName))
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}

type statusOut struct {
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
	Deadline          string                   `json:"deadline,omitempty"`
	DeadlineReached   bool                     `json:"deadlineReached"`
	Window            string                   `json:"window"`
	InWindow          bool                     `json:"inWindow"`
	State             state.File               `json:"state"`
	Redundancy        *health.Summary          `json:"redundancy,omitempty"`
	StateDir          string                   `json:"stateDir"`
	Version           string                   `json:"version"`
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
	case a.Claude != nil:
		r.Workdir = a.Claude.Workdir
	case a.Codex != nil:
		r.Workdir = a.Codex.Workdir
	}
	return r
}

func status(e env, cfg *config.Config, now time.Time, asJSON bool, staleAfter time.Duration) int {
	l := cfg.Loop
	ctl, err := control.Load(l.StateDir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	st, err := state.Load(l.StateDir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	in, why, err := window.Inside(l.Window, now)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	out := statusOut{Loop: l.Name, Primary: l.Describe(), Target: st.Target(), Control: ctl, ControlState: ctl.Summary(), Window: why, InWindow: in, State: st, StateDir: l.StateDir, Version: version}
	if g, err := control.LoadGoal(l.StateDir); err != nil {
		out.GoalError = err.Error()
	} else if g != nil {
		summary := g.Summary()
		out.Goal = &summary
	}
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
	out.PrimaryAgent = refOf(l.Agent)
	if st.Target() == state.Primary {
		out.TargetAgent, out.TargetConfigured = refOf(l.Agent), true
	} else if c, ok := cfg.FallbackSet.Find(st.Target()); ok && !c.Disabled {
		out.TargetAgent = refOf(c.Agent)
		out.TargetConfigured = true
	}
	out.TaskSource = "inline"
	if l.PromptFile != "" {
		out.TaskSource, out.TaskFile = "file", l.PromptFile
	}
	out.Notifications = report.InspectDeliveries(l.StateDir)
	out.Runtime = describeRuntime(ctl, st, in, out.TargetConfigured, now, staleAfter, version)
	queryCtx, cancel := context.WithTimeout(e.ctx, time.Second)
	out.Daemon = inspectDaemon(queryCtx, l.StateDir, version, false)
	cancel()
	if out.Daemon.State == "responsive" {
		out.Runtime.NextCheck = out.Daemon.NextCheck
	}
	if out.TargetConfigured {
		out.TargetSession = savedSession(l.StateDir, st.Target(), out.TargetAgent.Adapter)
	}
	if fb := cfg.FallbackSet; fb != nil {
		reg, err := health.Load(l.StateDir)
		if err == nil {
			group := l.Group
			if c, ok := fb.Find(st.Target()); ok {
				group = c.Group
			}
			s := health.Evaluate(fb, reg, now, st.Target(), group)
			out.Redundancy = &s
		}
	}
	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
		return exitOK
	}
	w := e.stdout
	fmt.Fprintf(w, "loop:      %s\n", out.Loop)
	if out.Goal != nil {
		printGoal(e, *out.Goal, false)
	}
	if out.GoalError != "" {
		fmt.Fprintf(w, "goal: unavailable (%s)\n", out.GoalError)
	}
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
		if c.DryRun {
			fmt.Fprintln(w, "dry run:   true; the decision was not executed")
		}
	} else {
		fmt.Fprintln(w, "decision:  unavailable; no detailed check record")
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
	fmt.Fprintf(w, "state dir: %s\n", out.StateDir)
	return exitOK
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
	})
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

// retarget points the loop at a candidate, or back at the primary, without
// sending anything. The next tick supervises the new target.
func retarget(e env, cfg *config.Config, id, note string) int {
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
	if err := cfg.CheckSources(); err != nil {
		e.errf("%v; no retarget performed; reload and retry", err)
		return exitSkipped
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	from := st.Target()
	agent := cfg.Loop.Agent
	if c, ok := cfg.FallbackSet.Find(id); ok {
		agent = c.Agent
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
	if err := state.Save(cfg.Loop.StateDir, st); err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	state.Append(cfg.Loop.StateDir, state.Event{Loop: cfg.Loop.Name, Event: "retarget", Reason: note, Target: id, Extra: map[string]any{"from": from}})
	fmt.Fprintf(e.stdout, "%s: target %s (was %s)\n", cfg.Loop.Name, id, from)
	return exitOK
}
