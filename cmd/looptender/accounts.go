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
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/userdir"
)

type accountRun func(context.Context, string, []string, []string, string) ([]byte, error)

func accountsDirectory(path string) (string, error) {
	if path == "" {
		var err error
		path, err = userdir.Config("accounts")
		if err != nil {
			return "", accounts.ErrInvalid
		}
	}
	return registryDirectory(path) // same resolved ancestry/checkout/privacy guards
}
func accountsCommand(e env, args []string, run accountRun) int {
	if len(args) == 0 {
		e.errf("usage: accounts list|add|update|check|usage [flags]")
		return exitUsage
	}
	op := args[0]
	fs := flag.NewFlagSet("accounts "+op, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dir := fs.String("directory", "", "private catalog directory")
	id := fs.String("id", "", "selected connection ID")
	label := fs.String("label", "", "owner-selected account label")
	provider := fs.String("provider", "", "claude-code, codex, antigravity, cursor, bb or other")
	mode := fs.String("selection", "inherited", "inherited, explicit_directory or externally_managed")
	cli := fs.String("cli", "", "selected provider executable")
	accountDir := fs.String("account-dir", "", "existing selected account directory; never copied")
	workdir := fs.String("workdir", "", "selected working directory")
	expect := fs.String("expect", "", "required current revision for update")
	asJSON := fs.Bool("json", false, "print minimized structured result")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		return exitUsage
	}
	supplied := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	for k := range supplied {
		allowed := k == "directory" || k == "json" || ((op == "check" || op == "update" || op == "usage") && k == "id") ||
			((op == "add" || op == "update") && (k == "label" || k == "provider" || k == "selection" || k == "cli" || k == "account-dir" || k == "workdir" || (op == "update" && k == "expect")))
		if !allowed {
			e.errf("accounts: flag not allowed for this operation")
			return exitUsage
		}
	}
	if op != "list" && op != "add" && op != "update" && op != "check" && op != "usage" {
		return exitUsage
	}
	path, err := accountsDirectory(*dir)
	if err != nil {
		e.errf("accounts: private catalog directory unavailable")
		return exitFailed
	}
	if op == "usage" {
		return accountsUsage(e, path, *id, *asJSON)
	}
	now := time.Now().UTC()
	caller := os.Getenv("BB_THREAD_ID")
	effective := os.Environ()
	if len(caller) > 160 {
		e.errf("accounts: caller identity unavailable")
		return exitFailed
	}
	var resultID, revision string
	switch op {
	case "list":
	case "add", "update":
		selected := accounts.Entry{Label: *label, Provider: *provider, Mode: *mode}
		if *provider == "claude-code" || *provider == "codex" || *provider == "antigravity" {
			name := *cli
			if name == "" {
				switch *provider {
				case "codex":
					name = "codex"
				case "antigravity":
					name = "agy"
				default:
					name = "claude"
				}
			}
			resolved, lookup := exec.LookPath(name)
			if lookup != nil {
				e.errf("accounts: selected executable unavailable")
				return exitFailed
			}
			selected.CLI, err = filepath.Abs(resolved)
			if err == nil {
				selected.CLI, err = filepath.EvalSymlinks(selected.CLI)
			}
			if err != nil {
				e.errf("accounts: selected executable unavailable")
				return exitFailed
			}
			wd := *workdir
			if wd == "" {
				wd, err = os.Getwd()
			}
			if err == nil {
				selected.Workdir, err = existingDirectory(wd)
			}
			if err != nil {
				e.errf("accounts: selected working directory unavailable")
				return exitFailed
			}
			if *mode == "explicit_directory" {
				selected.Directory, err = existingDirectory(*accountDir)
				if err != nil {
					e.errf("accounts: selected account directory unavailable")
					return exitFailed
				}
			}
			if *mode != "explicit_directory" && *accountDir != "" {
				return exitUsage
			}
		} else if *cli != "" || *workdir != "" || *accountDir != "" {
			return exitUsage
		}
		err = accounts.Update(path, func(f *accounts.File) error {
			if op == "update" {
				old := accounts.Find(f, *id)
				if old == nil || *expect == "" || old.Revision != *expect {
					return accounts.ErrChanged
				}
				selected = accounts.Revise(*f, *old, selected, caller, now, effective)
				*old = selected
			} else {
				if len(f.Entries) >= accounts.MaxEntries {
					return accounts.ErrInvalid
				}
				selected = accounts.NewEntry(*f, selected, caller, now, effective)
				f.Entries = append(f.Entries, selected)
			}
			resultID, revision = selected.ID, selected.Revision
			return nil
		})
		if home, herr := os.UserHomeDir(); err == nil && herr == nil && accountExplicitIsDefault(selected, home) {
			e.errf("warning: %s. %s", accountDefaultDirWarning, accountDefaultDirFix)
		}
	case "check":
		err = accountCheck(e.ctx, path, *id, caller, run, now)
	}
	if err != nil {
		if errors.Is(err, lock.ErrHeld) {
			e.errf("accounts: operation already running")
			return exitSkipped
		}
		if errors.Is(err, accounts.ErrChanged) {
			e.errf("accounts: selection or context changed; review again")
		} else {
			e.errf("accounts: catalog or selected operation unavailable")
		}
		return exitFailed
	}
	view := accounts.ReadView(path, time.Now().UTC())
	if !view.Complete {
		e.errf("accounts: catalog unavailable")
		return exitFailed
	}
	revisions := map[string]string{}
	catalog, loadErr := accounts.Load(path)
	if loadErr != nil {
		e.errf("accounts: catalog unavailable")
		return exitFailed
	}
	for _, selected := range catalog.Entries {
		revisions[selected.ID] = selected.Revision
	}
	if *asJSON {
		out := struct {
			Revisions     map[string]string `json:"revisions"`
			SchemaVersion int               `json:"schemaVersion"`
			ID            string            `json:"id,omitempty"`
			Revision      string            `json:"revision,omitempty"`
			Accounts      accounts.View     `json:"accounts"`
		}{revisions, 1, resultID, revision, view}
		if json.NewEncoder(e.stdout).Encode(out) != nil {
			return exitFailed
		}
	} else {
		if resultID != "" {
			fmt.Fprintf(e.stdout, "Selected account %s; revision %s.\n", resultID, revision)
		}
		for _, c := range view.Cards {
			fmt.Fprintf(e.stdout, "%s  %s  %s  %s  revision %s\n", c.ID, c.Label, c.Provider, c.Auth.State, revisions[c.ID])
		}
		fmt.Fprintln(e.stdout, "Selection and login reports do not verify identity, quota or model access. No route or loop was changed.")
	}
	return exitOK
}
func accountCheck(ctx context.Context, dir, id, caller string, run accountRun, now time.Time) error {
	f, err := accounts.Load(dir)
	if err != nil {
		return err
	}
	found := accounts.Find(&f, id)
	if found == nil {
		return accounts.ErrInvalid
	}
	selected := *found
	guard, err := accounts.CheckGuard(dir, id)
	if err != nil {
		return err
	}
	defer guard.Release()
	// Re-read after acquiring the selection check guard.
	f, err = accounts.Load(dir)
	if err != nil {
		return err
	}
	current := accounts.Find(&f, id)
	if current == nil || current.Revision != selected.Revision {
		return accounts.ErrChanged
	}
	effective := os.Environ()
	o := accounts.Observation{Revision: selected.Revision, Context: selected.Context, At: now, Auth: "unavailable", Caller: caller}
	bound, accepted := accounts.Bound(f, selected, effective)
	if !accountContextExists(selected) || !accepted {
		o.Reason = "context_changed"
	} else if selected.Provider != "claude-code" && selected.Provider != "codex" {
		o.Reason = "unsupported"
	} else {
		// Recorded under the current definition; this replaces a binding
		// recorded by an earlier build (accounts.Bound).
		o.Context = bound
		effective = accounts.ExecutionEnvironment(selected, effective)
		call := func(args ...string) ([]byte, error) {
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return run(c, selected.CLI, args, effective, selected.Workdir)
		}
		raw, callErr := call("--version")
		adapter := "claude"
		if selected.Provider == "codex" {
			adapter = "codex"
		}
		v := doctorAgentVersion(adapter, raw)
		if callErr != nil || v == "" {
			o.Reason = "version_unavailable"
			if errors.Is(callErr, context.DeadlineExceeded) {
				o.Reason = "timeout"
			}
			if errors.Is(callErr, context.Canceled) {
				o.Reason = "cancelled"
			}
		} else {
			o.Version = v
			if selected.Provider == "claude-code" {
				raw, callErr = call("auth", "status", "--json")
			} else {
				raw, callErr = call("login", "status")
			}
			o = accountLogin(selected, o, raw, callErr)
			if selected.Provider == "codex" && o.Auth == "reported_present" {
				raw, callErr = call("app-server")
				o.Plan, o.PlanReason = accountCodexPlan(raw, callErr)
				if o.Plan != "" {
					o.PlanAt = &now
				} else if (o.PlanReason == "timeout" || o.PlanReason == "cancelled" || o.PlanReason == "status_unavailable") && selected.Last != nil && selected.Last.Plan != "" {
					o.Plan, o.PlanAt = selected.Last.Plan, selected.Last.PlanAt
					if o.PlanAt == nil {
						o.PlanAt = &selected.Last.At
					}
				}
			}
		}
	}
	return accounts.Update(dir, func(latest *accounts.File) error {
		e := accounts.Find(latest, id)
		if e == nil || e.Revision != selected.Revision || e.Context != selected.Context {
			return accounts.ErrChanged
		}
		if o.Reason != "context_changed" {
			if now, ok := accounts.Bound(*latest, *e, os.Environ()); !accountContextExists(*e) || !ok || now != o.Context {
				return accounts.ErrChanged
			}
		}
		if o.Context != e.Context {
			// One-time rebinding of an earlier build's digest. The previous
			// observation belongs to the same accepted context.
			e.Context = o.Context
			if e.Last != nil {
				last := *e.Last
				last.Context = o.Context
				e.Last = &last
			}
		}
		e.Attempt = &o
		if o.Auth == "reported_present" || o.Auth == "reported_absent" {
			e.Last = &o
		}
		return nil
	})
}
func accountLogin(e accounts.Entry, o accounts.Observation, raw []byte, err error) accounts.Observation {
	if errors.Is(err, context.DeadlineExceeded) {
		o.Reason = "timeout"
		return o
	}
	if errors.Is(err, context.Canceled) {
		o.Reason = "cancelled"
		return o
	}
	r := doctorReport{}
	if e.Provider == "claude-code" {
		doctorClaudeLogin(&r, "login", raw, err)
	} else {
		doctorCodexLogin(&r, "login", err)
	}
	if len(r.Checks) != 1 {
		o.Reason = "status_unavailable"
		return o
	}
	switch r.Checks[0].Status {
	case "ok":
		o.Auth = "reported_present"
	case "warning":
		o.Auth = "reported_absent"
	default:
		o.Reason = "status_unavailable"
		return o
	}
	// Only the reviewed broad subscription tokens are retained. Unknown strings,
	// account identities and every other provider field are discarded.
	if e.Provider == "claude-code" && o.Auth == "reported_present" {
		var x struct {
			SubscriptionType string `json:"subscriptionType"`
		}
		if json.Unmarshal(raw, &x) == nil {
			switch x.SubscriptionType {
			case "max", "pro", "team", "enterprise", "free":
				o.Plan = x.SubscriptionType
			}
		}
	}
	return o
}
func accountExec(ctx context.Context, cli string, args []string, env []string, workdir string) ([]byte, error) {
	if len(args) == 1 && args[0] == "app-server" {
		return accountCodexRead(ctx, cli, env, workdir)
	}
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = env
	cmd.Dir = workdir
	cmd.WaitDelay = time.Second
	b := &doctorBuffer{remaining: 64 * 1024}
	cmd.Stdout = b
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if b.exceeded {
		return nil, errors.New("output_limit")
	}
	return bytes.Clone(b.Bytes()), err
}

func accountContextExists(e accounts.Entry) bool {
	if e.Provider != "claude-code" && e.Provider != "codex" {
		return true
	}
	cli, err := filepath.EvalSymlinks(e.CLI)
	if err != nil || cli != e.CLI {
		return false
	}
	work, err := existingDirectory(e.Workdir)
	if err != nil || work != e.Workdir {
		return false
	}
	if e.Mode == "explicit_directory" {
		dir, err := existingDirectory(e.Directory)
		return err == nil && dir == e.Directory
	}
	return true
}
