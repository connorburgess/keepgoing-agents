package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// checkTransferSources extends the source-byte guard to persisted target bindings.
// Pending transfers permit only stop, against a verified before/after config.
func checkTransferSources(cfg *config.Config, allowStop bool) error {
	if err := cfg.CheckSources(); err != nil {
		return err
	}
	return checkTransferRecord(cfg, allowStop)
}

// checkTransferRecord is checkTransferSources without the check that the
// configuration's files are unchanged: the control record, the pending
// transfer rule and the transferred BB target.
func checkTransferRecord(cfg *config.Config, allowStop bool) error {
	if info, err := os.Lstat(filepath.Join(cfg.Loop.StateDir, control.FileName)); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("control.json must not be a symlink; use the original state directory")
	}
	var ctl control.File
	if readTransferRecord(cfg.Loop.StateDir, control.FileName, &ctl) != nil {
		return errors.New("control state unavailable")
	}
	if p := ctl.PendingTransfer; p != nil {
		if !p.Valid() || !ctl.AwaitingUser || cfg.Loop.Adapter != "bb" || cfg.Loop.BB == nil || cfg.Loop.HasRouteList() {
			return errors.New("transfer recovery record unavailable or inconsistent")
		}
		digest := cfg.SourceDigest()
		if !((digest == p.Before && cfg.Loop.BB.Thread == p.From) || (digest == p.After && cfg.Loop.BB.Thread == p.To)) {
			return errors.New("configuration does not match pending transfer")
		}
		if allowStop && ctl.Stop {
			return nil
		}
		return control.ErrTransferPending
	}
	if ctl.BBTarget != "" && (!transferThreadID.MatchString(ctl.BBTarget) || cfg.Loop.Adapter != "bb" || cfg.Loop.BB == nil || cfg.Loop.BB.Thread != ctl.BBTarget) {
		return errors.New("configuration does not match the transferred BB target; use the current loop configuration")
	}
	return nil
}

func transferCaller(ctl control.File, to string, takeOver bool) error {
	if err := control.CheckOwner(ctl, "", false); err != nil {
		return errors.New("ownership metadata unavailable")
	}
	if ctl.Ownership != nil && ctl.Ownership.Generation == ^uint64(0) {
		return errors.New("loop ownership generation exhausted")
	}
	caller := os.Getenv("BB_THREAD_ID")
	if caller == to && takeOver {
		return nil
	}
	if err := control.CheckOwner(ctl, caller, false); err != nil {
		return errors.New("transfer caller is not the owner; the destination thread may explicitly use --take-over")
	}
	return nil
}

// afterStep is nil in production; fixtures interrupt at durable boundaries.
func transferApply(e env, args []string, run doctorRun, afterStep func(string) error) int {
	cmd := args[0]
	fs := flag.NewFlagSet("transfer "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("c", "", configFlagHelp("configuration path"))
	from := fs.String("from", "", "expected current BB primary (apply)")
	to := fs.String("thread", "", "new existing BB primary (apply)")
	id := fs.String("id", "", "pending transfer identity (recover)")
	takeOver := fs.Bool("take-over", false, "explicitly authorize the destination BB caller to take ownership")
	asJSON := fs.Bool("json", false, "machine-readable result")
	timeout := fs.Duration("timeout", 5*time.Second, "timeout per BB metadata query (at most 30s)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("transfer: invalid arguments")
		return exitUsage
	}
	if fs.NArg() != 0 || *timeout <= 0 || *timeout > 30*time.Second || (cmd == "apply" && (!transferThreadID.MatchString(*from) || !transferThreadID.MatchString(*to) || *from == *to || *id != "")) || (cmd == "recover" && (*id == "" || *from != "" || *to != "")) {
		e.errf("transfer: apply requires distinct --from and --thread; recover requires --id; timeout must be greater than zero and at most 30s")
		return exitUsage
	}
	fail := func(err error) int {
		e.errf("transfer: %v", err)
		if errors.Is(err, lock.ErrHeld) {
			return exitSkipped
		}
		return exitFailed
	}
	if !configDefault(e, "transfer", fs, path) {
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("transfer: configuration unavailable; inspect it privately")
		return exitUsage
	}
	if cfg.Loop.Adapter != "bb" || cfg.Loop.BB == nil || cfg.Loop.HasRouteList() {
		return fail(errors.New("apply/recover support only a single BB primary without fallbacks"))
	}
	if err := transferPrivatePaths(*path, cfg); err != nil {
		return fail(err)
	}
	tick, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		return fail(err)
	}
	defer tick.Release()
	if cfg.CheckSources() != nil {
		return fail(errors.New("configuration changed; reload and retry"))
	}
	var ctl control.File
	if readTransferRecord(cfg.Loop.StateDir, control.FileName, &ctl) != nil {
		return fail(errors.New("control record unavailable"))
	}
	var p control.PendingBBTransfer
	var edit *config.BBTransferEdit
	if cmd == "apply" {
		if err := checkTransferSources(cfg, false); err != nil {
			return fail(err)
		}
		if cfg.Loop.BB.Thread != *from {
			return fail(errors.New("expected source thread changed; preview again"))
		}
		if err := transferCaller(ctl, *to, *takeOver); err != nil {
			return fail(err)
		}
		edit, err = cfg.PrepareBBTransfer(*to)
		if err != nil {
			return fail(err)
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return fail(errors.New("could not allocate transfer identity"))
		}
		p = control.PendingBBTransfer{Version: 1, ID: "move_" + hex.EncodeToString(random[:]), From: *from, To: *to, Before: cfg.SourceDigest(), After: edit.AfterDigest()}
	} else {
		if ctl.PendingTransfer == nil || !ctl.PendingTransfer.Valid() || ctl.PendingTransfer.ID != *id || !ctl.AwaitingUser {
			return fail(errors.New("pending transfer identity or recovery record does not match"))
		}
		p = *ctl.PendingTransfer
		if err := transferCaller(ctl, p.To, *takeOver); err != nil {
			return fail(err)
		}
		if cfg.SourceDigest() == p.Before && cfg.Loop.BB.Thread == p.From {
			edit, err = cfg.PrepareBBTransfer(p.To)
			if err != nil || edit.AfterDigest() != p.After {
				return fail(errors.New("configuration no longer matches the planned replacement"))
			}
		} else if cfg.SourceDigest() != p.After || cfg.Loop.BB.Thread != p.To {
			return fail(errors.New("configuration differs from both recorded transfer versions; restore a matching version before recovery"))
		}
	}
	if !transferLabel.MatchString(cfg.Loop.Provider) || !transferLabel.MatchString(cfg.Loop.Group) || (cfg.Loop.BB.Model != "" && !transferLabel.MatchString(cfg.Loop.BB.Model)) || (cfg.Loop.BB.Reasoning != "" && !transferLabel.MatchString(cfg.Loop.BB.Reasoning)) {
		return fail(errors.New("configured labels are unsupported; inspect them privately"))
	}
	// Provider calls are never made under control.lock. Metadata is revalidated
	// on recovery; this is not account/model verification or an atomic BB lease.
	source, sourcePath, err := inspectTransferTarget(e.ctx, cfg.Loop.BB.CLI, p.From, *timeout, run)
	if err != nil {
		return fail(errors.New("source BB identity unavailable"))
	}
	dest, destPath, err := inspectTransferTarget(e.ctx, cfg.Loop.BB.CLI, p.To, *timeout, run)
	if err != nil {
		return fail(errors.New("destination BB identity unavailable"))
	}
	if source.Project != dest.Project || source.Environment != dest.Environment || source.Host != dest.Host || filepath.Clean(sourcePath) != filepath.Clean(destPath) || source.Provider != cfg.Loop.Provider || dest.Provider != cfg.Loop.Provider {
		return fail(errors.New("BB project, environment, host, working directory or provider differs; transfer refused"))
	}
	if e.ctx.Err() != nil {
		return fail(errors.New("operation interrupted before transfer"))
	}
	if cfg.CheckSources() != nil {
		return fail(errors.New("configuration changed during validation"))
	}
	if err := checkTransferLocal(cfg); err != nil {
		return fail(err)
	}
	if cmd == "apply" {
		_, err = updateControlWithHistory(cfg.Loop.StateDir, "transfer_begin", func(f *control.File) error {
			if f.PendingTransfer != nil {
				return control.ErrTransferPending
			}
			if f.BBTarget != "" && f.BBTarget != p.From {
				return errors.New("target binding changed")
			}
			if err := transferCaller(*f, p.To, *takeOver); err != nil {
				return err
			}
			if cfg.CheckSources() != nil {
				return errors.New("configuration changed before transfer")
			}
			f.PendingTransfer = &p
			f.BBTarget = p.From
			f.AwaitingUser = true
			return nil
		})
		if err != nil {
			e.errf("transfer identity: %s; inspect status before retrying; a saved pending transfer requires recover", p.ID)
			return fail(err)
		}
	}
	checkpoint := func(step string) error {
		if e.ctx.Err() != nil {
			return errors.New("operation interrupted; recovery required")
		}
		if afterStep != nil {
			return afterStep(step)
		}
		return nil
	}
	pendingFail := func(err error) int {
		e.errf("transfer remains pending: %s; use transfer recover with this --id", p.ID)
		return fail(err)
	}
	if err := checkpoint("held"); err != nil {
		return pendingFail(err)
	}
	if err := resetTransferState(cfg.Loop.StateDir); err != nil {
		return pendingFail(err)
	}
	if err := checkpoint("state"); err != nil {
		return pendingFail(err)
	}
	if edit != nil {
		if err := edit.Commit(); err != nil {
			return pendingFail(err)
		}
	}
	if err := checkpoint("config"); err != nil {
		return pendingFail(err)
	}
	fresh, err := config.Load(*path)
	if err != nil || fresh.Loop.StateDir != cfg.Loop.StateDir || fresh.SourceDigest() != p.After {
		return pendingFail(errors.New("replacement configuration could not be verified"))
	}
	f, err := updateControlWithHistory(cfg.Loop.StateDir, "transfer_finish", func(f *control.File) error {
		if f.PendingTransfer == nil || *f.PendingTransfer != p || !f.AwaitingUser {
			return errors.New("pending transfer changed")
		}
		if err := transferCaller(*f, p.To, *takeOver); err != nil {
			return err
		}
		if fresh.CheckSources() != nil {
			return errors.New("replacement configuration changed")
		}
		if err := checkTransferLocal(fresh); err != nil {
			return err
		}
		f.PendingTransfer = nil
		return control.BindBBTarget(f, p.To)
	})
	if err != nil && !errors.Is(err, errControlHistory) {
		return pendingFail(err)
	}
	result := struct {
		Outcome                  string `json:"outcome"`
		ID                       string `json:"id"`
		From                     string `json:"from"`
		To                       string `json:"to"`
		AwaitingUser             bool   `json:"awaitingUser"`
		Generation               uint64 `json:"ownerGeneration"`
		ModelAccountVerification string `json:"modelAccountVerification"`
	}{"applied", p.ID, p.From, p.To, true, f.Ownership.Generation, "unavailable"}
	if *asJSON {
		if json.NewEncoder(e.stdout).Encode(result) != nil {
			return exitFailed
		}
	} else {
		fmt.Fprintf(e.stdout, "Transferred %s -> %s; awaiting explicit resume (owner generation %d).\n", p.From, p.To, f.Ownership.Generation)
		fmt.Fprintln(e.stdout, "Model/reasoning and quota-group labels were retained; account and model identity remain unverified.")
	}
	if err != nil {
		return fail(err)
	}
	return exitOK
}

func checkTransferLocal(cfg *config.Config) error {
	var st state.File
	if readTransferRecord(cfg.Loop.StateDir, state.FileName, &st) != nil || st.Target() != state.Primary {
		return errors.New("active primary state cannot be verified")
	}
	g, err := control.LoadGoal(cfg.Loop.StateDir)
	if err != nil || (g != nil && g.OwningLoop != cfg.Loop.Name) {
		return errors.New("goal metadata cannot be preserved until repaired")
	}
	return nil
}

func resetTransferState(dir string) error {
	var st state.File
	if readTransferRecord(dir, state.FileName, &st) != nil || st.Target() != state.Primary {
		return errors.New("primary state unavailable; no reset performed")
	}
	// Retain progress/timing and spent counters. Clear only target observations.
	st.StallRetry = nil
	st.LastState = ""
	st.LastCheck = nil
	st.LastPromptSend = nil
	st.CheckedAt = time.Time{}
	st.StopRequestedSent = false
	st.AwaitingReported = false
	st.PrimaryResetsAt = time.Time{}
	st.LastSwitchBackTry = time.Time{}
	st.ClearBriefs()
	st.ClearChildErrors()
	if state.Save(dir, st) != nil {
		return errors.New("could not reset primary observations")
	}
	target := filepath.Join(dir, "targets", "primary")
	for _, path := range []string{filepath.Join(dir, "targets"), target} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("primary cursor directory unavailable")
		}
	}
	path := filepath.Join(target, "bb-cursor.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("primary cursor unavailable")
	}
	if os.Remove(path) != nil {
		return errors.New("could not clear primary cursor")
	}
	return nil
}

// Like init, refuse mutation inside a checkout, including symlinked ancestors.
func transferPrivatePaths(path string, cfg *config.Config) error {
	bad := errors.New("transfer configuration and runtime state must be private paths outside checkouts")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return bad
	}
	configPath, err := canonicalFuturePath(path)
	if err != nil {
		return bad
	}
	dir, err := canonicalFuturePath(cfg.Loop.StateDir)
	if err != nil {
		return bad
	}
	for _, dest := range []string{filepath.Dir(configPath), dir} {
		for p := dest; ; p = filepath.Dir(p) {
			if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil || !errors.Is(err, os.ErrNotExist) {
				return bad
			}
			if filepath.Dir(p) == p {
				break
			}
		}
		for _, repo := range cfg.Loop.Progress.Repositories {
			root, err := canonicalFuturePath(repo)
			if err != nil || pathWithin(root, dest) {
				return bad
			}
		}
	}
	return nil
}
