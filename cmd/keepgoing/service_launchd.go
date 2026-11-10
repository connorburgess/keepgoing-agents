package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
	"unicode"
	"unicode/utf8"
)

type serviceHost struct {
	platform, home, executable, searchPath string
	uid                                    int
	configHome                             string
	query                                  func(context.Context, ...string) ([]byte, int, error)
	run                                    func(context.Context, ...string) (int, error)
}

func localServiceHost() serviceHost {
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	h := serviceHost{platform: runtime.GOOS, home: home, executable: exe, searchPath: os.Getenv("PATH"), uid: os.Getuid(), run: launchctlRun, configHome: os.Getenv("XDG_CONFIG_HOME"), query: systemctlQuery}
	if h.platform == "linux" {
		h.run = func(ctx context.Context, args ...string) (int, error) {
			_, code, err := systemctlQuery(ctx, args...)
			return code, err
		}
	}
	return h
}

// Only numeric exit status is consumed; launchctl output may contain private values.
func launchctlRun(ctx context.Context, args ...string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "/bin/launchctl", args...).Run()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return -1, errors.New("launchctl could not be executed")
}

type servicePlan struct {
	SchemaVersion int    `json:"schemaVersion"`
	Backend       string `json:"backend"`
	UID           int    `json:"uid"`
	Label         string `json:"label"`
	Definition    string `json:"definition"`
	StateDir      string `json:"stateDir"`
	Config        string `json:"config"`
	Executable    string `json:"executable"`
	SearchPath    string `json:"searchPath"` // private record only; never in reports
}

func serviceIdentity(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return fmt.Sprintf("org.keepgoing.loop.%x", sum[:16])
}

func newServicePlan(h serviceHost, dir, cfg string) (servicePlan, error) {
	p := servicePlan{SchemaVersion: 1, Backend: h.backend(), UID: h.uid, Label: serviceIdentity(dir), StateDir: dir, Config: cfg, Executable: h.executable, SearchPath: h.searchPath}
	if h.platform == "linux" {
		p.Label += ".service"
	}
	p.Definition = filepath.Join(h.definitionDir(), p.Label+h.definitionSuffix())
	return p, validateServicePlan(h, dir, p)
}

func validateServicePlan(h serviceHost, dir string, p servicePlan) error {
	label := serviceIdentity(dir)
	if h.platform == "linux" {
		label += ".service"
	}
	if p.SchemaVersion != 1 || p.Backend != h.backend() || p.UID != h.uid || p.StateDir != dir || p.Label != label || p.Definition != filepath.Join(h.definitionDir(), label+h.definitionSuffix()) {
		return errors.New("service record does not match this user and state directory")
	}
	for _, s := range []string{h.home, h.definitionDir(), p.Config, p.Executable, p.StateDir, p.Definition} {
		if !filepath.IsAbs(s) || filepath.Clean(s) != s {
			return errors.New("service paths must be clean absolute paths")
		}
	}
	for _, s := range []string{p.Config, p.Executable, p.StateDir, p.Definition, p.SearchPath} {
		if !utf8.ValidString(s) || containsServiceControl(s) {
			return errors.New("service paths and PATH must not contain control characters")
		}
	}
	return nil
}

func launchdDefinition(p servicePlan) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n")
	var err error
	str := func(s string) {
		b.WriteString("<string>")
		if e := xml.EscapeText(&b, []byte(s)); e != nil {
			err = e
		}
		b.WriteString("</string>\n")
	}
	b.WriteString("<key>Label</key>")
	str(p.Label)
	b.WriteString("<key>ProgramArguments</key><array>\n")
	for _, s := range []string{p.Executable, "__service-daemon", "-c", p.Config, "--state-dir", p.StateDir} {
		str(s)
	}
	b.WriteString("</array><key>WorkingDirectory</key>")
	str(filepath.Dir(p.Config))
	b.WriteString("<key>EnvironmentVariables</key><dict><key>PATH</key>")
	str(p.SearchPath)
	b.WriteString("<key>BB_THREAD_ID</key><string></string></dict>\n")
	b.WriteString("<key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n<key>ThrottleInterval</key><integer>30</integer>\n")
	b.WriteString("<key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string>\n</dict></plist>\n")
	return b.Bytes(), err
}

func (p servicePlan) domain() string { return fmt.Sprintf("gui/%d", p.UID) }
func (p servicePlan) target() string { return p.domain() + "/" + p.Label }

func serviceRegistration(ctx context.Context, h serviceHost, p servicePlan) string {
	if p.Backend == "systemd" {
		return inspectSystemd(ctx, h, p).registration
	}
	code, err := h.run(ctx, "print", p.target())
	if err == nil && code == 0 {
		return "loaded"
	}
	// Observed launchctl service-not-found code. Confirm the GUI domain is
	// accessible; all other errors remain unknown, never assumed absent.
	if err == nil && code == 113 {
		code, err = h.run(ctx, "print", p.domain())
		if err == nil && code == 0 {
			return "absent"
		}
	}
	return "unknown"
}

func serviceManager(ctx context.Context, h serviceHost, args ...string) error {
	code, err := h.run(ctx, args...)
	if err != nil {
		return fmt.Errorf("%s %s unavailable or timed out", h.backend(), args[0])
	}
	if code != 0 {
		return fmt.Errorf("%s %s failed (exit %d)", h.backend(), args[0], code)
	}
	return nil
}

func containsServiceControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
