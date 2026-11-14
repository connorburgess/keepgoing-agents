package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

var transferThreadID = regexp.MustCompile(`^thr_[A-Za-z0-9_-]{1,128}$`)
var transferProjectID = regexp.MustCompile(`^proj_[A-Za-z0-9_-]{1,128}$`)
var transferEnvironmentID = regexp.MustCompile(`^env_[A-Za-z0-9_-]{1,128}$`)
var transferHostID = regexp.MustCompile(`^host_[A-Za-z0-9_-]{1,128}$`)
var transferLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type transferTarget struct {
	Thread      string `json:"thread"`
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	Host        string `json:"host,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Status      string `json:"status,omitempty"`
}

type transferPreview struct {
	SchemaVersion        int                  `json:"schemaVersion"`
	Outcome              string               `json:"outcome"`
	ApplySupported       bool                 `json:"applySupported"`
	ReadAt               time.Time            `json:"readAt"`
	Compatibility        string               `json:"compatibility"`
	From                 transferTarget       `json:"from"`
	To                   transferTarget       `json:"to"`
	ConfiguredProvider   string               `json:"configuredProvider,omitempty"`
	ConfiguredModel      string               `json:"configuredModel,omitempty"`
	ConfiguredReasoning  string               `json:"configuredReasoning,omitempty"`
	ConfiguredQuotaGroup string               `json:"configuredQuotaGroup,omitempty"`
	Controls             *incident.Controls   `json:"controls,omitempty"`
	Owner                string               `json:"owner,omitempty"`
	Goal                 *control.GoalSummary `json:"goal,omitempty"`
	Checks               []doctorCheck        `json:"checks"`
}

// A preview is advisory, not a prepared transaction or an authorization token.
// The preview branch takes no locks and performs no provider commands, control
// writes or environment changes. Apply/recover have their own guarded path.
func transferCommand(e env, args []string, run doctorRun) int {
	if len(args) > 0 && (args[0] == "apply" || args[0] == "recover") {
		return transferApply(e, args, run, nil)
	}
	if len(args) == 0 || args[0] != "preview" {
		e.errf("usage: keepgoing transfer preview|apply|recover; see transfer preview --help, transfer apply --help or transfer recover --help")
		return exitUsage
	}
	fs := flag.NewFlagSet("transfer preview", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("c", "", configFlagHelp("configuration path"))
	thread := fs.String("thread", "", "existing destination BB thread ID")
	asJSON := fs.Bool("json", false, "versioned advisory preview")
	offline := fs.Bool("offline", false, "skip BB identity queries")
	timeout := fs.Duration("timeout", 5*time.Second, "timeout per read-only BB query (at most 30s)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("transfer: invalid arguments; see transfer preview --help")
		return exitUsage
	}
	if fs.NArg() != 0 || !transferThreadID.MatchString(*thread) || *timeout <= 0 || *timeout > 30*time.Second {
		e.errf("transfer: provide a valid --thread and a timeout greater than zero and at most 30s; no positional arguments")
		return exitUsage
	}
	if !configDefault(e, "transfer", path) {
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("transfer: configuration unavailable or invalid; inspect it privately")
		return exitUsage
	}
	a := cfg.Loop.Agent
	if a.Adapter != "bb" || a.BB == nil || !transferThreadID.MatchString(a.BB.Thread) {
		e.errf("transfer: preview requires a BB primary with a valid thread ID")
		return exitUsage
	}
	r := transferPreview{SchemaVersion: 1, Outcome: "preview", ApplySupported: true, ReadAt: time.Now().UTC(), Compatibility: "compatible_metadata", From: transferTarget{Thread: a.BB.Thread}, To: transferTarget{Thread: *thread}, Checks: []doctorCheck{}}
	add := func(id, status, message string) {
		r.Checks = append(r.Checks, doctorCheck{ID: id, Status: status, Message: message})
		if status == "error" {
			r.Compatibility = "blocked"
		} else if status == "unavailable" && r.Compatibility != "blocked" {
			r.Compatibility = "unavailable"
		}
	}
	if *thread == a.BB.Thread {
		add("destination", "error", "Source and destination are the same thread.")
	}
	// A copied label may describe an account, but never proves a subscription.
	if !transferLabel.MatchString(a.Provider) || !transferLabel.MatchString(a.Group) || (a.BB.Model != "" && !transferLabel.MatchString(a.BB.Model)) || (a.BB.Reasoning != "" && !transferLabel.MatchString(a.BB.Reasoning)) {
		add("labels", "error", "Provider, quota group, model or reasoning labels are missing or unsupported; raw values withheld.")
	} else {
		r.ConfiguredProvider, r.ConfiguredQuotaGroup, r.ConfiguredModel, r.ConfiguredReasoning = a.Provider, a.Group, a.BB.Model, a.BB.Reasoning
		add("labels", "ok", "Configured labels have a supported shape; this does not verify accounts or model access.")
	}
	if cfg.Loop.HasRouteList() {
		add("fallbacks", "error", "Fallback configurations require a broader transfer plan; this preview only assesses a single BB primary.")
	}
	var ctl control.File
	if err := readTransferRecord(cfg.Loop.StateDir, control.FileName, &ctl); err != nil || control.CheckOwner(ctl, "", false) != nil {
		add("control", "unavailable", "Control or ownership metadata is unavailable; existing holds must be preserved.")
	} else {
		r.Controls = &incident.Controls{Enabled: ctl.Enabled, Paused: ctl.Paused, Stop: ctl.Stop, AwaitingUser: ctl.AwaitingUser, Completed: ctl.Completed}
		if ctl.Ownership != nil {
			r.Owner = ctl.Ownership.ThreadID
		}
		add("control", "ok", "Current control flags read; notes are withheld. Missing controls mean disabled.")
		if ctl.PendingTransfer != nil {
			add("transfer", "error", "A transfer is pending; inspect status and recover it before planning another move.")
		}
		if ctl.BBTarget != "" && ctl.BBTarget != a.BB.Thread {
			add("binding", "error", "Configuration does not match the transferred BB target.")
		}
	}
	var st state.File
	if err := readTransferRecord(cfg.Loop.StateDir, state.FileName, &st); err != nil {
		add("target", "unavailable", "Recorded active target is unavailable.")
	} else if st.Target() != state.Primary {
		add("target", "error", "The loop is supervising a fallback; a primary-only transfer cannot be assessed.")
	} else {
		add("target", "ok", "Recorded active target is primary.")
	}
	if g, err := control.LoadGoal(cfg.Loop.StateDir); err != nil {
		add("goal", "unavailable", "Goal metadata is unavailable; repair it before planning a move.")
	} else if g != nil {
		summary := g.Summary()
		r.Goal = &summary
		if g.OwningLoop != cfg.Loop.Name {
			add("goal", "error", "Goal belongs to a different loop name.")
		}
	}
	if *offline {
		add("threads", "unavailable", "Offline preview skipped BB thread identity and environment checks.")
	} else {
		from, fromPath, fromErr := inspectTransferTarget(e.ctx, a.BB.CLI, a.BB.Thread, *timeout, run)
		to, toPath, toErr := inspectTransferTarget(e.ctx, a.BB.CLI, *thread, *timeout, run)
		if fromErr != nil || toErr != nil {
			add("threads", "unavailable", "Both live BB identities and environments could not be verified; raw output withheld.")
		} else {
			r.From, r.To = from, to
			if from.Project != to.Project || from.Environment != to.Environment || from.Host != to.Host || filepath.Clean(fromPath) != filepath.Clean(toPath) {
				add("environment", "error", "Destination differs in project, environment, host or working directory; cross-environment transfer is outside this preview.")
			} else {
				add("environment", "ok", "BB reports the same project, environment, host and working directory for both threads.")
			}
			if from.Provider != a.Provider || to.Provider != a.Provider {
				add("provider", "error", "A thread provider differs from the configured provider; automatic relabeling is not supported.")
			} else {
				add("provider", "ok", "Both thread provider IDs match the configured provider label.")
			}
		}
	}
	if cfg.CheckSources() != nil {
		add("config", "unavailable", "Configuration changed during preview; discard this result and inspect again.")
	}
	// The preview never grants authority or reserves a future apply operation.
	r.Checks = append(r.Checks,
		doctorCheck{ID: "model_account", Status: "unavailable", Message: "Thread show does not verify model/reasoning selection, account identity, quota or model access. Configured labels are not verification."},
		doctorCheck{ID: "apply", Status: "warning", Message: "Preview only. Use explicit transfer apply with expected --from and --thread; it revalidates under the tick lock and leaves an awaiting-user hold. The plugin move UI is not implemented."},
		doctorCheck{ID: "freshness", Status: "warning", Message: "Reads are independent, not an atomic snapshot. Holds, ownership and targets may change; a future apply operation must revalidate under locks."},
	)
	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if enc.Encode(r) != nil {
			return exitFailed
		}
	} else {
		fmt.Fprintf(e.stdout, "Transfer preview: %s -> %s (%s; apply requires revalidation)\n", r.From.Thread, r.To.Thread, r.Compatibility)
		for _, target := range []transferTarget{r.From, r.To} {
			if target.Environment != "" {
				fmt.Fprintf(e.stdout, "%s: provider=%s status=%s project=%s environment=%s host=%s\n", target.Thread, target.Provider, target.Status, target.Project, target.Environment, target.Host)
			}
		}
		fmt.Fprintf(e.stdout, "Configured provider: %s; model: %s; reasoning: %s; quota group: %s\n", r.ConfiguredProvider, r.ConfiguredModel, r.ConfiguredReasoning, r.ConfiguredQuotaGroup)
		if r.Controls != nil {
			fmt.Fprintf(e.stdout, "Controls: enabled=%t paused=%t stop=%t awaiting-user=%t completed=%t\n", r.Controls.Enabled, r.Controls.Paused, r.Controls.Stop, r.Controls.AwaitingUser, r.Controls.Completed)
		}
		if r.Owner != "" {
			fmt.Fprintf(e.stdout, "Recorded owner: %s\n", r.Owner)
		}
		if r.Goal != nil {
			printGoal(e, *r.Goal, false)
		}
		for _, c := range r.Checks {
			fmt.Fprintf(e.stdout, "%s [%s]: %s\n", c.ID, c.Status, c.Message)
		}
	}
	if r.Compatibility != "compatible_metadata" {
		return exitFailed
	}
	return exitOK
}

func readTransferRecord(dir, name string, dst any) error {
	p := filepath.Join(dir, name)
	info, err := snapshot.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("record unavailable")
	}
	// Never block a concurrent control or state save (Windows).
	f, err := snapshot.Open(p)
	if err != nil {
		return errors.New("record unavailable")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("record unavailable")
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return errors.New("record unavailable")
	}
	// Require an object: a null control must not look like a disabled loop.
	var object map[string]json.RawMessage
	if json.Unmarshal(b, &object) != nil || object == nil {
		return errors.New("record unavailable")
	}
	return json.Unmarshal(b, dst)
}

func inspectTransferTarget(ctx context.Context, cli, id string, timeout time.Duration, run doctorRun) (transferTarget, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	b, err := run(ctx, cli, []string{"thread", "show", id, "--json"}, nil, "")
	bad := errors.New("BB identity unavailable")
	if err != nil || ctx.Err() != nil || len(b) > 64*1024 {
		return transferTarget{}, "", bad
	}
	var v struct {
		OK     *bool           `json:"ok"`
		Error  json.RawMessage `json:"error"`
		Thread *struct {
			ID          string          `json:"id"`
			Project     string          `json:"projectId"`
			Environment string          `json:"environmentId"`
			Provider    string          `json:"providerId"`
			Status      string          `json:"status"`
			Archived    json.RawMessage `json:"archivedAt"`
			Deleted     json.RawMessage `json:"deletedAt"`
		} `json:"thread"`
		Environment *struct {
			ID      string `json:"id"`
			Project string `json:"projectId"`
			Host    string `json:"hostId"`
			Path    string `json:"path"`
		} `json:"environment"`
	}
	if json.Unmarshal(b, &v) != nil || (v.OK != nil && !*v.OK) || (len(v.Error) != 0 && string(v.Error) != "null") || v.Thread == nil || v.Environment == nil {
		return transferTarget{}, "", bad
	}
	t, e := v.Thread, v.Environment
	if t.ID != id || !transferProjectID.MatchString(t.Project) || !transferEnvironmentID.MatchString(t.Environment) || !transferHostID.MatchString(e.Host) || !transferLabel.MatchString(t.Provider) || string(t.Archived) != "null" || string(t.Deleted) != "null" || e.ID != t.Environment || e.Project != t.Project || !filepath.IsAbs(e.Path) {
		return transferTarget{}, "", bad
	}
	switch t.Status {
	case "pending", "idle", "starting", "active", "stopping", "error":
	default:
		return transferTarget{}, "", bad
	}
	return transferTarget{Thread: id, Project: t.Project, Environment: t.Environment, Host: e.Host, Provider: t.Provider, Status: t.Status}, e.Path, nil
}
