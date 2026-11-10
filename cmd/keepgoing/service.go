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

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

const serviceRecordName = "service-launchd.json"

type serviceReport struct {
	Backend           string        `json:"backend"`
	Operation         string        `json:"operation"`
	DryRun            bool          `json:"dryRun"`
	Recorded          bool          `json:"recorded"`
	DefinitionPresent bool          `json:"definitionPresent"`
	Label             string        `json:"label"`
	Definition        string        `json:"definition"`
	Registration      string        `json:"registration"`
	Message           string        `json:"message"`
	ActiveState       string        `json:"activeState,omitempty"`
	UnitFileState     string        `json:"unitFileState,omitempty"`
	Daemon            *daemonStatus `json:"daemon,omitempty"`
}

func serviceCommand(e env, args []string, h serviceHost) int {
	if len(args) == 0 {
		e.errf("usage: keepgoing service install|remove|status [-c config] [--dry-run] [--json]")
		return exitUsage
	}
	op, args := args[0], args[1:]
	if op != "install" && op != "remove" && op != "status" {
		e.errf("service: expected install, remove, or status")
		return exitUsage
	}
	fs := flag.NewFlagSet("service "+op, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	cfgPath := fs.String("c", "keepgoing.yaml", "path to config")
	statePath := fs.String("state-dir", "", "remove/status: locate the recorded service even when config is broken")
	dry := fs.Bool("dry-run", false, "preview local changes without contacting the service manager or writing files")
	asJSON := fs.Bool("json", false, "print structured service status")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	explicitConfig := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "c" {
			explicitConfig = true
		}
	})
	if fs.NArg() != 0 || (*statePath != "" && (op == "install" || explicitConfig)) || (*dry && op == "status") {
		e.errf("service: --state-dir is for remove/status instead of -c; --dry-run is for install/remove; no positional arguments")
		return exitUsage
	}
	if (h.platform != "darwin" && h.platform != "linux") || h.uid < 0 || h.home == "" {
		e.errf("service: user-service management currently supports macOS GUI LaunchAgents and Linux systemd user services; use start or an external scheduler on this platform")
		return exitFailed
	}
	var cfg *config.Config
	var err error
	var dir, path string
	if *statePath != "" {
		dir = *statePath
	} else {
		path, err = filepath.Abs(*cfgPath)
		if err == nil {
			cfg, err = config.Load(path)
		}
		if err != nil {
			e.errf("service: config cannot be read or validated; remove/status can use --state-dir to locate an existing record")
			return exitUsage
		}
		dir = cfg.Loop.StateDir
	}
	dir, err = canonicalFuturePath(dir)
	if err != nil {
		e.errf("service: cannot resolve state directory")
		return exitFailed
	}
	if op == "install" {
		if _, err := exec.LookPath(h.executable); err != nil {
			e.errf("service: current executable is unavailable; install a stable keepgoing binary first")
			return exitFailed
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			e.errf("service: cannot resolve config file")
			return exitFailed
		}
	}
	// Keep files outside checkouts, including existing ancestor symlinks.
	for _, p := range []string{dir, h.definitionDir()} {
		if err := serviceOutsideCheckout(p); err != nil {
			e.errf("service: %v", err)
			return exitFailed
		}
	}
	if !*dry && op != "status" {
		l, err := lock.AcquireNamed(dir, lock.Service)
		if err != nil {
			return ownershipError(e, err)
		}
		defer l.Release()
	}
	if op == "install" {
		// Read-only owner check. Installation rechecks under tick.lock before any
		// file is written. Removal only restricts the recorded scheduler.
		if err := checkOwnership(dir, nil); err != nil {
			return ownershipError(e, err)
		}
	}
	p, exists, err := readServicePlan(h, dir)
	if err != nil {
		e.errf("service: %v", err)
		return exitFailed
	}
	if !exists {
		p, err = newServicePlan(h, dir, path)
		// Removal/status need only the deterministic identity, not an executable.
		if op != "install" {
			p.Config = filepath.Join(dir, "unused.yaml")
			p.Executable = filepath.Join(dir, "unused")
			err = validateServicePlan(h, dir, p)
		}
		if err != nil {
			e.errf("service: %v", err)
			return exitFailed
		}
	}
	r := serviceReport{Backend: p.Backend, Operation: op, DryRun: *dry, Recorded: exists, Label: p.Label, Definition: p.Definition, Registration: "not_checked"}
	definition, err := serviceDefinition(p)
	if err != nil {
		e.errf("service: cannot render definition")
		return exitFailed
	}
	actual, present, err := readServiceFile(p.Definition)
	if err != nil {
		e.errf("service: %v", err)
		return exitFailed
	}
	r.DefinitionPresent = present
	if present && (!exists || !bytes.Equal(actual, definition)) {
		e.errf("service: definition is unrecorded or modified; refusing to manage it")
		return exitFailed
	}
	if op == "status" {
		if p.Backend == "systemd" {
			ss := inspectSystemd(e.ctx, h, p)
			r.Registration, r.ActiveState, r.UnitFileState = ss.registration, ss.active, ss.unitFile
		} else {
			r.Registration = serviceRegistration(e.ctx, h, p)
		}
		d := inspectDaemon(e.ctx, dir, version, false)
		r.Daemon = &d
		r.Message = "Registration is separate from daemon liveness; a responding daemon is not proven to belong to this service. Disabled overrides and other timers are not inspected."
		if p.Backend == "systemd" {
			r.Message = "Unit properties are separate from loop daemon liveness; a responding daemon is not proven to belong to this service. Unknown identity, overrides, or manager responses require inspection. Other timers are not inspected."
		}
		code := exitOK
		if r.Registration == "unknown" || (r.Registration == "loaded" && !exists) {
			code = exitFailed
		}
		return printService(e, r, *asJSON, code)
	}
	if op == "remove" && !exists {
		e.errf("service: no managed record; no service or file was removed")
		return exitFailed
	}
	if *dry {
		if op == "install" && exists {
			e.errf("service: already recorded; use status or remove before reinstalling")
			return exitFailed
		}
		if op == "install" {
			if err := startPreflight(e.ctx, cfg); err != nil {
				e.errf("service: %v", err)
				return exitFailed
			}
		}
		r.Message = "Would create private service files and enable/bootstrap the LaunchAgent. No launchctl calls or file changes were made; loop controls and ownership remain unchanged."
		if op == "remove" {
			r.Message = "Would disable/bootout the recorded LaunchAgent, verify absence, and remove its files. No launchctl calls or file changes were made; loop controls and ownership remain unchanged."
		}
		if p.Backend == "systemd" {
			r.Message = "Would install/reload/enable/start the recorded user unit; remove would disable/stop it, remove its definition, reload, and verify absence. No manager calls or file changes were made; controls and ownership remain unchanged."
		}
		return printService(e, r, *asJSON, exitOK)
	}
	if op == "install" {
		return installService(e, h, cfg, p, exists, definition, r, *asJSON)
	}
	if p.Backend == "systemd" {
		return removeSystemdService(e, h, p, definition, r, *asJSON)
	}
	// Disable first: a partial removal must not reload on next login. Only this
	// exact, locally recorded definition can be removed; no wildcard bootout.
	if err := serviceManager(e.ctx, h, "disable", p.target()); err != nil {
		e.errf("service: %v; removal incomplete, files retained", err)
		return exitFailed
	}
	loaded := serviceRegistration(e.ctx, h, p)
	if loaded == "unknown" {
		e.errf("service: registration unknown; removal incomplete, files retained")
		return exitFailed
	}
	if loaded == "loaded" {
		if err := serviceManager(e.ctx, h, "bootout", p.target()); err != nil {
			e.errf("service: %v; removal incomplete, files retained", err)
			return exitFailed
		}
	}
	if serviceRegistration(e.ctx, h, p) != "absent" {
		e.errf("service: service absence not verified; removal incomplete, files retained")
		return exitFailed
	}
	// Registration can disappear before a process has finished exiting. Retain
	// recovery files unless the loop's process lock is free as well. Never kill
	// a PID inferred from a file or a separately launched daemon.
	daemonLock, err := waitServiceDaemonExit(e.ctx, dir)
	if err != nil {
		e.errf("service: daemon exit not verified; registration disabled, cleanup incomplete")
		return exitFailed
	}
	defer daemonLock.Release()
	// Recheck both files before deletion; edited definitions remain for review.
	if err := removeServiceFiles(h, dir, p, definition); err != nil {
		e.errf("service: %v; registration disabled, cleanup incomplete", err)
		return exitFailed
	}
	r.Recorded, r.DefinitionPresent, r.Registration = false, false, "absent"
	r.Message = "Managed service disabled, unloaded, and removed. Loop controls are unchanged; no agent stop command was sent. Other schedulers were not removed."
	return printService(e, r, *asJSON, exitOK)
}

func installService(e env, h serviceHost, cfg *config.Config, p servicePlan, exists bool, definition []byte, r serviceReport, asJSON bool) int {
	if exists {
		e.errf("service: already recorded; use status or remove before reinstalling")
		return exitFailed
	}
	daemonLock, err := lock.AcquireNamed(p.StateDir, lock.Daemon)
	if err != nil {
		e.errf("service: a daemon is running or its lock is unavailable; end it before installing")
		if errors.Is(err, lock.ErrHeld) {
			return exitSkipped
		}
		return exitFailed
	}
	defer daemonLock.Release()
	tick, err := lock.Acquire(p.StateDir)
	if err != nil {
		return ownershipError(e, err)
	}
	defer tick.Release()
	if err := checkOwnership(p.StateDir, nil); err != nil {
		return ownershipError(e, err)
	}
	if err := startPreflight(e.ctx, cfg); err != nil {
		e.errf("service: %v", err)
		return exitFailed
	}
	if serviceRegistration(e.ctx, h, p) != "absent" {
		e.errf("service: existing or unknown registration; nothing installed")
		return exitFailed
	}
	if e.ctx.Err() != nil {
		e.errf("service: cancelled")
		return exitFailed
	}
	// Save the recovery record before activating anything. If a later step
	// fails it stays available to status/remove, including after config damage.
	record, _ := json.MarshalIndent(p, "", "  ")
	if len(record) > 65536 || len(definition) > 65536 {
		e.errf("service: generated service files exceed 64 KiB; nothing installed")
		return exitFailed
	}
	if err := writeServiceFile(filepath.Join(p.StateDir, h.recordName()), record); err != nil {
		e.errf("service: cannot create private service record")
		return exitFailed
	}
	if err := writeServiceFile(p.Definition, definition); err != nil {
		e.errf("service: cannot create definition; installation incomplete, record retained")
		return exitFailed
	}
	// The launched daemon must acquire these itself. service.lock serializes
	// installers/removers; daemon.lock prevents duplicate supervision processes.
	_ = tick.Release()
	_ = daemonLock.Release()
	if p.Backend == "systemd" {
		return activateSystemdService(e, h, p, r, asJSON)
	}
	for _, args := range [][]string{{"enable", p.target()}, {"bootstrap", p.domain(), p.Definition}} {
		if err := serviceManager(e.ctx, h, args...); err != nil {
			e.errf("service: %v; installation incomplete, files retained; use service status/remove", err)
			return exitFailed
		}
	}
	r.Registration = serviceRegistration(e.ctx, h, p)
	r.Recorded, r.DefinitionPresent = true, true
	if r.Registration != "loaded" {
		r.Message = "Files installed, but registration not verified; inspect service status or remove."
		return printService(e, r, asJSON, exitFailed)
	}
	r.Message = "LaunchAgent installed and registered. Loop controls and ownership are unchanged; use enable explicitly if disabled. Registration does not establish a healthy daemon or provider."
	return printService(e, r, asJSON, exitOK)
}

func printService(e env, r serviceReport, asJSON bool, code int) int {
	if asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
	} else {
		fmt.Fprintf(e.stdout, "service: %s; recorded %t; definition present %t; registration %s\n%s\n%s\n", r.Label, r.Recorded, r.DefinitionPresent, r.Registration, r.Definition, r.Message)
		if r.ActiveState != "" {
			fmt.Fprintf(e.stdout, "unit: active %s; enablement %s\n", r.ActiveState, r.UnitFileState)
		}
		if r.Daemon != nil {
			fmt.Fprintf(e.stdout, "loop daemon: %s; %s\n", r.Daemon.State, r.Daemon.NextCheck)
		}
	}
	return code
}

func readServicePlan(h serviceHost, dir string) (servicePlan, bool, error) {
	var p servicePlan
	b, exists, err := readServiceFile(filepath.Join(dir, h.recordName()))
	if err != nil || !exists {
		return p, exists, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, true, errors.New("invalid service record; raw values withheld")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, true, errors.New("invalid trailing service record data")
	}
	return p, true, validateServicePlan(h, dir, p)
}

func readServiceFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.New("service file inaccessible")
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, true, errors.New("service file must be a regular file of at most 64 KiB; symlinks are refused")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, errors.New("service file cannot be read")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, true, errors.New("service file changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return nil, true, errors.New("service file cannot be read within the size limit")
	}
	return b, true, nil
}

func writeServiceFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func removeServiceFiles(h serviceHost, dir string, p servicePlan, definition []byte) error {
	current, exists, err := readServicePlan(h, dir)
	if err != nil || !exists || current != p {
		return errors.New("service record changed during removal")
	}
	b, present, err := readServiceFile(p.Definition)
	if err != nil || (present && !bytes.Equal(b, definition)) {
		return errors.New("service definition changed during removal")
	}
	if present {
		if err := os.Remove(p.Definition); err != nil {
			return errors.New("cannot remove service definition")
		}
	}
	if err := os.Remove(filepath.Join(dir, h.recordName())); err != nil {
		return errors.New("cannot remove service record")
	}
	return nil
}

func serviceOutsideCheckout(path string) error {
	p, err := canonicalFuturePath(path)
	if err != nil {
		return errors.New("cannot resolve service destination")
	}
	for ; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return errors.New("service runtime files must be outside Git checkouts")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect service destination")
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

// Pin the service to its recorded state directory even if someone edits the
// config before a crash/reboot restart. Ordinary daemon reload guards still apply.
func serviceDaemon(e env, args []string) int {
	fs := flag.NewFlagSet("service daemon", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	path := fs.String("c", "", "config path")
	expected := fs.String("state-dir", "", "expected state directory")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || !filepath.IsAbs(*path) || !filepath.IsAbs(*expected) {
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("service: configuration unavailable")
		return exitFailed
	}
	actual, err := canonicalFuturePath(cfg.Loop.StateDir)
	if err != nil || actual != *expected {
		e.errf("service: state directory changed; remove and reinstall the service explicitly")
		return exitFailed
	}
	return daemon(e, cfg, *path, false)
}

func waitServiceDaemonExit(parent context.Context, dir string) (*lock.Lock, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	timer := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		l, err := lock.AcquireNamed(dir, lock.Daemon)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, lock.ErrHeld) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
