package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (h serviceHost) backend() string {
	if h.platform == "linux" {
		return "systemd"
	}
	return "launchd"
}
func (h serviceHost) recordName() string { return "service-" + h.backend() + ".json" }
func (h serviceHost) definitionDir() string {
	if h.platform == "linux" {
		base := h.configHome
		if base == "" {
			base = filepath.Join(h.home, ".config")
		}
		return filepath.Join(base, "systemd", "user")
	}
	return filepath.Join(h.home, "Library", "LaunchAgents")
}
func (h serviceHost) definitionSuffix() string {
	if h.platform == "linux" {
		return ""
	}
	return ".plist"
}
func serviceDefinition(p servicePlan) ([]byte, error) {
	if p.Backend == "systemd" {
		return systemdDefinition(p), nil
	}
	return launchdDefinition(p)
}

// systemd quoting is not shell quoting. Percent specifiers are escaped in all
// quoted directives; the ExecStart ':' prefix disables dollar/environment expansion.
func systemdQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(s) + `"`
}
func systemdDefinition(p servicePlan) []byte {
	args := []string{":" + p.Executable, "__service-daemon", "-c", p.Config, "--state-dir", p.StateDir}
	for i := range args {
		args[i] = systemdQuote(args[i])
	}
	// WorkingDirectory takes a raw path (not a quoted word). A trailing slash
	// prevents trailing spaces/backslashes from becoming unit-file syntax.
	return []byte("[Unit]\nDescription=keepgoing loop supervisor\n\n[Service]\nType=exec\nExecStart=" + strings.Join(args, " ") + "\nWorkingDirectory=" + strings.ReplaceAll(filepath.Dir(p.Config)+"/", "%", "%%") + "\nEnvironment=" + systemdQuote("PATH="+p.SearchPath) + " \"BB_THREAD_ID=\"\nRestart=on-failure\nRestartSec=30s\nTimeoutStopSec=10s\nKillMode=process\nStandardOutput=null\nStandardError=null\n\n[Install]\nWantedBy=default.target\n")
}

func systemctlQuery(ctx context.Context, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Absolute system executable, never a project-local command; no pager or
	// authorization prompt. doctorExec bounds stdout at 64 KiB and drops stderr.
	out, err := doctorExec(ctx, "/usr/bin/systemctl", append([]string{"--user", "--no-pager", "--no-ask-password"}, args...), nil, "")
	if ctx.Err() != nil {
		return nil, -1, errors.New("systemctl timed out or cancelled")
	}
	if err == nil {
		return out, 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out, exit.ExitCode(), nil
	}
	return nil, -1, errors.New("systemctl unavailable or output exceeded limit")
}

type systemdState struct{ registration, active, unitFile string }

// Only machine-readable show properties are interpreted. Identity, definition,
// empty drop-ins, and known states are required before any unit is managed.
func inspectSystemd(ctx context.Context, h serviceHost, p servicePlan) systemdState {
	unknown := systemdState{registration: "unknown"}
	out, code, err := h.query(ctx, "show", "--all", "--property=Id,LoadState,ActiveState,UnitFileState,FragmentPath,DropInPaths", p.Label)
	if err != nil || (code != 0 && code != 4) || len(out) > 65536 {
		return unknown
	}
	props := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return unknown
		}
		if _, dup := props[k]; dup {
			return unknown
		}
		switch k {
		case "Id", "LoadState", "ActiveState", "UnitFileState", "FragmentPath", "DropInPaths":
		default:
			return unknown
		}
		props[k] = v
	}
	if len(props) != 6 || props["Id"] != p.Label || props["DropInPaths"] != "" {
		return unknown
	}
	if props["LoadState"] == "not-found" && props["ActiveState"] == "inactive" && props["FragmentPath"] == "" && props["UnitFileState"] == "" {
		return systemdState{registration: "absent", active: "inactive"}
	}
	if code != 0 || props["LoadState"] != "loaded" {
		return unknown
	}
	// systemd may canonicalize an ancestor symlink in the file name.
	expected, e1 := canonicalFuturePath(p.Definition)
	actual, e2 := canonicalFuturePath(props["FragmentPath"])
	if !filepath.IsAbs(props["FragmentPath"]) || e1 != nil || e2 != nil || actual != expected {
		return unknown
	}
	switch props["ActiveState"] {
	case "active", "inactive", "failed", "activating", "deactivating", "reloading":
	default:
		return unknown
	}
	switch props["UnitFileState"] {
	case "enabled", "disabled":
	case "":
		// A removed file can remain cached until daemon-reload; allow only a
		// stopped unit so interrupted removal can finish conservatively.
		if _, present, err := readServiceFile(p.Definition); err != nil || present || props["ActiveState"] != "inactive" {
			return unknown
		}
	default:
		return unknown
	}
	return systemdState{"loaded", props["ActiveState"], props["UnitFileState"]}
}

func systemdFilesMatch(h serviceHost, p servicePlan) bool {
	current, exists, err := readServicePlan(h, p.StateDir)
	if err != nil || !exists || current != p {
		return false
	}
	b, present, err := readServiceFile(p.Definition)
	return err == nil && present && bytes.Equal(b, systemdDefinition(p))
}

func activateSystemdService(e env, h serviceHost, p servicePlan, r serviceReport, asJSON bool) int {
	fail := func() int {
		e.errf("service: installation incomplete; files retained; inspect service status/remove")
		return exitFailed
	}
	if err := serviceManager(e.ctx, h, "daemon-reload"); err != nil {
		e.errf("service: %v", err)
		return fail()
	}
	if s := inspectSystemd(e.ctx, h, p); s.registration != "loaded" || s.active != "inactive" || s.unitFile != "disabled" {
		return fail()
	}
	for _, args := range [][]string{{"enable", p.Label}, {"start", p.Label}} {
		// Recheck identity and overrides immediately before each mutation.
		if !systemdFilesMatch(h, p) || inspectSystemd(e.ctx, h, p).registration != "loaded" {
			return fail()
		}
		if err := serviceManager(e.ctx, h, args...); err != nil {
			e.errf("service: %v", err)
			return fail()
		}
	}
	s := inspectSystemd(e.ctx, h, p)
	r.Registration, r.ActiveState, r.UnitFileState = s.registration, s.active, s.unitFile
	r.Recorded, r.DefinitionPresent = true, true
	if s.registration != "loaded" || s.active != "active" || s.unitFile != "enabled" {
		return fail()
	}
	r.Message = "User unit installed, enabled, and started. Loop controls and ownership are unchanged; use enable explicitly if disabled. Manager state does not establish a healthy loop or provider."
	return printService(e, r, asJSON, exitOK)
}

// The record outlives definition deletion and the final daemon-reload so a
// failed last step can be retried with --state-dir even without a config/unit.
func removeSystemdService(e env, h serviceHost, p servicePlan, definition []byte, r serviceReport, asJSON bool) int {
	fail := func() int {
		e.errf("service: removal incomplete; recovery record retained; inspect service status/remove")
		return exitFailed
	}
	s := inspectSystemd(e.ctx, h, p)
	if s.registration == "unknown" {
		return fail()
	}
	_, present, err := readServiceFile(p.Definition)
	if err != nil {
		return fail()
	}
	if (s.registration == "loaded" && s.unitFile != "") || present {
		if err := serviceManager(e.ctx, h, "disable", p.Label); err != nil {
			e.errf("service: %v", err)
			return fail()
		}
	}
	s = inspectSystemd(e.ctx, h, p)
	if s.registration == "unknown" {
		return fail()
	}
	if s.registration == "loaded" {
		if s.unitFile != "disabled" && s.unitFile != "" {
			return fail()
		}
		if err := serviceManager(e.ctx, h, "stop", p.Label); err != nil {
			e.errf("service: %v", err)
			return fail()
		}
		s = inspectSystemd(e.ctx, h, p)
	}
	if s.registration != "absent" && (s.registration != "loaded" || s.active != "inactive" || (s.unitFile != "disabled" && s.unitFile != "")) {
		return fail()
	}
	// Also rule out the managed enable link after an interrupted deletion; never
	// follow or delete an unrelated link, including a dangling one.
	link := filepath.Join(filepath.Dir(p.Definition), "default.target.wants", p.Label)
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		return fail()
	}
	daemonLock, err := waitServiceDaemonExit(e.ctx, p.StateDir)
	if err != nil {
		e.errf("service: daemon exit not verified")
		return fail()
	}
	defer daemonLock.Release()
	current, exists, err := readServicePlan(h, p.StateDir)
	if err != nil || !exists || current != p {
		return fail()
	}
	b, present, err := readServiceFile(p.Definition)
	if err != nil || (present && !bytes.Equal(b, definition)) {
		return fail()
	}
	if present {
		if err := os.Remove(p.Definition); err != nil {
			return fail()
		}
	}
	if err := serviceManager(e.ctx, h, "daemon-reload"); err != nil {
		e.errf("service: %v", err)
		return fail()
	}
	if inspectSystemd(e.ctx, h, p).registration != "absent" {
		return fail()
	}
	// removeServiceFiles rechecks the recovery record and any reappearing file.
	if err := removeServiceFiles(h, p.StateDir, p, definition); err != nil {
		return fail()
	}
	r.Recorded, r.DefinitionPresent, r.Registration = false, false, "absent"
	r.ActiveState, r.UnitFileState = "inactive", ""
	r.Message = "Managed user unit disabled, stopped, and removed; manager reloaded and absence verified. Loop controls unchanged; no agent stop command sent. Other schedulers unchanged."
	return printService(e, r, asJSON, exitOK)
}
