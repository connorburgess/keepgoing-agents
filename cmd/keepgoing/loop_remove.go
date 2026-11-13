package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
)

// loopMarkerName is the file `init` writes into a setup directory. It is the
// only thing that lets `loop remove` delete that directory: a path named by a
// configuration value is never enough.
const loopMarkerName = ".keepgoing-loop"

// loopMarker says a directory is a loop setup that keepgoing created.
type loopMarker struct {
	SchemaVersion int    `json:"schemaVersion"`
	Type          string `json:"type"`
	Loop          string `json:"loop"`
	CreatedBy     string `json:"createdBy"`
}

func newLoopMarker(loop string) []byte {
	b, _ := json.MarshalIndent(loopMarker{SchemaVersion: 1, Type: "keepgoing_loop_setup", Loop: loop, CreatedBy: version}, "", "  ")
	return append(b, '\n')
}

// readLoopMarker reads a setup directory's marker: a small regular file, not
// a symlink, naming the loop.
func readLoopMarker(dir, loop string) error {
	p := filepath.Join(dir, loopMarkerName)
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("the setup directory carries no keepgoing marker, so keepgoing cannot tell that it created it")
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return errors.New("the setup directory's keepgoing marker is not a small regular file")
	}
	b, err := os.ReadFile(p)
	var m loopMarker
	if err != nil || json.Unmarshal(b, &m) != nil || m.SchemaVersion != 1 || m.Type != "keepgoing_loop_setup" {
		return errors.New("the setup directory's keepgoing marker is not one keepgoing wrote")
	}
	if m.Loop != loop {
		return errors.New("the setup directory's keepgoing marker names another loop")
	}
	return nil
}

// loopSetupFiles are the entries `init` and keepgoing write at the top of a
// setup directory. Anything else there is not keepgoing's to delete.
var loopSetupFiles = []string{loopMarkerName, "keepgoing.yaml", "fallbacks.yaml", "prompt.md", "task.md"}

type loopRemovalItem struct {
	Kind string `json:"kind"` // state_directory, registry_entry, configuration_directory
	// What says what the item is without a private path.
	What string `json:"what"`
	Path string `json:"path,omitempty"` // with --private only
}

type loopRefusal struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	// Force says --force lifts this refusal.
	Force bool `json:"force"`
}

type loopRemovalFailure struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type loopRemovalReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	Type          string `json:"type"`
	Loop          string `json:"loop"`
	// Applied is false for a preview and for a refusal.
	Applied bool `json:"applied"`
	// Complete says everything listed was removed.
	Complete bool                 `json:"complete"`
	Items    []loopRemovalItem    `json:"items"`
	Refusals []loopRefusal        `json:"refusals"`
	Removed  []string             `json:"removed"`
	Failed   []loopRemovalFailure `json:"failed"`
	// Recorded says the removal log was written.
	Recorded bool   `json:"recorded"`
	Message  string `json:"message"`
}

// Swapped in tests to make one step fail.
var (
	loopRemoveAll = os.RemoveAll
	loopRemove    = os.Remove
)

func loopCommand(e env, args []string) int {
	if len(args) == 0 || args[0] != "remove" {
		e.errf("usage: keepgoing loop remove [-c keepgoing.yaml] [--yes] [--force] [--json] [--private] [--registry DIR]")
		return exitUsage
	}
	fs := flag.NewFlagSet("loop remove", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	cfgPath := fs.String("c", "keepgoing.yaml", "path to the config file")
	yes := fs.Bool("yes", false, "remove what the preview lists; without it nothing is removed")
	force := fs.Bool("force", false, "remove a loop that is still switched on or held for a person; never one with anything running")
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	private := fs.Bool("private", false, "show private paths, and keep them in the removal record")
	registryDir := fs.String("registry", "", "private registry directory (default: the user's)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		if err == nil {
			e.errf("loop remove takes no arguments; name the loop with -c")
		}
		return exitUsage
	}
	return loopRemoveCmd(e, *cfgPath, *registryDir, *yes, *force, *asJSON, *private)
}

// loopPlan is what a removal would do and why it may not.
type loopPlan struct {
	cfg        *config.Config
	configPath string
	setupDir   string
	stateDir   string // "" when it does not exist
	registry   string
	entry      *registry.Entry
	files      []string // known files present in the setup directory
	items      []loopRemovalItem
	refusals   []loopRefusal
}

func loopRemoveCmd(e env, cfgPath, registryDir string, yes, force, asJSON, private bool) int {
	plan, err := planLoopRemoval(e.ctx, cfgPath, registryDir, private)
	if err != nil {
		e.errf("loop remove: %v", err)
		return exitUsage
	}
	report := loopRemovalReport{SchemaVersion: 1, Type: "loop_removal", Loop: plan.cfg.Loop.Name, Items: plan.items, Refusals: []loopRefusal{}, Removed: []string{}, Failed: []loopRemovalFailure{}}
	blocked := false
	for _, r := range plan.refusals {
		if !(r.Force && force) {
			report.Refusals = append(report.Refusals, r)
			blocked = true
		}
	}
	emit := func(code int) int {
		if asJSON {
			b, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(e.stdout, string(b))
			return code
		}
		fmt.Fprintf(e.stdout, "%s: %s\n", report.Loop, report.Message)
		for _, item := range report.Items {
			line := "  " + item.What
			if item.Path != "" {
				line += ": " + item.Path
			}
			fmt.Fprintln(e.stdout, line)
		}
		for _, r := range report.Refusals {
			fmt.Fprintf(e.stdout, "  refused: %s\n", r.Reason)
		}
		for _, f := range report.Failed {
			fmt.Fprintf(e.stdout, "  not removed: %s: %s\n", f.Kind, f.Reason)
		}
		return code
	}
	switch {
	case blocked:
		report.Message = "not removed; nothing was changed"
		return emit(exitFailed)
	case !yes:
		report.Message = "preview only, nothing was removed. This would remove, and cannot be undone:"
		if len(report.Items) == 0 {
			report.Message = "preview only: there is nothing left to remove"
		}
		return emit(exitOK)
	}
	applyLoopRemoval(plan, &report, private)
	if report.Complete {
		report.Message = "removed. The agent's conversation and the work repository were not touched."
		return emit(exitOK)
	}
	report.Message = "partly removed. What is listed as removed is gone; run the same command again to remove the rest."
	return emit(exitFailed)
}

// planLoopRemoval reads everything and changes nothing: it creates no
// directory, lock file or record.
func planLoopRemoval(ctx context.Context, cfgPath, registryDir string, private bool) (*loopPlan, error) {
	path, err := filepath.Abs(cfgPath)
	if err != nil {
		return nil, errors.New("the configuration path cannot be resolved")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("the configuration could not be loaded, so what belongs to the loop is not known: %v", err)
	}
	p := &loopPlan{cfg: cfg, configPath: path, setupDir: filepath.Dir(path)}
	refuse := func(code string, forceable bool, format string, args ...any) {
		p.refusals = append(p.refusals, loopRefusal{Code: code, Reason: fmt.Sprintf(format, args...), Force: forceable})
	}
	show := func(path string) string {
		if private {
			return path
		}
		return ""
	}

	// The setup directory: a real directory keepgoing marked as its own,
	// outside any checkout, holding nothing but what keepgoing wrote.
	info, err := os.Lstat(p.setupDir)
	switch {
	case err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		refuse("not_a_directory", false, "the setup directory is a symlink or not a directory")
	default:
		if err := readLoopMarker(p.setupDir, cfg.Loop.Name); err != nil {
			refuse("no_marker", false, "%v; nothing unmarked is removed", err)
		}
	}
	for dir := p.setupDir; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			refuse("inside_checkout", false, "the setup directory is inside a Git checkout")
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	state := cfg.Loop.StateDir
	nested := filepath.Join(p.setupDir, "state")
	if sinfo, err := os.Lstat(state); err == nil {
		real, rerr := filepath.EvalSymlinks(state)
		realSetup, serr := filepath.EvalSymlinks(p.setupDir)
		switch {
		case !sinfo.IsDir() || sinfo.Mode()&os.ModeSymlink != 0:
			refuse("state_not_a_directory", false, "the state directory is a symlink or not a directory")
		case filepath.Clean(state) != nested || rerr != nil || serr != nil || real != filepath.Join(realSetup, "state"):
			refuse("state_outside_setup", false, "the state directory is not the one inside the setup directory, and keepgoing did not mark it as its own")
		default:
			p.stateDir = state
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		refuse("state_unreadable", false, "the state directory cannot be inspected")
	} else if filepath.Clean(state) != nested {
		// Nothing exists there, and it is not ours to look for.
		state = ""
	}

	known := map[string]bool{"state": true}
	for _, name := range loopSetupFiles {
		known[name] = true
	}
	entries, _ := os.ReadDir(p.setupDir)
	var unexpected []string
	for _, entry := range entries {
		switch {
		case !known[entry.Name()]:
			unexpected = append(unexpected, entry.Name())
		case entry.Name() == "state":
		case !entry.Type().IsRegular():
			unexpected = append(unexpected, entry.Name())
		default:
			p.files = append(p.files, entry.Name())
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		detail := fmt.Sprintf("%d entries", len(unexpected))
		if private {
			detail = strings.Join(unexpected, ", ")
		}
		refuse("unexpected_entries", false, "the setup directory holds entries keepgoing did not write (%s); move them out first", detail)
	}

	// Nothing may be running. Only locks that already exist are tried, so a
	// preview creates nothing.
	if p.stateDir != "" {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		if _, err := daemoninfo.Query(probe, p.stateDir); err == nil {
			refuse("daemon_running", false, "a daemon is responding for this loop; stop it first")
		}
		cancel()
		for _, l := range []struct{ name, code, reason string }{
			{lock.Daemon, "daemon_lock_held", "a daemon or service holds this loop's daemon lock; stop it first"},
			{lock.Tick, "check_running", "a check is running; try again when it has finished"},
		} {
			if _, err := os.Lstat(filepath.Join(p.stateDir, l.name+".lock")); err != nil {
				continue
			}
			held, err := lock.AcquireNamed(p.stateDir, l.name)
			if err != nil {
				refuse(l.code, false, "%s", l.reason)
				continue
			}
			held.Release()
		}
		targets, _ := os.ReadDir(filepath.Join(p.stateDir, "targets"))
		for _, t := range targets {
			if running, err := runner.Dir(filepath.Join(p.stateDir, "targets", t.Name())).Running(); err != nil || running {
				refuse("turn_running", false, "a turn is in progress on target %s, or its record cannot be read; wait for it or stop it first", t.Name())
			}
		}
		if records, _ := filepath.Glob(filepath.Join(p.stateDir, "service-*.json")); len(records) > 0 {
			refuse("service_installed", false, "a managed service is installed for this loop; run keepgoing service remove first")
		}
		if err := checkOwnership(p.stateDir, nil); err != nil {
			refuse("not_owner", false, "ownership: %v", err)
		}
		if ctl, err := control.Load(p.stateDir); err != nil {
			refuse("control_unreadable", false, "the loop's control file cannot be read")
		} else {
			if ctl.PendingTransfer != nil {
				refuse("transfer_pending", false, "a BB transfer is pending; finish or recover it first")
			}
			if ctl.Enabled {
				refuse("enabled", true, "the loop is switched on; run keepgoing disable first, or use --force")
			}
			if ctl.AwaitingUser {
				refuse("awaiting_user", true, "the loop is held for a person with an open question; settle it first, or use --force")
			}
		}
	}

	// What would go, in the order it is removed.
	if p.stateDir != "" {
		p.items = append(p.items, loopRemovalItem{Kind: "state_directory", What: "the state directory inside the setup directory, with everything keepgoing recorded for this loop", Path: show(p.stateDir)})
	}
	if dir, err := registryDirectory(registryDir); err == nil {
		p.registry = dir
		if f, err := registry.Load(dir); err == nil {
			for _, entry := range f.Entries {
				if entry.ConfigPath == path {
					entry := entry
					p.entry = &entry
					p.items = append(p.items, loopRemovalItem{Kind: "registry_entry", What: "the registry entry " + entry.ID})
				}
			}
		}
	}
	if err == nil && info.IsDir() {
		sort.Strings(p.files)
		p.items = append(p.items, loopRemovalItem{Kind: "configuration_directory", What: "the setup directory and its files: " + strings.Join(p.files, ", "), Path: show(p.setupDir)})
	}
	return p, nil
}

// applyLoopRemoval removes the state directory, then the registry entry,
// then the setup directory's own files and the directory. The configuration
// goes last, so a partial failure can be retried with the same command.
func applyLoopRemoval(p *loopPlan, report *loopRemovalReport, private bool) {
	report.Applied = true
	fail := func(kind string, err error) {
		reason := "could not be removed"
		if private && err != nil {
			reason = err.Error()
		}
		report.Failed = append(report.Failed, loopRemovalFailure{Kind: kind, Reason: reason})
	}
	if p.stateDir != "" {
		if err := loopRemoveAll(p.stateDir); err != nil {
			fail("state_directory", err)
		} else {
			report.Removed = append(report.Removed, "state_directory")
		}
	}
	if p.entry != nil && len(report.Failed) == 0 {
		if err := registry.Remove(p.registry, p.entry.ID); err != nil {
			fail("registry_entry", err)
		} else {
			report.Removed = append(report.Removed, "registry_entry")
		}
	}
	if len(report.Failed) == 0 {
		// The configuration and then the marker go last, and removal stops
		// at the first failure: while those two are there, the same command
		// can find the loop and finish the job.
		var order []string
		for _, name := range p.files {
			if name != "keepgoing.yaml" && name != loopMarkerName {
				order = append(order, name)
			}
		}
		order = append(order, "keepgoing.yaml", loopMarkerName)
		var failed error
		for _, name := range order {
			if err := loopRemove(filepath.Join(p.setupDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				failed = err
				break
			}
		}
		if failed == nil {
			// Never recursive: a directory that is not empty is left.
			failed = loopRemove(p.setupDir)
		}
		if failed != nil {
			fail("configuration_directory", failed)
		} else {
			report.Removed = append(report.Removed, "configuration_directory")
		}
	}
	report.Complete = len(report.Failed) == 0
	report.Recorded = recordLoopRemoval(p, report, private)
}

// recordLoopRemoval appends to a removal log in the user's private registry
// directory, where the record survives the loop. It holds who ran it and
// what was removed; paths only on request.
func recordLoopRemoval(p *loopPlan, report *loopRemovalReport, private bool) bool {
	if p.registry == "" {
		return false
	}
	if err := os.MkdirAll(p.registry, 0o700); err != nil {
		return false
	}
	record := map[string]any{"schemaVersion": 1, "at": time.Now().UTC().Format(time.RFC3339), "loop": report.Loop, "by": "terminal",
		"removed": report.Removed, "complete": report.Complete, "build": version}
	if caller := os.Getenv("BB_THREAD_ID"); caller != "" {
		record["by"], record["thread"] = "thread", caller
	}
	if p.entry != nil {
		record["registryId"] = p.entry.ID
	}
	if private {
		record["configPath"] = p.configPath
	}
	b, err := json.Marshal(record)
	if err != nil {
		return false
	}
	f, err := os.OpenFile(filepath.Join(p.registry, "removals.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err == nil
}
