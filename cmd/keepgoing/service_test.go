//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

type fakeLaunchd struct {
	loaded, disabled bool
	calls            [][]string
	before           func([]string)
	fail             string
	exit             int
	unknown          bool
}

func (f *fakeLaunchd) run(ctx context.Context, args ...string) (int, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.before != nil {
		f.before(args)
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if args[0] == f.fail {
		return f.exit, nil
	}
	switch args[0] {
	case "print":
		if f.unknown {
			return -1, errors.New("private scheduler output must not escape")
		}
		if strings.Count(args[1], "/") == 1 || f.loaded {
			return 0, nil
		}
		return 113, nil
	case "enable":
		f.disabled = false
	case "disable":
		f.disabled = true
	case "bootstrap":
		f.loaded = true
	case "bootout":
		f.loaded = false
	default:
		return 1, nil
	}
	return 0, nil
}

type serviceFixture struct {
	f       *fixture
	cfg     *config.Config
	host    serviceHost
	manager *fakeLaunchd
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Loop.StateDir, err = canonicalFuturePath(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	manager := &fakeLaunchd{}
	host := serviceHost{platform: "darwin", home: filepath.Join(f.dir, "fictional-home"), executable: filepath.Join(f.dir, "bin", "keepgoing"), searchPath: "/usr/bin:/bin", uid: 501, run: manager.run}
	write(t, host.executable, "#!/bin/sh\nexit 0\n", 0o700)
	return &serviceFixture{f, cfg, host, manager}
}
func (f *serviceFixture) call(t *testing.T, op string, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	args = append([]string{op, "-c", f.f.cfg}, args...)
	code := serviceCommand(env{context.Background(), &out, &errs}, args, f.host)
	return code, out.String(), errs.String()
}
func (f *serviceFixture) plan(t *testing.T) servicePlan {
	t.Helper()
	p, exists, err := readServicePlan(f.host, f.cfg.Loop.StateDir)
	if err != nil || !exists {
		t.Fatalf("plan: %v %v", exists, err)
	}
	return p
}
func (f *serviceFixture) installed(t *testing.T) {
	t.Helper()
	if c, o, e := f.call(t, "install"); c != 0 {
		t.Fatalf("install: %d %s %s", c, o, e)
	}
}

func TestServiceInstallStatusRemovePreservesControls(t *testing.T) {
	for _, hold := range []control.File{{}, {Enabled: true, Paused: true}, {Enabled: true, Stop: true}, {Enabled: true, Completed: true}, {Enabled: true, AwaitingUser: true}} {
		t.Run(hold.Summary(), func(t *testing.T) {
			f := newServiceFixture(t)
			hold.Note = "fictional review"
			hold.Ownership = &control.Ownership{ThreadID: "thr_service_owner", Generation: 7}
			t.Setenv("BB_THREAD_ID", "thr_service_owner")
			if err := control.Save(f.cfg.Loop.StateDir, hold); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(f.cfg.Loop.StateDir, control.FileName))
			f.installed(t)
			p := f.plan(t)
			if !f.manager.loaded || f.manager.disabled {
				t.Fatal("not registered")
			}
			for _, path := range []string{p.Definition, filepath.Join(p.StateDir, serviceRecordName)} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("permissions: %v %v", info, err)
				}
			}
			calls := len(f.manager.calls)
			if c, _, _ := f.call(t, "install"); c != 1 || len(f.manager.calls) != calls {
				t.Fatal("reinstall should refuse without manager changes")
			}
			if c, o, e := f.call(t, "status", "--json"); c != 0 {
				t.Fatalf("status %d %s %s", c, o, e)
			} else {
				var r serviceReport
				if err := json.Unmarshal([]byte(o), &r); err != nil {
					t.Fatal(err)
				}
				if !r.Recorded || !r.DefinitionPresent || r.Registration != "loaded" || r.Daemon == nil || r.Daemon.State == "responsive" {
					t.Fatalf("status: %+v", r)
				}
			}
			// Restrictive removal is available from another BB conversation too.
			t.Setenv("BB_THREAD_ID", "thr_other")
			if c, o, e := f.call(t, "remove"); c != 0 {
				t.Fatalf("remove %d %s %s", c, o, e)
			}
			if f.manager.loaded || !f.manager.disabled {
				t.Fatal("scheduled service remains enabled")
			}
			for _, path := range []string{p.Definition, filepath.Join(p.StateDir, serviceRecordName)} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("artifact remains: %s", path)
				}
			}
			after, _ := os.ReadFile(filepath.Join(f.cfg.Loop.StateDir, control.FileName))
			if !bytes.Equal(before, after) || f.f.read("calls") != "" || f.f.read("probes") != "" {
				t.Fatal("controls or providers touched")
			}
			if c, o, e := f.call(t, "status", "--json"); c != 0 || !strings.Contains(o, `"registration": "absent"`) {
				t.Fatalf("removed status %d %s %s", c, o, e)
			}
		})
	}
}

func TestServicePreviewDoesNotWriteOrQuery(t *testing.T) {
	f := newServiceFixture(t)
	if c, o, e := f.call(t, "install", "--dry-run", "--json"); c != 0 || !strings.Contains(o, `"dryRun": true`) {
		t.Fatalf("preview %d %s %s", c, o, e)
	}
	if len(f.manager.calls) != 0 {
		t.Fatal("preview contacted scheduler")
	}
	for _, dir := range []string{f.cfg.Loop.StateDir, f.host.home} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("preview wrote files")
		}
	}
	f.installed(t)
	p := f.plan(t)
	f.manager.calls = nil
	if c, o, e := f.call(t, "remove", "--dry-run"); c != 0 {
		t.Fatalf("remove preview %d %s %s", c, o, e)
	}
	if len(f.manager.calls) != 0 {
		t.Fatal("remove preview contacted scheduler")
	}
	if _, err := os.Stat(p.Definition); err != nil {
		t.Fatal(err)
	}
}

func TestServiceFailedInstallRemainsRemovableWithoutConfig(t *testing.T) {
	for _, step := range []string{"enable", "bootstrap"} {
		t.Run(step, func(t *testing.T) {
			f := newServiceFixture(t)
			f.manager.fail = step
			f.manager.exit = 5
			if c, _, e := f.call(t, "install"); c != 1 || !strings.Contains(e, "incomplete") {
				t.Fatalf("install %d %s", c, e)
			}
			p := f.plan(t)
			if _, err := os.Stat(p.Definition); err != nil {
				t.Fatal("recovery definition missing")
			}
			write(t, f.f.cfg, "invalid yaml: [", 0o600)
			f.manager.fail = ""
			var out, errs bytes.Buffer
			c := serviceCommand(env{context.Background(), &out, &errs}, []string{"remove", "--state-dir", p.StateDir}, f.host)
			if c != 0 || f.manager.loaded || !f.manager.disabled {
				t.Fatalf("recovery %d %s %s", c, &out, &errs)
			}
			if _, err := os.Stat(p.Definition); !os.IsNotExist(err) {
				t.Fatal("definition remains")
			}
		})
	}
}

func TestServiceRemovalFailuresRetainRecoveryFiles(t *testing.T) {
	for _, kind := range []string{"disable", "bootout", "unknown", "still-loaded", "changed-during-removal"} {
		t.Run(kind, func(t *testing.T) {
			f := newServiceFixture(t)
			f.installed(t)
			p := f.plan(t)
			switch kind {
			case "disable", "bootout":
				f.manager.fail = kind
				f.manager.exit = 5
			case "unknown":
				f.manager.unknown = true
			case "still-loaded":
				f.manager.fail = "bootout"
				f.manager.exit = 0
			case "changed-during-removal":
				f.manager.before = func(args []string) {
					if args[0] == "bootout" {
						write(t, p.Definition, "modified", 0o600)
					}
				}
			}
			if c, _, _ := f.call(t, "remove"); c != 1 {
				t.Fatal("failed removal claimed success")
			}
			for _, path := range []string{p.Definition, filepath.Join(p.StateDir, serviceRecordName)} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("recovery file removed: %v", err)
				}
			}
		})
	}
}

func TestServiceRefusesUnmanagedAndChangedArtifacts(t *testing.T) {
	for _, kind := range []string{"unrecorded", "modified", "symlink", "record-path", "record-extra", "oversized", "missing-definition"} {
		t.Run(kind, func(t *testing.T) {
			f := newServiceFixture(t)
			f.installed(t)
			p := f.plan(t)
			switch kind {
			case "unrecorded":
				os.Remove(filepath.Join(p.StateDir, serviceRecordName))
			case "modified":
				write(t, p.Definition, "foreign", 0o600)
			case "symlink":
				os.Remove(p.Definition)
				if err := os.Symlink(f.f.cfg, p.Definition); err != nil {
					t.Fatal(err)
				}
			case "record-path":
				p.Definition = filepath.Join(f.f.dir, "foreign.plist")
				b, _ := json.Marshal(p)
				write(t, filepath.Join(p.StateDir, serviceRecordName), string(b), 0o600)
			case "record-extra":
				b, _ := os.ReadFile(filepath.Join(p.StateDir, serviceRecordName))
				write(t, filepath.Join(p.StateDir, serviceRecordName), string(b)+" {}", 0o600)
			case "oversized":
				write(t, p.Definition, strings.Repeat("x", 65537), 0o600)
			case "missing-definition":
				os.Remove(p.Definition)
			}
			f.manager.calls = nil
			c, _, _ := f.call(t, "remove")
			if kind == "missing-definition" {
				if c != 0 {
					t.Fatal("partial artifact cleanup should succeed")
				}
				return
			}
			if c != 1 || len(f.manager.calls) != 0 {
				t.Fatalf("unsafe artifact managed: %d %v", c, f.manager.calls)
			}
		})
	}
}

func TestServiceInstallGuards(t *testing.T) {
	for _, kind := range []string{"owner", "daemon", "tick", "service", "registered", "unknown", "domain-unavailable", "checkout", "symlink-checkout", "invalid-cli", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			f := newServiceFixture(t)
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
			case "registered":
				f.manager.loaded = true
			case "unknown":
				f.manager.unknown = true
			case "domain-unavailable":
				f.manager.fail = "print"
				f.manager.exit = 113
			case "checkout":
				os.MkdirAll(filepath.Join(f.host.home, ".git"), 0o700)
			case "symlink-checkout":
				repo := filepath.Join(f.f.dir, "repo")
				os.MkdirAll(filepath.Join(repo, ".git"), 0o700)
				os.Symlink(repo, f.host.home)
			case "invalid-cli":
				os.Remove(f.cfg.Loop.BB.CLI)
			case "unsupported":
				f.host.platform = "windows"
			}
			c, _, _ := f.call(t, "install")
			if c != want {
				t.Fatalf("code %d want %d", c, want)
			}
			if _, err := os.Stat(filepath.Join(f.cfg.Loop.StateDir, serviceRecordName)); !os.IsNotExist(err) {
				t.Fatal("guard wrote registration")
			}
			for _, call := range f.manager.calls {
				if call[0] != "print" {
					t.Fatalf("guard changed scheduler: %v", call)
				}
			}
		})
	}
}

func TestLaunchdDefinitionEscapingAndPlistValidation(t *testing.T) {
	f := newServiceFixture(t)
	p, err := newServicePlan(f.host, f.cfg.Loop.StateDir, filepath.Join(f.f.dir, `a <&"' task.yaml`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := launchdDefinition(p)
	if err != nil {
		t.Fatal(err)
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	var stringsFound []string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "string" {
			var s string
			if err := d.DecodeElement(&s, &el); err != nil {
				t.Fatal(err)
			}
			stringsFound = append(stringsFound, s)
		}
	}
	if !reflect.DeepEqual(stringsFound[:7], []string{p.Label, p.Executable, "__service-daemon", "-c", p.Config, "--state-dir", p.StateDir}) {
		t.Fatalf("argv escaping: %v", stringsFound)
	}
	if bytes.Contains(b, []byte("thr_service")) || !bytes.Contains(b, []byte("<key>BB_THREAD_ID</key><string></string>")) {
		t.Fatal("owner identity inherited")
	}
	if runtime.GOOS == "darwin" {
		file := filepath.Join(f.f.dir, "definition.plist")
		write(t, file, string(b), 0o600)
		if out, err := exec.Command("/usr/bin/plutil", "-lint", file).CombinedOutput(); err != nil {
			t.Fatalf("native plist validation: %v %s", err, out)
		}
	}
	p.SearchPath = "/bin\nPRIVATE_VALUE"
	if err := validateServicePlan(f.host, p.StateDir, p); err == nil {
		t.Fatal("newline accepted")
	}
}

func TestServiceDaemonRefusesStateDirectoryChange(t *testing.T) {
	f := newServiceFixture(t)
	var out, errs bytes.Buffer
	c := serviceDaemon(env{context.Background(), &out, &errs}, []string{"-c", f.f.cfg, "--state-dir", filepath.Join(f.f.dir, "other-state")})
	if c != 1 || f.f.read("calls") != "" || f.f.read("probes") != "" {
		t.Fatalf("state binding: %d %s %s", c, &out, &errs)
	}
	if _, err := os.Stat(f.cfg.Loop.StateDir); !os.IsNotExist(err) {
		t.Fatal("wrong state directory mutated")
	}
}

func TestServiceInstallSerializesAndCanReinstall(t *testing.T) {
	f := newServiceFixture(t)
	f.manager.before = func(args []string) {
		if args[0] == "print" {
			f.manager.before = nil
			if c, _, _ := f.call(t, "install"); c != exitSkipped {
				t.Fatalf("concurrent install: %d", c)
			}
		}
	}
	f.installed(t)
	old := f.plan(t)
	if c, o, e := f.call(t, "remove"); c != 0 {
		t.Fatalf("remove %d %s %s", c, o, e)
	}
	f.installed(t)
	if p := f.plan(t); p.Label != old.Label || f.manager.disabled || !f.manager.loaded {
		t.Fatalf("reinstall: %+v", p)
	}
}

func TestServiceStatusAndFlags(t *testing.T) {
	f := newServiceFixture(t)
	f.manager.unknown = true
	if c, o, e := f.call(t, "status", "--json"); c != 1 || !strings.Contains(o, `"registration": "unknown"`) || strings.Contains(o+e, "private scheduler") {
		t.Fatalf("status: %d %s %s", c, o, e)
	}
	if _, err := os.Stat(f.cfg.Loop.StateDir); !os.IsNotExist(err) {
		t.Fatal("status wrote runtime")
	}
	for _, args := range [][]string{{}, {"other"}, {"install", "--state-dir", f.cfg.Loop.StateDir}, {"status", "--dry-run"}, {"remove", "--state-dir", f.cfg.Loop.StateDir, "-c", f.f.cfg}, {"install", "--take-over"}} {
		var out, errs bytes.Buffer
		if c := serviceCommand(env{context.Background(), &out, &errs}, args, f.host); c != exitUsage {
			t.Fatalf("flags %v: %d %s", args, c, &errs)
		}
	}
}

func TestServiceRemovalWaitsForDaemonExit(t *testing.T) {
	f := newServiceFixture(t)
	f.installed(t)
	p := f.plan(t)
	l, err := lock.AcquireNamed(p.StateDir, lock.Daemon)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var out, errs bytes.Buffer
	c := serviceCommand(env{ctx, &out, &errs}, []string{"remove", "-c", f.f.cfg}, f.host)
	if c != exitFailed || !strings.Contains(errs.String(), "daemon exit not verified") || !f.manager.disabled || f.manager.loaded {
		t.Fatalf("exit check %d %s %s", c, &out, &errs)
	}
	if _, err := os.Stat(p.Definition); err != nil {
		t.Fatal("recovery definition removed")
	}
	l.Release()
	if c, o, e := f.call(t, "remove"); c != 0 {
		t.Fatalf("retry %d %s %s", c, o, e)
	}
}
