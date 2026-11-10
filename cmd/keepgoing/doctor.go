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

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/state"
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
	SchemaVersion int           `json:"schemaVersion"`
	Version       string        `json:"version"`
	Loop          string        `json:"loop,omitempty"`
	Target        string        `json:"target,omitempty"`
	TargetAgent   *agentRef     `json:"targetAgent,omitempty"`
	QuotaGroup    string        `json:"quotaGroup,omitempty"`
	ControlState  string        `json:"controlState,omitempty"`
	CheckedAt     time.Time     `json:"checkedAt"`
	LastCheckAt   *time.Time    `json:"lastCheckAt,omitempty"`
	StaleAfter    string        `json:"staleAfter"`
	NextCheck     string        `json:"nextCheck"`
	Checks        []doctorCheck `json:"checks"`
}

func (r *doctorReport) add(id, status, message, fix string) {
	r.Checks = append(r.Checks, doctorCheck{id, status, message, fix})
}

type doctorRun func(context.Context, string, []string, map[string]string, string) ([]byte, error)

func doctor(e env, args []string, run doctorRun) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	path := fs.String("c", "keepgoing.yaml", "path to the config file")
	asJSON := fs.Bool("json", false, "print the versioned diagnostic report")
	offline := fs.Bool("offline", false, "inspect local files only; do not execute CLIs")
	timeout := fs.Duration("timeout", 5*time.Second, "timeout per read-only CLI command")
	stale := fs.Duration("stale-after", defaultStaleAfter, "warn when the last check is older; set above your scheduler interval")
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
	r := doctorReport{SchemaVersion: 1, Version: version, CheckedAt: time.Now().UTC(), StaleAfter: stale.String(), NextCheck: unknownNextCheck, Checks: []doctorCheck{}}
	cfg, err := config.Load(*path)
	if err != nil {
		r.add("config", "error", "Configuration or fallbacks could not be read or validated.", "Check the selected config path and YAML against the configuration guide. Raw parser errors are withheld to protect values.")
		return printDoctor(e, r, *asJSON)
	}
	r.Loop = cfg.Loop.Name
	r.add("config", "ok", "Configuration and configured fallbacks validate.", "")
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
	for _, name := range []string{control.FileName, state.FileName, "health.json", state.LogName} {
		p := filepath.Join(cfg.Loop.StateDir, name)
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			doctorFile(&r, "state."+name, p, false)
		}
	}
	ctl, ctlErr := control.Load(cfg.Loop.StateDir)
	if ctlErr != nil {
		r.add("control", "error", "Control state could not be read.", "Repair control.json before enabling supervision; do not discard an existing stop or pause.")
	} else {
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
			candidate, ok := cfg.FallbackSet.Find(r.Target)
			if !ok || candidate.Disabled {
				valid = false
				r.add("target", "error", "Recorded active target is missing or disabled in the fallback configuration.", "Review the intended target and use retarget explicitly before resuming.")
			} else {
				a = candidate.Agent
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
		doctorAgent(e.ctx, &r, id, a, *offline, *timeout, run)
	}
	checkAgent("primary", cfg.Loop.Agent)
	if cfg.FallbackSet != nil {
		for _, c := range cfg.FallbackSet.Candidates {
			if c.Disabled {
				continue
			}
			checkAgent(c.ID, c.Agent)
		}
	}
	r.add("scheduler", "unavailable", "Scheduler installation, liveness, and next scheduled check are not verified by this command.", "Inspect your daemon or external scheduler. A recent tick alone does not prove it is still running.")
	doctorCheckVersion(&r, st.LastCheck, stateErr == nil)
	r.add("probes", "unavailable", "No model requests or paid probes were made. Login status does not establish credential validity, model access, or quota health.", "Use fallbacks check explicitly only when you intend model calls; its manual probes are outside the scheduled daily cap.")
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
	for _, c := range r.Checks {
		fmt.Fprintf(e.stdout, "[%s] %s: %s\n", c.Status, c.ID, c.Message)
		if c.Fix != "" {
			fmt.Fprintf(e.stdout, "  %s\n", c.Fix)
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
	f, err := os.Open(path)
	if err != nil {
		r.add(id, "error", "Configured path could not be opened for reading.", "Review its access permissions.")
		return
	}
	_ = f.Close()
	if runtime.GOOS == "windows" {
		r.add(id, "unavailable", "Configured path is readable; Windows ACL privacy and write access were not checked.", "Review access permissions for private configuration and state.")
	} else if info.Mode().Perm()&0o077 != 0 {
		r.add(id, "warning", "Configured path grants group or other access.", "Review permissions; keep task, account references, and runtime records private. Doctor never changes permissions.")
	} else {
		r.add(id, "ok", "Configured path is readable and has owner-only Unix mode bits; write access and extended ACLs were not checked.", "")
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
	} else if v := doctorVersion(out); v != "" {
		r.add(prefix+".version", "ok", "CLI version: "+v+". Compatibility is not inferred from the version alone.", "")
	} else {
		r.add(prefix+".version", "unavailable", "CLI returned an unrecognized version format; raw output is withheld.", "Check the installed CLI version manually.")
	}
	if a.BB != nil {
		r.add(prefix+".login", "unavailable", "BB-managed provider authentication is not queried by doctor.", "Verify the selected account through BB's provider controls.")
		out, err := call("thread", "show", a.BB.Thread, "--json")
		doctorBB(r, prefix+".target", a.BB.Thread, out, err)
	} else if len(extraArgs) > 0 {
		r.add(prefix+".login", "unavailable", "Login status skipped because extra_args may change the turn's authentication configuration.", "Verify the effective account with the provider's controls; doctor does not replay arbitrary turn arguments.")
	} else if a.Claude != nil {
		out, err := call("auth", "status", "--json")
		doctorClaudeLogin(r, prefix+".login", out, err)
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
