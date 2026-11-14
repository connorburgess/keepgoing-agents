package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/window"
)

var clientRegistrationID = regexp.MustCompile(`^loop_[0-9a-f]{32}$`)

type clientStatusReport struct {
	SchemaVersion  int             `json:"schemaVersion"`
	RegistrationID string          `json:"registrationId"`
	ReadAt         time.Time       `json:"readAt"`
	Outcome        string          `json:"outcome"` // available, partial, unavailable; never execution health
	Issues         []string        `json:"issues"`
	Snapshot       *clientSnapshot `json:"snapshot,omitempty"`
}
type clientSnapshot struct {
	Loop               string               `json:"loop"`
	Controls           *clientControls      `json:"controls,omitempty"`
	ControlState       string               `json:"controlState,omitempty"`
	CompletionEvidence string               `json:"completionEvidence"`
	Target             *clientTarget        `json:"target,omitempty"`
	Runtime            *clientRuntime       `json:"runtime,omitempty"`
	LastCheck          *clientCheck         `json:"lastCheck,omitempty"`
	GoalState          string               `json:"goalState"`
	Goal               *control.GoalSummary `json:"goal,omitempty"`
	PrivateGoal        *clientGoalDisplay   `json:"privateGoal,omitempty"`
	Daemon             daemonStatus         `json:"daemon"`
	ExternalScheduler  string               `json:"externalScheduler"`
}
type clientControls struct {
	Enabled      bool `json:"enabled"`
	Paused       bool `json:"paused"`
	Stop         bool `json:"stop"`
	AwaitingUser bool `json:"awaitingUser"`
	Completed    bool `json:"completed"`
}
type clientTarget struct {
	Alias            string `json:"alias"`
	Adapter          string `json:"adapter"`
	BBThread         string `json:"bbThread,omitempty"`
	IdentityEvidence string `json:"identityEvidence"`
}
type clientRuntime struct {
	State            string     `json:"state"`
	Source           string     `json:"source"`
	Freshness        string     `json:"freshness"`
	StaleAfter       string     `json:"staleAfter"`
	BinaryComparison string     `json:"binaryComparison"`
	CheckedAt        *time.Time `json:"checkedAt,omitempty"`
	// Overdue is the overdue rule's verdict (see state.ScheduleOf), with
	// what to say about it.
	Overdue        bool   `json:"overdue"`
	OverdueMessage string `json:"overdueMessage,omitempty"`
}
type clientCheck struct {
	At      *time.Time `json:"at,omitempty"`
	Target  string     `json:"target"`
	Action  string     `json:"action"`
	Outcome string     `json:"outcome"`
	DryRun  bool       `json:"dryRun"`
}
type clientGoalDisplay struct {
	Title      string `json:"title"`
	NextAction string `json:"nextAction,omitempty"`
}

type clientDaemonQuery func(context.Context, string, string, bool) daemonStatus

func clientCommand(e env, args []string) int {
	if len(args) > 0 && (args[0] == "prepare-resume" || args[0] == "resume") {
		return clientResumeCommand(e, args)
	}
	if len(args) > 0 && (args[0] == "pause" || args[0] == "stop") {
		return clientHoldCommand(e, args)
	}
	if len(args) > 0 && (args[0] == "review" || args[0] == "apply") {
		return clientReviewCommand(e, args)
	}
	if len(args) == 0 || args[0] != "status" {
		e.errf("usage: keepgoing client status|pause|stop|prepare-resume|resume --id ID, or client review|apply pause|stop|resume --loop KEY [--directory private-registry] [--json]; see each command --help")
		return exitUsage
	}
	fs := flag.NewFlagSet("client status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "registration ID")
	directory := fs.String("directory", "", "private registry directory")
	asJSON := fs.Bool("json", false, "print schema-versioned status")
	private := fs.Bool("private", false, "include current private goal title and next-action summary")
	offline := fs.Bool("offline", false, "skip the local daemon challenge")
	staleAfter := fs.Duration("stale-after", defaultStaleAfter, "recorded-check freshness threshold")
	client := fs.String("client", "", "the calling program's NAME@VERSION (bb-plugin, macos-menu), recorded for the panel's Versions view")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("client: invalid arguments; see client status --help")
		return exitUsage
	}
	if fs.NArg() != 0 || !clientRegistrationID.MatchString(*id) || *staleAfter <= 0 {
		e.errf("client: provide a registration ID and positive stale-after duration; no positional arguments")
		return exitUsage
	}
	report := readClientStatus(e.ctx, *directory, *id, *private, *offline, *staleAfter, time.Now().UTC(), inspectDaemon)
	defer noteClient(*client, "")
	if *asJSON {
		if json.NewEncoder(e.stdout).Encode(report) != nil {
			return exitFailed
		}
	} else {
		fmt.Fprintf(e.stdout, "Registration %s: %s (read %s; independent snapshots)\n", report.RegistrationID, report.Outcome, report.ReadAt.Format(time.RFC3339))
		if s := report.Snapshot; s != nil {
			fmt.Fprintf(e.stdout, "loop: %s\ncontrol: %s\ncompletion evidence: %s\n", s.Loop, s.ControlState, s.CompletionEvidence)
			if s.Controls != nil {
				fmt.Fprintf(e.stdout, "holds: paused=%t stop=%t awaiting-user=%t completed=%t\n", s.Controls.Paused, s.Controls.Stop, s.Controls.AwaitingUser, s.Controls.Completed)
			}
			if s.Target != nil {
				fmt.Fprintf(e.stdout, "target: %s (%s) %s; configuration only\n", s.Target.Alias, s.Target.Adapter, s.Target.BBThread)
			}
			if s.Runtime != nil {
				fmt.Fprintf(e.stdout, "check evidence: %s; %s; source %s\n", s.Runtime.State, s.Runtime.Freshness, s.Runtime.Source)
			}
			if s.LastCheck != nil {
				fmt.Fprintf(e.stdout, "last recorded action: %s; %s; target %s; dry-run=%t\n", s.LastCheck.Action, s.LastCheck.Outcome, s.LastCheck.Target, s.LastCheck.DryRun)
			}
			fmt.Fprintf(e.stdout, "goal: %s\n", s.GoalState)
			if s.Goal != nil {
				printGoal(e, *s.Goal, false)
			}
			if s.PrivateGoal != nil {
				fmt.Fprintf(e.stdout, "private goal title: %q\nprivate next action: %q\n", s.PrivateGoal.Title, s.PrivateGoal.NextAction)
			}
			fmt.Fprintf(e.stdout, "daemon: %s; %s\nexternal scheduler: %s\n", s.Daemon.State, s.Daemon.Message, s.ExternalScheduler)
		}
		for _, issue := range report.Issues {
			fmt.Fprintf(e.stdout, "unavailable evidence: %s\n", issue)
		}
	}
	if report.Outcome != "available" {
		return exitFailed
	}
	return exitOK
}

func clientTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
func clientToken(value string, allowed ...string) string {
	for _, v := range allowed {
		if value == v {
			return v
		}
	}
	return "unknown"
}
func clientEntry(dir, id string) (registry.Entry, bool) {
	f, err := registry.Load(dir)
	if err != nil {
		return registry.Entry{}, false
	}
	for _, entry := range f.Entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return registry.Entry{}, false
}

// A registration is a name/runtime binding, not an authority or an observation
// of a live target. Retain config spelling to preserve relative path semantics.
func clientConfig(entry registry.Entry) (*config.Config, bool) {
	info, err := os.Lstat(entry.ConfigPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, false
	}
	cfg, err := config.Load(entry.ConfigPath)
	if err != nil || cfg.Loop.Name != entry.Loop {
		return nil, false
	}
	dir, err := canonicalFuturePath(cfg.Loop.StateDir)
	if err != nil || dir != entry.StateDir {
		return nil, false
	}
	return cfg, true
}
func clientAlias(cfg *config.Config, alias string) string {
	if alias == state.Primary {
		return alias
	}
	if c, ok := currentRoutes(cfg).Find(alias); ok && !c.Disabled {
		return c.ID
	}
	return "unavailable"
}
func readClientStatus(ctx context.Context, directory, id string, private, offline bool, staleAfter time.Duration, now time.Time, query clientDaemonQuery) clientStatusReport {
	r := clientStatusReport{SchemaVersion: 1, RegistrationID: id, ReadAt: now, Outcome: "unavailable", Issues: []string{}}
	var scheduleOf func(*daemonStatus) scheduleStatus // set once the loop's records are read
	unavailable := func(issue string) clientStatusReport {
		r.Outcome = "unavailable"
		r.Snapshot = nil
		r.Issues = []string{issue}
		return r
	}
	dir, err := registryDirectory(directory)
	if err != nil {
		return unavailable("registry_unavailable")
	}
	entry, ok := clientEntry(dir, id)
	if !ok {
		return unavailable("registration_unavailable")
	}
	cfg, ok := clientConfig(entry)
	if !ok {
		return unavailable("binding_unavailable")
	}
	if ctx.Err() != nil {
		return unavailable("cancelled")
	}
	s := &clientSnapshot{Loop: entry.Loop, CompletionEvidence: "declaration_only", GoalState: "not_recorded", ExternalScheduler: "not_inspected"}
	r.Snapshot = s
	r.Outcome = "available"
	issue := func(code string) { r.Issues = append(r.Issues, code); r.Outcome = "partial" }
	var ctl control.File
	controlOK := readTransferRecord(entry.StateDir, control.FileName, &ctl) == nil && control.CheckOwner(ctl, "", false) == nil
	if !controlOK {
		issue("control_unavailable")
		s.CompletionEvidence = "unavailable"
	} else {
		if ctl.PendingTransfer != nil {
			return unavailable("transfer_pending")
		}
		if ctl.BBTarget != "" && (cfg.Loop.BB == nil || ctl.BBTarget != cfg.Loop.BB.Thread || !transferThreadID.MatchString(ctl.BBTarget)) {
			return unavailable("target_binding_mismatch")
		}
		s.Controls = &clientControls{ctl.Enabled, ctl.Paused, ctl.Stop, ctl.AwaitingUser, ctl.Completed}
		s.ControlState = ctl.Summary()
	}
	var st state.File
	stateOK := readTransferRecord(entry.StateDir, state.FileName, &st) == nil
	if !stateOK {
		issue("state_unavailable")
	} else {
		var agent config.Agent
		configured := false
		if st.Target() == state.Primary {
			agent = cfg.Loop.Agent
			configured = true
		} else if c, ok := effectiveRoutes(cfg, st).Find(st.Target()); ok && !c.Disabled {
			agent, configured = st.CandidateAgent(c)
		}
		if configured {
			target := &clientTarget{Alias: st.Target(), Adapter: agent.Adapter, IdentityEvidence: "configuration_only"}
			if agent.BB != nil {
				if transferThreadID.MatchString(agent.BB.Thread) {
					target.BBThread = agent.BB.Thread
				} else {
					configured = false
				}
			}
			if configured {
				s.Target = target
			}
		}
		if !configured {
			issue("target_unavailable")
		}
		if controlOK {
			in, _, err := window.Inside(cfg.Loop.Window, now)
			if err != nil {
				issue("window_unavailable")
			} else {
				rt := describeRuntime(ctl, st, in, configured, now, staleAfter, version)
				s.Runtime = &clientRuntime{rt.State, rt.Source, rt.Freshness, rt.StaleAfter, rt.BinaryComparison, clientTime(st.CheckedAt), false, ""}
				scheduleOf = func(d *daemonStatus) scheduleStatus { return describeSchedule(&cfg.Loop, st, ctl, d, now) }
			}
		}
		if c := st.LastCheck; c != nil {
			s.LastCheck = &clientCheck{At: clientTime(c.At), Target: clientAlias(cfg, c.Target), Action: clientToken(c.Action, "none", "wake", "nudge", "reprompt", "await_user", "stop", "handoff", "switch_back"), Outcome: clientToken(c.Outcome, "succeeded", "failed", "dry_run"), DryRun: c.DryRun}
		}
	}
	if goal, err := control.LoadGoal(entry.StateDir); err != nil || (goal != nil && goal.OwningLoop != entry.Loop) {
		issue("goal_unavailable")
		s.GoalState = "unavailable"
	} else if goal != nil {
		summary := goal.Summary()
		s.Goal = &summary
		s.GoalState = "recorded"
		if private {
			spec := goal.Revisions[len(goal.Revisions)-1].Spec
			s.PrivateGoal = &clientGoalDisplay{Title: spec.Title}
			if spec.NextAction != nil {
				s.PrivateGoal.NextAction = spec.NextAction.Summary
			}
		}
	}
	// Shared authenticated diagnostics only: never adapter commands, BB queries,
	// scheduler queries, task files, output logs or provider session files.
	queryCtx, cancel := context.WithTimeout(ctx, time.Second)
	s.Daemon = query(queryCtx, entry.StateDir, version, offline)
	// After the daemon is asked: one that ran the last check and is still
	// running means the computer slept.
	if scheduleOf != nil && s.Runtime != nil {
		sch := scheduleOf(&s.Daemon)
		s.Runtime.Overdue, s.Runtime.OverdueMessage = sch.Overdue, sch.Message
	}
	cancel()
	if ctx.Err() != nil {
		return unavailable("cancelled")
	}
	// Recheck binding and source files after the slow read. This detects changes,
	// not a transaction or an authorization token for a later mutation.
	current, ok := clientEntry(dir, id)
	if !ok || current != entry {
		return unavailable("registration_changed")
	}
	fresh, ok := clientConfig(entry)
	if !ok || cfg.CheckSources() != nil || fresh.CheckSources() != nil {
		return unavailable("binding_changed")
	}
	return r
}
