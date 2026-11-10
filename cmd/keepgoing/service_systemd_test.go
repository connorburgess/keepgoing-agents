//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

type fakeSystemd struct {
	p                       servicePlan
	calls                   [][]string
	loaded, enabled, active bool
	fail                    string
	before                  func([]string)
	properties              map[string]string
	raw                     *string
}

func (f *fakeSystemd) query(ctx context.Context, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.before != nil {
		f.before(args)
	}
	if ctx.Err() != nil {
		return nil, -1, ctx.Err()
	}
	if args[0] == f.fail {
		return nil, 1, errors.New("PRIVATE_MANAGER_ERROR")
	}
	_, err := os.Stat(f.p.Definition)
	present := err == nil
	link := filepath.Join(filepath.Dir(f.p.Definition), "default.target.wants", f.p.Label)
	switch args[0] {
	case "show":
		execution := reflect.DeepEqual(args, []string{"show", "--all", "--property=" + systemdExecutionProperties, f.p.Label})
		if !execution && !reflect.DeepEqual(args, []string{"show", "--all", "--property=Id,LoadState,ActiveState,UnitFileState,FragmentPath,DropInPaths", f.p.Label}) {
			return nil, 1, errors.New("unexpected query")
		}
		if f.raw != nil {
			return []byte(*f.raw), 0, nil
		}
		// A show by exact name may load a definition even before daemon-reload.
		f.loaded = f.loaded || present
		props := map[string]string{"Id": f.p.Label, "LoadState": "not-found", "ActiveState": "inactive", "UnitFileState": "", "FragmentPath": "", "DropInPaths": ""}
		props["Result"], props["ExecMainCode"], props["ExecMainStatus"] = "success", "0", "0"
		if f.loaded {
			props["LoadState"], props["FragmentPath"] = "loaded", f.p.Definition
		}
		if present {
			props["UnitFileState"] = "disabled"
			if f.enabled {
				props["UnitFileState"] = "enabled"
			}
		}
		if f.active {
			props["ActiveState"] = "active"
		}
		for k, v := range f.properties {
			props[k] = v
		}
		var out strings.Builder
		keys := []string{"Id", "LoadState", "ActiveState", "UnitFileState", "FragmentPath", "DropInPaths"}
		if execution {
			keys = strings.Split(systemdExecutionProperties, ",")
		}
		for _, k := range keys {
			fmt.Fprintf(&out, "%s=%s\n", k, props[k])
		}
		return []byte(out.String()), 0, nil
	case "daemon-reload":
		f.loaded = present
	case "enable":
		f.enabled = true
		if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
			return nil, -1, err
		}
		if err := os.Symlink(f.p.Definition, link); err != nil {
			return nil, -1, err
		}
	case "disable":
		f.enabled = false
		os.Remove(link)
	case "start":
		f.active = true
	case "stop":
		f.active = false
	default:
		return nil, 1, errors.New("unexpected operation")
	}
	return nil, 0, nil
}
func newSystemdFixture(t *testing.T) (*serviceFixture, *fakeSystemd) {
	t.Helper()
	f := newServiceFixture(t)
	f.host.platform = "linux"
	p, err := newServicePlan(f.host, f.cfg.Loop.StateDir, f.f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeSystemd{p: p}
	f.host.query = m.query
	f.host.run = func(ctx context.Context, args ...string) (int, error) { _, c, e := m.query(ctx, args...); return c, e }
	return f, m
}
func TestSystemdLifecyclePreservesHolds(t *testing.T) {
	for _, hold := range []control.File{{}, {Enabled: true, Paused: true}, {Enabled: true, Stop: true}, {Enabled: true, Completed: true}, {Enabled: true, AwaitingUser: true}} {
		t.Run(hold.Summary(), func(t *testing.T) {
			f, m := newSystemdFixture(t)
			hold.Ownership = &control.Ownership{ThreadID: "thr_fixture_owner", Generation: 3}
			hold.Note = "fictional hold"
			t.Setenv("BB_THREAD_ID", "thr_fixture_owner")
			if err := control.Save(f.cfg.Loop.StateDir, hold); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(f.cfg.Loop.StateDir, control.FileName))
			f.installed(t)
			p := f.plan(t)
			if p.Backend != "systemd" || !m.active || !m.enabled {
				t.Fatal("not installed")
			}
			for _, path := range []string{p.Definition, filepath.Join(p.StateDir, f.host.recordName())} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("not private", err)
				}
			}
			n := len(m.calls)
			if c, _, _ := f.call(t, "install"); c != 1 || len(m.calls) != n {
				t.Fatal("duplicate install")
			}
			c, o, e := f.call(t, "status", "--json")
			var r serviceReport
			if c != 0 || json.Unmarshal([]byte(o), &r) != nil || r.Registration != "loaded" || r.ActiveState != "active" || r.UnitFileState != "enabled" || r.Daemon == nil || r.Daemon.State == "responsive" {
				t.Fatalf("status %d %s %s", c, o, e)
			}
			t.Setenv("BB_THREAD_ID", "thr_other")
			if c, o, e := f.call(t, "remove"); c != 0 {
				t.Fatalf("remove %d %s %s", c, o, e)
			}
			if m.active || m.enabled || m.loaded {
				t.Fatal("unit remains")
			}
			for _, path := range []string{p.Definition, filepath.Join(p.StateDir, f.host.recordName())} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("file remains")
				}
			}
			after, _ := os.ReadFile(filepath.Join(p.StateDir, control.FileName))
			if !bytes.Equal(before, after) || f.f.read("calls") != "" || f.f.read("probes") != "" {
				t.Fatal("controls/providers changed")
			}
			if c, o, e := f.call(t, "status", "--json"); c != 0 || !strings.Contains(o, `"registration": "absent"`) {
				t.Fatalf("absent %d %s %s", c, o, e)
			}
			t.Setenv("BB_THREAD_ID", "thr_fixture_owner")
			f.installed(t)
		})
	}
}
func TestSystemdPreviewNoWritesOrCommands(t *testing.T) {
	f, m := newSystemdFixture(t)
	if c, o, e := f.call(t, "install", "--dry-run"); c != 0 {
		t.Fatalf("preview %d %s %s", c, o, e)
	}
	if len(m.calls) != 0 {
		t.Fatal("manager queried")
	}
	for _, dir := range []string{f.host.home, f.cfg.Loop.StateDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("files written")
		}
	}
	f.installed(t)
	m.calls = nil
	if c, _, _ := f.call(t, "remove", "--dry-run"); c != 0 || len(m.calls) != 0 {
		t.Fatal("remove preview mutated")
	}
}
func TestSystemdInterruptedInstallRecovery(t *testing.T) {
	for _, step := range []string{"daemon-reload", "enable", "start"} {
		t.Run(step, func(t *testing.T) {
			f, m := newSystemdFixture(t)
			m.fail = step
			if c, _, e := f.call(t, "install"); c != 1 || strings.Contains(e, "PRIVATE_MANAGER") {
				t.Fatal("failed install or redaction", c, e)
			}
			p := f.plan(t)
			write(t, f.f.cfg, "invalid yaml: [", 0600)
			m.fail = ""
			var out, errs bytes.Buffer
			if c := serviceCommand(env{context.Background(), &out, &errs}, []string{"remove", "--state-dir", p.StateDir}, f.host); c != 0 {
				t.Fatalf("recovery %d %s", c, &errs)
			}
		})
	}
}
func TestSystemdInterruptedRemovalRecovery(t *testing.T) {
	for _, step := range []string{"disable", "stop", "daemon-reload"} {
		t.Run(step, func(t *testing.T) {
			f, m := newSystemdFixture(t)
			f.installed(t)
			p := f.plan(t)
			m.fail = step
			if c, _, _ := f.call(t, "remove"); c != 1 {
				t.Fatal("failed removal claimed success")
			}
			if _, err := os.Stat(filepath.Join(p.StateDir, f.host.recordName())); err != nil {
				t.Fatal("record removed")
			}
			if step == "daemon-reload" {
				if _, err := os.Stat(p.Definition); !os.IsNotExist(err) {
					t.Fatal("fixture did not reach deletion")
				}
			}
			m.fail = ""
			if c, o, e := f.call(t, "remove"); c != 0 {
				t.Fatalf("retry %d %s %s", c, o, e)
			}
		})
	}
}
func TestSystemdUnknownAndForeignUnitsNeverMutated(t *testing.T) {
	for _, entry := range []struct{ k, v string }{{"Id", "foreign.service"}, {"LoadState", "masked"}, {"ActiveState", "future"}, {"UnitFileState", "linked"}, {"FragmentPath", "/fictional/foreign.service"}, {"DropInPaths", "/fictional/override.conf"}} {
		t.Run(entry.k, func(t *testing.T) {
			f, m := newSystemdFixture(t)
			f.installed(t)
			m.calls = nil
			m.properties = map[string]string{entry.k: entry.v}
			if c, o, e := f.call(t, "status", "--json"); c != 1 || !strings.Contains(o, `"registration": "unknown"`) || strings.Contains(o+e, entry.v) {
				t.Fatal("unknown status or leaked property", c, o, e)
			}
			if c, _, _ := f.call(t, "remove"); c != 1 {
				t.Fatal("foreign removed")
			}
			for _, args := range m.calls {
				if args[0] != "show" {
					t.Fatal("foreign mutated", args)
				}
			}
		})
	}
}
func TestSystemdMalformedProperties(t *testing.T) {
	f, m := newSystemdFixture(t)
	for _, s := range []string{"", "PRIVATE_RESPONSE", "Id=x\nId=x\n", strings.Repeat("x", 65537)} {
		m.raw = &s
		if got := inspectSystemd(context.Background(), f.host, m.p); got.registration != "unknown" {
			t.Fatalf("accepted malformed: %+v", got)
		}
	}
	m.raw = nil
	// Structured absence alone is sufficient only with a successful
	// exit; a failed bus connection or other nonzero code remains unknown.
	for _, code := range []int{0, 4, 1, 5} {
		f.host.query = func(ctx context.Context, args ...string) ([]byte, int, error) {
			b, _, _ := m.query(ctx, args...)
			return b, code, nil
		}
		want := "unknown"
		if code == 0 {
			want = "absent"
		}
		if got := inspectSystemd(context.Background(), f.host, m.p).registration; got != want {
			t.Fatal(code, got)
		}
	}
}
func TestSystemdGuardsAndChangedFiles(t *testing.T) {
	for _, kind := range []string{"owner", "daemon", "tick", "service", "unrecorded", "modified", "symlink", "relative-xdg", "checkout-xdg", "dropin-after-reload", "changed-before-start", "changed-removing", "dangling-enable-link"} {
		t.Run(kind, func(t *testing.T) {
			f, m := newSystemdFixture(t)
			op := "install"
			want := 1
			switch kind {
			case "owner":
				control.Save(f.cfg.Loop.StateDir, control.File{Ownership: &control.Ownership{ThreadID: "thr_owner", Generation: 1}})
				t.Setenv("BB_THREAD_ID", "thr_other")
			case "daemon", "tick", "service":
				l, err := lock.AcquireNamed(f.cfg.Loop.StateDir, kind)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Release()
				want = 75
			case "relative-xdg":
				f.host.configHome = "relative"
			case "checkout-xdg":
				f.host.configHome = filepath.Join(f.f.dir, "checkout")
				os.MkdirAll(filepath.Join(f.host.configHome, ".git"), 0700)
			case "dropin-after-reload":
				m.before = func(args []string) {
					if args[0] == "daemon-reload" {
						m.properties = map[string]string{"DropInPaths": "/fictional/override.conf"}
					}
				}
			case "changed-before-start":
				m.before = func(args []string) {
					if args[0] == "enable" {
						write(t, m.p.Definition, "changed", 0600)
					}
				}
			default:
				f.installed(t)
				op = "remove"
				switch kind {
				case "unrecorded":
					os.Remove(filepath.Join(m.p.StateDir, f.host.recordName()))
				case "modified":
					write(t, m.p.Definition, "foreign", 0600)
				case "symlink":
					os.Remove(m.p.Definition)
					os.Symlink(f.f.cfg, m.p.Definition)
				case "changed-removing":
					m.before = func(args []string) {
						if args[0] == "stop" {
							write(t, m.p.Definition, "changed", 0600)
						}
					}
				case "dangling-enable-link":
					m.before = func(args []string) {
						if args[0] == "stop" {
							os.Symlink("/fictional/other", filepath.Join(filepath.Dir(m.p.Definition), "default.target.wants", m.p.Label))
						}
					}
				}
			}
			if c, o, e := f.call(t, op); c != want {
				t.Fatalf("guard %d want %d %s %s", c, want, o, e)
			}
			if f.f.read("calls") != "" || f.f.read("probes") != "" {
				t.Fatal("provider touched")
			}
			if kind == "changed-before-start" && m.active {
				t.Fatal("changed definition started")
			}
		})
	}
}
func TestSystemdDefinitionNativeValidation(t *testing.T) {
	f, _ := newSystemdFixture(t)
	f.host.configHome = filepath.Join(f.f.dir, "custom config")
	f.host.executable = filepath.Join(f.f.dir, `keepgoing %h $HOME ${NAME} ü`)
	write(t, f.host.executable, "#!/bin/sh\nexit 0\n", 0700)
	p, err := newServicePlan(f.host, f.cfg.Loop.StateDir, filepath.Join(f.f.dir, `dir %h $HOME ${NAME} "quote" \ backslash ' ü`, `task %h $HOME ${NAME} "quote" \ backslash ' ü.yaml`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Definition, f.host.configHome+string(os.PathSeparator)) {
		t.Fatal("XDG ignored")
	}
	b := systemdDefinition(p)
	for _, s := range []string{`%%h $HOME ${NAME} \"quote\" \\ backslash ' ü.yaml`, `ExecStart=":`, `"BB_THREAD_ID="`, "KillMode=process", "Restart=on-failure"} {
		if !bytes.Contains(b, []byte(s)) {
			t.Fatalf("missing %q in %s", s, b)
		}
	}
	if runtime.GOOS != "linux" {
		t.Skip("native systemd unit validation requires Linux")
	}
	path, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Fatal("systemd-analyze required for native fixture")
	}
	file := filepath.Join(f.f.dir, p.Label)
	write(t, file, string(b), 0600)
	cmd := exec.Command(path, "verify", "--generators=no", file)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native unit validation: %v %s", err, out)
	}
}

func TestSystemdRejectsUnsupportedExecutablePaths(t *testing.T) {
	for _, char := range []string{"\"", "'", "\\"} {
		f, m := newSystemdFixture(t)
		f.host.executable = filepath.Join(f.f.dir, "keepgoing"+char)
		write(t, f.host.executable, "#!/bin/sh\nexit 0\n", 0700)
		if c, _, e := f.call(t, "install"); c != 1 || !strings.Contains(e, "executable path") || len(m.calls) != 0 {
			t.Fatalf("invalid executable: %d %s", c, e)
		}
	}
}
