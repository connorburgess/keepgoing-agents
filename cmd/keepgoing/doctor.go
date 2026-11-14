package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/netretry"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

// Diagnostics never return raw command output, environment values, task text,
// control notes, or parser errors. Only known fields become report entries.
type doctorCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // ok, warning, error, unavailable
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

type doctorReport struct {
	Notifications *report.DeliveryStatus    `json:"notifications,omitempty"`
	SchemaVersion int                       `json:"schemaVersion"`
	Version       string                    `json:"version"`
	Loop          string                    `json:"loop,omitempty"`
	Target        string                    `json:"target,omitempty"`
	TargetAgent   *agentRef                 `json:"targetAgent,omitempty"`
	QuotaGroup    string                    `json:"quotaGroup,omitempty"`
	ControlState  string                    `json:"controlState,omitempty"`
	CheckedAt     time.Time                 `json:"checkedAt"`
	LastCheckAt   *time.Time                `json:"lastCheckAt,omitempty"`
	StaleAfter    string                    `json:"staleAfter"`
	NextCheck     string                    `json:"nextCheck"`
	Checks        []doctorCheck             `json:"checks"`
	Sessions      map[string]*sessionStatus `json:"sessions,omitempty"`
	Daemon        *daemonStatus             `json:"daemon,omitempty"`
	BBAutomation  *bbAutomationStatus       `json:"bbAutomation,omitempty"`
}

func (r *doctorReport) add(id, status, message, fix string) {
	r.Checks = append(r.Checks, doctorCheck{id, status, message, fix})
}

type doctorRun func(context.Context, string, []string, map[string]string, string) ([]byte, error)

func doctor(e env, args []string, run doctorRun) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	path := fs.String("c", config.DefaultPath(), "path to the config file")
	asJSON := fs.Bool("json", false, "print the versioned diagnostic report")
	offline := fs.Bool("offline", false, "inspect local files only; do not execute CLIs")
	timeout := fs.Duration("timeout", 5*time.Second, "timeout per read-only CLI command")
	stale := fs.Duration("stale-after", defaultStaleAfter, "warn when the last check is older; set above your scheduler interval")
	bbAutomation := fs.String("bb-automation", "", "inspect an existing BB script automation ID (requires --bb-project)")
	bbProject := fs.String("bb-project", "", "project owning the selected BB automation")
	bbCLI := fs.String("bb-cli", "bb", "BB executable for the selected automation query")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() != 0 || *timeout <= 0 || *stale <= 0 {
		e.errf("doctor: no positional arguments; timeout and stale-after must be positive")
		return exitUsage
	}
	if !validAutomationSelection(*bbAutomation, *bbProject) || (*bbAutomation == "" && *bbCLI != "bb") {
		e.errf("doctor: --bb-automation and --bb-project require valid BB IDs together; --bb-cli requires that selection")
		return exitUsage
	}
	r := doctorReport{SchemaVersion: 1, Version: version, CheckedAt: time.Now().UTC(), StaleAfter: stale.String(), NextCheck: unknownNextCheck, Checks: []doctorCheck{}}
	cfg, err := config.Load(*path)
	if errors.Is(err, config.ErrDefaultAmbiguous) {
		r.add("config", "error", "No -c was given, and both "+config.FileName+" and "+config.LegacyFileName+" are in this directory, so which loop is meant cannot be told.", "Pass -c with the one you mean.")
		doctorVariables(&r)
		doctorUserDirectory(&r, userdir.Look())
		return printDoctor(e, r, *asJSON)
	}
	if err != nil {
		r.add("config", "error", "Configuration or fallbacks could not be read or validated.", "Check the selected config path and YAML against the configuration guide. Raw parser errors are withheld to protect values.")
		// What belongs to the user and to no loop is reported all the same.
		doctorVariables(&r)
		doctorUserDirectory(&r, userdir.Look())
		return printDoctor(e, r, *asJSON)
	}
	r.Loop = cfg.Loop.Name
	r.add("config", "ok", "Configuration and configured fallbacks validate.", "")
	// Timing probes are paid requests the owner turned on; doctor says what
	// they are for, what they cost and where their readings come from.
	if ts := cfg.TimingSummary(); ts != nil {
		r.add("timing", "ok", ts.Collecting+". "+ts.Cost+".", "Set loop.timing.enabled to false to stop these probes.")
		for i, w := range ts.Warnings {
			fix := "Check that this computer's CLI is signed in to the account BB uses for this thread; the readings describe whichever account the CLI uses."
			if strings.Contains(w, "scheduled_disabled") {
				fix = "Remove probe.scheduled_disabled from the fallbacks file, or turn timing off."
			}
			r.add(fmt.Sprintf("timing.warning.%d", i+1), "warning", w+".", fix)
		}
	}
	// The variables move what every command reads, so doctor says where.
	// (Two names that disagree are reported by doctorVariables instead: no
	// registry is used then.)
	if dir, name, err := envvar.RegistryDir.Path(); err == nil && dir != "" {
		r.add("registry.directory", "ok", name+" is set: commands use the registry in "+dir+" when no --directory is given.", "Unset it to use the default registry.")
	}
	doctorVariables(&r)
	doctorUserDirectory(&r, userdir.Look())
	// A check claims its session before acting on it. Doctor says what is
	// wrong with the claim directory, in terms of its real cause.
	if status, message, fix := sessionDirectoryCheck(); status != "" {
		r.add("session.directory", status, message, fix)
	}
	doctorFile(&r, "config.permissions", *path, false)
	if cfg.Loop.Fallbacks != "" {
		doctorFile(&r, "fallbacks.permissions", cfg.Loop.Fallbacks, false)
	}
	if cfg.Loop.PromptFile != "" {
		doctorFile(&r, "task", cfg.Loop.PromptFile, false)
	} else {
		r.add("task", "ok", "Task is configured inline; its text is not displayed.", "")
	}
	doctorFile(&r, "state.directory", cfg.Loop.StateDir, true)
	doctorStateAccess(&r, cfg.Loop.StateDir, doctorOSAccess)
	for _, name := range []string{control.FileName, state.FileName, "health.json", state.LogName, daemoninfo.FileName, report.DeliveryFile, "delivery.lock"} {
		p := filepath.Join(cfg.Loop.StateDir, name)
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			doctorFile(&r, "state."+name, p, false)
		}
	}
	notifications := report.InspectDeliveries(cfg.Loop.StateDir)
	r.Notifications = &notifications
	doctorDeliveries(&r, notifications)
	ctl, ctlErr := control.Load(cfg.Loop.StateDir)
	if ctlErr != nil {
		r.add("control", "error", "Control state could not be read.", "Repair control.json before enabling supervision; do not discard an existing stop or pause.")
	} else {
		if ctl.PendingTransfer != nil {
			r.add("transfer", "error", "BB transfer is pending; automatic work and resume are held.", "Inspect status for the transfer ID, then use transfer recover. Do not discard the recovery record.")
		} else if ctl.BBTarget != "" && (cfg.Loop.BB == nil || cfg.Loop.BB.Thread != ctl.BBTarget) {
			r.add("transfer", "error", "Configuration does not match the transferred BB target.", "Select the current loop configuration.")
		}
		r.ControlState = ctl.Summary()
		r.add("control", "ok", "Control state: "+r.ControlState+".", "")
	}
	st, stateErr := state.Load(cfg.Loop.StateDir)
	if stateErr != nil {
		r.add("state", "error", "Tick state could not be read.", "Inspect or restore state.json; preserve the active session and control file.")
	} else {
		doctorHeartbeat(&r, st, ctl, ctlErr == nil, *stale)
	}
	if cfg.FallbackSet != nil {
		if _, err := health.Load(cfg.Loop.StateDir); err != nil {
			r.add("health", "error", "Recorded fallback health could not be read.", "Inspect health.json before relying on fallbacks.")
		}
	}
	if stateErr == nil {
		r.Target = st.Target()
		a := cfg.Loop.Agent
		valid := true
		if r.Target != state.Primary {
			candidate, ok := effectiveRoutes(cfg, st).Find(r.Target)
			if !ok || candidate.Disabled {
				valid = false
				r.add("target", "error", "Recorded active target is missing or disabled in the fallback configuration.", "Review the intended target and use retarget explicitly before resuming.")
			} else if created, known := st.CandidateAgent(candidate); !known {
				valid = false
				r.add("target", "error", "Recorded active target starts a new BB thread at handoff, and no created thread is recorded for it.", "Retarget to the primary or another fallback before resuming.")
			} else {
				a = created
			}
		}
		if valid {
			ref := refOf(a)
			r.TargetAgent = &ref
			r.QuotaGroup = a.Group
		}
	}
	if current := os.Getenv("BB_THREAD_ID"); current != "" && cfg.Loop.BB != nil && current != cfg.Loop.BB.Thread {
		r.add("target.current_thread", "warning", "The configured primary is a different BB conversation from this caller.", "Review the configured thread ID; this command does not transfer supervision.")
	}
	checkAgent := func(id string, a config.Agent) {
		if s := savedSession(cfg.Loop.StateDir, id, a.Adapter); s != nil {
			if r.Sessions == nil {
				r.Sessions = make(map[string]*sessionStatus)
			}
			r.Sessions[id] = s
			status := "unavailable"
			if s.State == "saved" {
				status = "ok"
			} else if s.State == "unavailable" {
				status = "warning"
			}
			r.add("agent."+id+".session", status, s.Message, "")
		}
		doctorAgent(e.ctx, &r, id, a, *offline, *timeout, run)
		if a.Custom != nil {
			// A defined agent's own mapping decides what moves work to a
			// fallback, so every such mapping is said in words.
			if moves := a.Custom.FailoverMappings(cfg.Loop.Errors.FailoverOn); len(moves) > 0 {
				r.add("agent."+id+".failover", "warning", "This definition decides what moves work to a fallback: "+strings.Join(moves, "; ")+".", "Map only a real provider limit to a class the loop fails over on. Leave a refusal, a policy block or an account restriction unmapped: it is then unknown, which never moves work.")
			} else {
				r.add("agent."+id+".failover", "ok", "Nothing in this definition maps the agent's output to a class the loop fails over on.", "")
			}
		}
	}
	checkAgent("primary", cfg.Loop.Agent)
	// The effective routes: for a loop that follows the standard list, a
	// current route that is no longer in it is checked too.
	if fb := currentRoutes(cfg); fb != nil {
		for _, c := range fb.Candidates {
			if c.Disabled {
				continue
			}
			checkAgent(c.ID, c.Agent)
		}
	}
	queryCtx, cancel := context.WithTimeout(e.ctx, *timeout)
	d := inspectDaemon(queryCtx, cfg.Loop.StateDir, version, *offline)
	cancel()
	r.Daemon, r.NextCheck = &d, d.NextCheck
	schedulerStatus, buildStatus := "unavailable", "unavailable"
	if d.State == "responsive" {
		schedulerStatus = "ok"
	}
	if d.BinaryComparison == "match" {
		buildStatus = "ok"
	} else if d.BinaryComparison == "different" {
		buildStatus = "warning"
	}
	r.add("scheduler", schedulerStatus, d.Message, "Service installation and external schedulers are not verified; a responding daemon can still be waiting on a stuck check.")
	r.add("supervisor.live_version", buildStatus, "Live daemon build comparison: "+d.BinaryComparison+".", "A different build label does not establish which is newer; empty and dev labels cannot identify a build.")
	if *bbAutomation != "" {
		s := inspectBBAutomation(e.ctx, *bbCLI, *bbAutomation, *bbProject, *offline, *timeout, run)
		r.BBAutomation = &s
		doctorAutomationResult(&r, s)
	}
	doctorCheckVersion(&r, st.LastCheck, stateErr == nil)
	doctorFailedChecks(&r, st, stateErr == nil)
	if stateErr == nil {
		if ctl, err := control.Load(cfg.Loop.StateDir); err == nil {
			if level, text, fix, ok := scheduleDoctorLine(describeSchedule(&cfg.Loop, st, ctl, &d, r.CheckedAt)); ok {
				r.add("supervisor.schedule", level, text, fix)
			}
		}
	}
	if _, err := state.LoadLedger(cfg.Loop.StateDir); err != nil {
		r.add("supervisor.send_record", "error", "The send record sends.json cannot be read, so no check sends anything.", "Restore sends.json from a copy. Deleting it loses the record of sends not yet counted, which may then be repeated, and resets the count against loop.limits.max_sends.")
	} else {
		r.add("supervisor.send_record", "ok", "The send record sends.json is readable.", "")
	}
	if stateErr == nil && st.StartRefused != nil {
		r.add("runner.wrapper", "error", fmt.Sprintf("No turn can be started, since %s: %s.", st.StartRefused.Since.Format(time.RFC3339), st.StartRefused.Reason), "Stop the daemon, make sure the keepgoing binary at its path is the one you mean to run, and start the daemon again. Nothing is sent or counted until then.")
	}
	doctorTickBudget(&r, cfg.Loop)
	doctorNetworkCheck(e.ctx, &r, cfg.Loop, st, *offline)
	r.add("probes", "unavailable", "No model requests or paid probes were made. Login status does not establish credential validity, model access, or quota health.", "Preview with fallbacks check --dry-run. Run fallbacks check without --dry-run only when you intend paid probes; manual probes are outside the scheduled daily cap.")
	return printDoctor(e, r, *asJSON)
}

func doctorCheckVersion(r *doctorReport, check *state.Check, stateReadable bool) {
	comparison := "unknown"
	if stateReadable {
		comparison = compareCheckVersion(check, r.Version)
	}
	switch comparison {
	case "match":
		r.add("supervisor.version", "ok", "The last recorded tick used this build label. This does not verify the currently running scheduler's binary.", "")
	case "different":
		r.add("supervisor.version", "warning", "The last recorded tick used a different build label; the scheduler may still use another binary.", "Compare the scheduler's executable with this installation; inspect a new tick before deciding whether the mismatch persists.")
	default:
		r.add("supervisor.version", "unavailable", "Comparable tick build history is unavailable; an empty or dev label cannot identify a build.", "Record a tick from a versioned binary, then compare it with the scheduler's executable.")
	}
}

// doctorTickBudget shows the check's time budget, says when an omitted
// tick_timeout was raised to fit the probe timeout, and warns when a slow
// check may outlast a five-minute scheduler timeout.
func doctorTickBudget(r *doctorReport, l config.Loop) {
	message := fmt.Sprintf("One check's optional work is budgeted at %s.", l.TickTimeout.Duration)
	if l.TickTimeoutGrown() {
		message = fmt.Sprintf("One check's optional work is budgeted at %s: tick_timeout is not set and was raised from the %s default to fit fallbacks probe.timeout.", l.TickTimeout.Duration, config.DefaultTickTimeout)
	}
	if w := l.TickTimeoutWarning(); w != "" {
		r.add("supervisor.tick_timeout", "warning", message+" A slow check may outlast a five-minute scheduler timeout.", "With an external scheduler, raise its timeout or lower fallbacks probe.timeout; the daemon and managed services have no such timeout.")
		return
	}
	r.add("supervisor.tick_timeout", "ok", message, "")
}

// doctorNetworkCheck runs the loop's connectivity check once, so a target
// that this machine cannot reach is found before it makes a loop wait.
func doctorNetworkCheck(ctx context.Context, r *doctorReport, l config.Loop, st state.File, offline bool) {
	timeout := l.NetworkCheckTimeout()
	switch {
	case !l.Network.Enabled():
		r.add("network.check", "ok", "The connectivity check is off; re-prompts, handoffs and probes are attempted whatever the network is doing.", "")
		return
	case timeout == 0:
		r.add("network.check", "warning", l.NetworkCheckWarning(), "")
		return
	}
	if offline {
		r.add("network.check", "unavailable", "The connectivity check was not tested because --offline was given.", "Run doctor without --offline to test it.")
		return
	}
	target := "the default target"
	switch {
	case len(l.Network.Command) > 0:
		target = "the configured command"
	case l.Network.URL != "":
		target = "the configured URL"
	}
	found := netretry.Probe{URL: l.Network.URL, Command: l.Network.Command, Timeout: timeout}.Check(ctx)
	switch found {
	case netretry.Reached:
		r.add("network.check", "ok", fmt.Sprintf("The connectivity check reached %s from this process.", target), "")
		return
	case netretry.Redirected, netretry.SignInNeeded:
		what := "a redirect"
		if found == netretry.SignInNeeded {
			what = "511 Network Authentication Required"
		}
		r.add("network.check", "warning", fmt.Sprintf("The connectivity check got %s from %s, which it reads as offline: a captive portal answers that way. While it does, re-prompts, handoffs, nudges and fallback probes wait.", what, target),
			"If this network needs a sign-in, complete it. If the target itself always redirects, point loop.network.url at an address that answers directly (redirects are never followed), or set network: {check: \"off\"}.")
		return
	}
	fix := "If the network is working, this machine or this environment cannot reach the check's target (a proxy, a DNS filter, a TLS-inspecting gateway). Point loop.network at a url or command that reflects your provider's reachability, or set network: {check: \"off\"}."
	message := fmt.Sprintf("The connectivity check could not reach %s from this process; while it fails, re-prompts, handoffs and fallback probes wait.", target)
	if st.NetworkDistrusted {
		message = fmt.Sprintf("The connectivity check could not reach %s, while the provider answered a probe: the loop has stopped believing the check for the current outage.", target)
	}
	r.add("network.check", "warning", message, fix)
}

// doctorFailedChecks reports consecutive failed checks. A check that could
// not observe or act still exits 0, so this is where a persistent problem,
// such as a broken provider CLI, shows up.
func doctorFailedChecks(r *doctorReport, st state.File, stateReadable bool) {
	switch {
	case !stateReadable:
		r.add("supervisor.failed_checks", "unavailable", "Check history is unavailable.", "")
	case st.FailedChecks == 0:
		r.add("supervisor.failed_checks", "ok", "The last recorded check completed.", "")
	default:
		r.add("supervisor.failed_checks", "warning", fmt.Sprintf("The last %d recorded checks in a row failed to observe or act, since %s.", st.FailedChecks, st.FailedSince.Format(time.RFC3339)), "Run `keepgoing status` for the reason of the last check and read the errors in the run log; fix the provider CLI, network or repository it names.")
	}
}

func printDoctor(e env, r doctorReport, asJSON bool) int {
	code := exitOK
	for _, c := range r.Checks {
		if c.Status == "error" {
			code = exitFailed
		}
	}
	if asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
		return code
	}
	fmt.Fprintf(e.stdout, "keepgoing doctor (%s)\n", r.Version)
	if r.Loop != "" {
		fmt.Fprintf(e.stdout, "loop: %s; control: %s; target: %s\n", r.Loop, r.ControlState, r.Target)
	}
	if r.TargetAgent != nil {
		fmt.Fprintf(e.stdout, "agent: %s; provider: %s; quota group: %s\n", r.TargetAgent.Adapter, r.TargetAgent.Provider, r.QuotaGroup)
		if r.TargetAgent.BBThread != "" {
			fmt.Fprintf(e.stdout, "thread: %s\n", r.TargetAgent.BBThread)
		} else if r.TargetAgent.Workdir != "" {
			fmt.Fprintf(e.stdout, "project: %s\n", r.TargetAgent.Workdir)
		}
	}
	if r.LastCheckAt != nil {
		fmt.Fprintf(e.stdout, "last check: %s\n", when(*r.LastCheckAt))
	}
	fmt.Fprintf(e.stdout, "next check: %s\n", r.NextCheck)
	if r.Daemon != nil && r.Daemon.State == "responsive" {
		fmt.Fprintf(e.stdout, "daemon: %s; dry run %t; live build %s\n", r.Daemon.Phase, r.Daemon.DryRun, r.Daemon.BinaryComparison)
	}
	for _, c := range r.Checks {
		fmt.Fprintf(e.stdout, "[%s] %s: %s\n", c.Status, c.ID, c.Message)
		if c.Fix != "" {
			fmt.Fprintf(e.stdout, "  %s\n", c.Fix)
		}
	}
	ids := make([]string, 0, len(r.Sessions))
	for id := range r.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if s := r.Sessions[id]; s.ID != "" {
			fmt.Fprintf(e.stdout, "saved session (%s): %s (live identity unverified)\n", id, s.ID)
		}
	}
	return code
}

func doctorHeartbeat(r *doctorReport, st state.File, ctl control.File, controlKnown bool, stale time.Duration) {
	if st.CheckedAt.IsZero() {
		status := "unavailable"
		if controlKnown && ctl.Enabled {
			status = "warning"
		}
		r.add("heartbeat", status, "No completed tick is recorded.", "After reviewing setup, run a tick or start your supervisor; enable alone does not schedule checks.")
		return
	}
	r.LastCheckAt = &st.CheckedAt
	age := r.CheckedAt.Sub(st.CheckedAt)
	switch {
	case age < -time.Minute:
		r.add("heartbeat", "warning", "The last check is in the future.", "Check host clocks and whether --now was used for a test tick.")
	case age > stale:
		r.add("heartbeat", "warning", "The last check exceeds the configured stale-after threshold.", "Inspect the supervisor and its logs, or adjust --stale-after above your external scheduler interval.")
	default:
		r.add("heartbeat", "ok", "A tick was recorded within the configured threshold; this is historical evidence, not a live scheduler check.", "")
	}
}

func doctorFile(r *doctorReport, id, path string, directory bool) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) && directory {
		r.add(id, "unavailable", "Runtime directory does not exist yet.", "It is created by control commands or ticks; doctor leaves it untouched.")
		return
	}
	if err != nil {
		r.add(id, "error", "Configured file or directory is missing or inaccessible.", "Check the configured path and access permissions.")
		return
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		r.add(id, "error", "Configured path has the wrong file type.", "Use a directory for state and regular files for configuration, task, and records.")
		return
	}
	open := os.Open
	if !directory {
		open = snapshot.Open // never block a concurrent save of a runtime record (Windows)
	}
	f, err := open(path)
	if err != nil {
		r.add(id, "error", "Configured path could not be opened for reading.", "Review its access permissions.")
		return
	}
	_ = f.Close()
	doctorACL(r, id+".acl", path, directory, doctorACLMetadata)
	if runtime.GOOS == "windows" {
		r.add(id, "unavailable", "Configured path is readable; Windows mode bits do not establish privacy. See the separate DACL metadata check; runtime access is checked separately for supported paths.", "Review access permissions for private configuration and state.")
	} else if info.Mode().Perm()&0o077 != 0 {
		r.add(id, "warning", "Configured path grants group or other access.", "Review permissions; keep task, account references, and runtime records private. Doctor never changes permissions.")
	} else {
		r.add(id, "ok", "Configured path is readable and has owner-only Unix mode bits. This mode-bit check does not establish write access or ACL privacy.", "")
	}
}

func doctorAgent(ctx context.Context, r *doctorReport, id string, a config.Agent, offline bool, timeout time.Duration, run doctorRun) {
	prefix := "agent." + id
	var cli, accountKey, workdir string
	var extraArgs []string
	var env map[string]string
	switch {
	case a.BB != nil:
		cli = a.BB.CLI
	case a.Claude != nil:
		cli, accountKey, env = a.Claude.CLI, "CLAUDE_CONFIG_DIR", a.Claude.Env
		workdir, extraArgs = a.Claude.Workdir, a.Claude.ExtraArgs
	case a.Codex != nil:
		cli, accountKey, env = a.Codex.CLI, "CODEX_HOME", a.Codex.Env
		workdir, extraArgs = a.Codex.Workdir, a.Codex.ExtraArgs
	case a.Cursor != nil:
		cli, workdir, env = a.Cursor.CLI, a.Cursor.Workdir, a.Cursor.Env
		r.add(prefix+".account", "unavailable", "Cursor quota, usage windows and reset times have no structured interface and are not checked. Account identity is never displayed; existing CLI configuration and environment are inherited.", "")
		r.add(prefix+".models", "unavailable", doctorCursorModels(a.Cursor.Model), "Check the model with Cursor's own model listing; doctor does not parse it.")
		r.add(prefix+".workspace", "unavailable", "Whether Cursor trusts the configured project directory cannot be checked. In a directory it does not trust, every turn exits with an error before it starts; keepgoing never passes a trust or approval flag.", "Open the project once with Cursor's own CLI and answer its trust question before starting supervision.")
		r.add(prefix+".capabilities", "unavailable", initCursorNotice, "Review the Cursor integration limits before starting supervision.")
	case a.Custom != nil:
		// A defined agent declares no version or login interface, and doctor
		// runs nothing a definition does not declare: it resolves the
		// executable and says what the definition lets keepgoing do.
		workdir, env = a.Custom.Workdir, a.Custom.Env
		spec, set, ok := runner.Resolve(a)
		if !ok {
			r.add(prefix+".cli", "error", "The defined agent has no usable definition.", "Run validate and correct the custom agent's definition.")
			return
		}
		if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
			r.add(prefix+".workdir", "error", "The defined agent's working directory is missing or not a directory.", "Correct custom.workdir; a relative path resolves against the configuration file.")
		} else {
			r.add(prefix+".workdir", "ok", "The defined agent's working directory exists.", "")
		}
		// Every command the definition names is checked, not only start.
		d := a.Custom.Definition
		commands := []struct {
			id, name string
			argv     config.Argv
		}{{".cli", "start", d.Start}, {".cli.resume", "resume", d.Resume}}
		if d.Probe != nil {
			commands = append(commands, struct {
				id, name string
				argv     config.Argv
			}{".cli.probe", "probe", d.Probe.Argv})
		}
		if d.Limits != nil {
			commands = append(commands, struct {
				id, name string
				argv     config.Argv
			}{".cli.limits", "limits", d.Limits.Argv})
		}
		for _, c := range commands {
			if len(c.argv) == 0 || len(c.argv[0].Parts) != 1 {
				continue
			}
			if c.id != ".cli" && c.argv[0].Parts[0] == set.CLI {
				continue // the same executable as start, already checked
			}
			if _, err := exec.LookPath(c.argv[0].Parts[0]); err != nil {
				r.add(prefix+c.id, "error", "The defined agent's "+c.name+" executable is missing or not executable.", "Install it or correct the first argument of the definition's "+c.name+" command and the scheduler PATH. A path resolves against the configuration file.")
			} else {
				r.add(prefix+c.id, "ok", "The defined agent's "+c.name+" executable resolves. It was not run.", "")
			}
		}
		can, cannot := spec.Abilities()
		r.add(prefix+".capabilities", "ok", "Defined agent "+spec.Provider+". keepgoing can: "+strings.Join(can, "; ")+".", "")
		if len(cannot) > 0 {
			r.add(prefix+".limits", "unavailable", "With this definition keepgoing cannot: "+strings.Join(cannot, "; ")+".", "Declare the capability with the rule that lets it be observed, if the agent's documented interface supports it.")
		}
		if spec.Probe.Command != nil {
			r.add(prefix+".probe", "warning", "The probe is the command the definition gives, with a fixed short prompt. keepgoing cannot tell what it does: if it is the agent's ordinary turn command, each probe is a full paid turn with tools and side effects.", "Use the agent's cheapest documented check for the probe, and see fallbacks check --dry-run for when probes run.")
		}
		r.add(prefix+".account", "unavailable", "A defined agent's account and login are not queried; its environment is passed through.", "")
		return
	case a.Process != nil:
		r.add(prefix+".cli", "unavailable", "Custom process commands are not executed by doctor.", "Check their structured status contract separately.")
		return
	}
	if accountKey != "" {
		if path, ok := env[accountKey]; ok {
			// Config environment values are literal, unlike YAML path fields.
			// Relative account paths would depend on the invoked command's cwd.
			if !filepath.IsAbs(path) {
				r.add(prefix+".account", "warning", "The explicit account directory is not absolute; its value is not displayed.", "Set an absolute existing account directory so turns and checks use the same account.")
			} else {
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() {
					r.add(prefix+".account", "error", "The explicit account directory is missing or inaccessible; its value is not displayed.", "Check the target's account-directory override. Do not create or switch logins to repair diagnostics.")
				} else {
					r.add(prefix+".account", "ok", "Explicit account directory exists; credentials and account identity were not read.", "")
				}
			}
		} else {
			r.add(prefix+".account", "unavailable", "Account selection inherits the launching environment and provider defaults.", "Check the scheduler's environment; doctor does not infer a subscription from directory names.")
		}
	}
	resolved, err := exec.LookPath(cli)
	if err != nil {
		r.add(prefix+".cli", "error", "Configured CLI is missing or not executable.", "Install the supported CLI or correct its configured path and the scheduler PATH.")
		r.add(prefix+".login", "unavailable", "Login status cannot be queried without the configured CLI.", "")
		return
	}
	// Keep relative executable paths tied to the caller before setting cmd.Dir.
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		r.add(prefix+".cli", "error", "Configured CLI path could not be resolved.", "Check the launching directory and configured executable.")
		r.add(prefix+".login", "unavailable", "Login status cannot be queried without the configured CLI.", "")
		return
	}
	r.add(prefix+".cli", "ok", "Configured CLI resolves to an executable.", "")
	if offline {
		r.add(prefix+".login", "unavailable", "Login status query skipped in offline mode.", "")
		r.add(prefix+".version", "unavailable", "CLI version query skipped in offline mode.", "")
		if a.BB != nil {
			r.add(prefix+".target", "unavailable", "BB target query skipped in offline mode.", "")
		}
		return
	}
	call := func(args ...string) ([]byte, error) {
		cmdCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return run(cmdCtx, resolved, args, env, workdir)
	}
	out, err := call("--version")
	if err != nil {
		r.add(prefix+".version", "unavailable", "CLI version command failed, timed out, or exceeded its output limit.", "Check the executable and supported --version interface; raw output is withheld.")
	} else if v := doctorAgentVersion(a.Adapter, out); v != "" && a.Cursor != nil {
		doctorCursorVersion(r, prefix+".version", v)
	} else if v != "" {
		r.add(prefix+".version", "ok", "CLI version: "+v+". Compatibility is not inferred from the version alone.", "")
	} else {
		r.add(prefix+".version", "unavailable", "CLI returned an unrecognized version format; raw output is withheld.", "Check the installed CLI version manually.")
	}
	if a.BB != nil {
		r.add(prefix+".login", "unavailable", "BB-managed provider authentication is not queried by doctor.", "Verify the selected account through BB's provider controls.")
		if a.BB.NewThread && a.BB.Thread == "" {
			// Nothing exists to inspect, and doctor never creates a thread.
			r.add(prefix+".target", "unavailable", "This fallback starts a new BB thread at handoff; there is no thread to inspect until then.", "")
		} else {
			out, err := call("thread", "show", a.BB.Thread, "--json")
			doctorBB(r, prefix+".target", a.BB.Thread, out, err)
		}
	} else if len(extraArgs) > 0 {
		r.add(prefix+".login", "unavailable", "Login status skipped because extra_args may change the turn's authentication configuration.", "Verify the effective account with the provider's controls; doctor does not replay arbitrary turn arguments.")
	} else if a.Claude != nil {
		out, err := call("auth", "status", "--json")
		doctorClaudeLogin(r, prefix+".login", out, err)
	} else if a.Cursor != nil {
		if doctorCursorEnvCredential(env) {
			r.add(prefix+".login", "unavailable", "A Cursor API key or token is supplied through the environment. The status command describes the stored login, which may not be the credential a turn uses, so it was not queried.", "Verify the key with Cursor; doctor does not read or test it.")
		} else {
			out, err := call("status", "--format", "json")
			doctorCursorLogin(r, prefix+".login", out, err)
		}
	} else if a.Codex != nil {
		_, err := call("login", "status")
		doctorCodexLogin(r, prefix+".login", err)
	}
}

// Only a narrow version token is ever returned, never arbitrary CLI stdout.
var doctorVersionPattern = regexp.MustCompile(`^(?:(?:codex-cli|claude|Claude Code|bb|bb version) )?v?([0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6})(?: \(Claude Code\))?$`)

func doctorVersion(raw []byte) string {
	m := doctorVersionPattern.FindStringSubmatch(strings.TrimSpace(string(raw)))
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

func doctorBB(r *doctorReport, id, expected string, raw []byte, cmdErr error) {
	var resp struct {
		OK    *bool `json:"ok"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
		Thread *struct {
			ID         string `json:"id"`
			ArchivedAt any    `json:"archivedAt"`
			DeletedAt  any    `json:"deletedAt"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &resp) == nil {
		if resp.Error != nil && resp.Error.Code == "thread_not_found" {
			r.add(id, "error", "Configured BB thread does not exist.", "Select the intended existing thread; doctor does not create or transfer it.")
			return
		}
		if cmdErr == nil && (resp.OK == nil || *resp.OK) && resp.Error == nil && resp.Thread != nil && resp.Thread.ID == expected {
			if resp.Thread.ArchivedAt != nil || resp.Thread.DeletedAt != nil {
				r.add(id, "error", "Configured BB thread is archived or deleted.", "Review the intended target before resuming.")
			} else {
				r.add(id, "ok", "BB returned the configured thread identity; conversation content was not inspected.", "")
			}
			return
		}
	}
	r.add(id, "unavailable", "BB target identity could not be verified from the command's structured response.", "Check BB availability and the configured thread ID. Raw output and error text are withheld.")
}

// Bound both runtime and stdout. Stderr is discarded, never parsed or reported.
// These are version/show/auth-status calls only; custom commands and probes are
// deliberately outside this function's callers.
func doctorExec(ctx context.Context, cli string, args []string, overrides map[string]string, workdir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Dir = workdir
	cmd.WaitDelay = time.Second
	cmd.Env = os.Environ()
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+overrides[k])
	}
	buf := &doctorBuffer{remaining: 64 * 1024}
	cmd.Stdout = buf
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if buf.exceeded {
		return nil, errors.New("diagnostic output limit exceeded")
	}
	return buf.Bytes(), err
}

type doctorBuffer struct {
	buffer    bytes.Buffer
	remaining int
	exceeded  bool
}

func (b *doctorBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		b.exceeded = true
		return 0, errors.New("diagnostic output limit exceeded")
	}
	b.remaining -= len(p)
	return b.buffer.Write(p)
}

func (b *doctorBuffer) Bytes() []byte { return b.buffer.Bytes() }

func doctorAgentVersion(adapter string, raw []byte) string {
	if adapter == "cursor" {
		v := strings.TrimSpace(string(raw))
		if config.CursorVersionPattern.MatchString(v) {
			return v
		}
		return ""
	}
	return doctorVersion(raw)
}

// doctorVariables says when a variable's two names, LOOPTENDER_X and the
// earlier KEEPGOING_X, are set to different values, and which earlier names
// are in use. It never prints a value: two of them are credentials.
//
// The session directory is left to its own check, which reports the same
// disagreement as the reason claims cannot be kept.
func doctorVariables(r *doctorReport) {
	location := map[envvar.Var]bool{}
	for _, v := range envvar.Locations {
		location[v] = true
	}
	var earlier []string
	for _, v := range envvar.Set {
		id := "environment." + strings.ToLower(string(v))
		switch _, name, err := v.Path(); {
		case location[v] && err != nil:
			if v != envvar.SessionDir {
				r.add(id, "error", v.Name()+" and "+v.Legacy()+" are set to different places. Commands that read either refuse until they agree.", "Unset "+v.Legacy()+", or set both to the same place.")
			}
		case !location[v] && v.Disagrees():
			r.add(id, "warning", v.Name()+" and "+v.Legacy()+" are set to different values. "+v.Name()+" is the one used.", "Unset "+v.Legacy()+".")
		case name == v.Legacy():
			earlier = append(earlier, name)
		}
	}
	if len(earlier) > 0 {
		r.add("environment.earlier_names", "ok", "Read under an earlier name: "+strings.Join(earlier, ", ")+". Earlier names still work; the current names begin "+envvar.Prefix+" in place of "+envvar.LegacyPrefix+".", "")
	}
}

// doctorUserDirectory says which name the tool's per-user directories go by
// on this computer and why. It warns when a registry or route list exists
// under the other name as well (those are not read), and when the name was
// decided by a directory that could not be inspected. It never suggests
// removing anything: a directory of either name can also hold a loop's own
// files.
func doctorUserDirectory(r *doctorReport, f userdir.Found) {
	const holds = "the loop registry, the standard route list, session claims and the panel's files"
	const guide = "See \"Per-user directories\" in docs/configuration.md."
	list := func(paths []string) string { return strings.Join(paths, ", ") }
	linked := ""
	if len(f.Linked) > 0 {
		linked = " In " + list(f.Linked) + " the two names are one directory, one being a link to the other."
	}
	switch {
	case f.Name == userdir.LegacyName && len(f.Unread) > 0:
		r.add("user.directory", "warning", "Per-user files ("+holds+") are read from directories named "+userdir.LegacyName+", the tool's earlier name ("+doctorUsed(f)+"). These, kept under the name "+userdir.Name+", are not read while that is so: "+list(f.Unread)+". A loop registered only there is not listed, and a route list there is not followed. Session claims are honoured under both names, so loops run by this release or later never act on one session at once."+linked,
			"Nothing is removed or moved for you, and nothing here needs removing. "+guide)
	case f.Name == userdir.LegacyName && len(f.Unknown) > 0:
		r.add("user.directory", "warning", "Per-user files ("+holds+") are kept in directories named "+userdir.LegacyName+", the tool's earlier name, because "+doctorUsed(f)+"."+linked,
			"Make "+list(f.Unknown)+" readable by this user, so what it holds can decide the name. "+guide)
	case f.Name == userdir.LegacyName:
		r.add("user.directory", "ok", "Per-user files ("+holds+") are kept in directories named "+userdir.LegacyName+", the tool's earlier name, because "+doctorUsed(f)+". Nothing is moved; a computer without them uses "+userdir.Name+"."+linked, "")
	default:
		r.add("user.directory", "ok", "Per-user files ("+holds+") are kept in directories named "+userdir.Name+" under the user configuration and state directories."+linked, "")
	}
}

// doctorUsed says what makes the earlier name the one in use: the tool's
// files that are there, or a directory that could not be looked into.
func doctorUsed(f userdir.Found) string {
	switch {
	case len(f.Used) > 0 && len(f.Unknown) > 0:
		return "this computer already has them there: " + strings.Join(f.Used, ", ") + "; and " + strings.Join(f.Unknown, ", ") + " could not be inspected"
	case len(f.Used) > 0:
		return "this computer already has them there: " + strings.Join(f.Used, ", ")
	case len(f.Unknown) > 0:
		return strings.Join(f.Unknown, ", ") + " could not be inspected, which counts as the earlier name"
	}
	return "an entry of that name was found"
}
