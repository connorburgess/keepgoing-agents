//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
)

func TestServiceHealthStatusIsReadOnly(t *testing.T) {
	for _, backend := range []string{"launchd", "systemd"} {
		t.Run(backend, func(t *testing.T) {
			var f *serviceFixture
			var managerCalls func() [][]string
			if backend == "launchd" {
				f = newServiceFixture(t)
				managerCalls = func() [][]string { return f.manager.calls }
			} else {
				var m *fakeSystemd
				f, m = newSystemdFixture(t)
				managerCalls = func() [][]string { return m.calls }
			}
			ctl := control.File{Enabled: true, Paused: true, Stop: true, AwaitingUser: true, Completed: true, Note: doctorPrivate, Ownership: &control.Ownership{ThreadID: "thr_health_fixture", Generation: 4}}
			t.Setenv("BB_THREAD_ID", "thr_health_fixture")
			if err := control.Save(f.cfg.Loop.StateDir, ctl); err != nil {
				t.Fatal(err)
			}
			f.installed(t)
			// A local diagnostic responder is deliberately independent of the fake
			// manager, proving that service and daemon evidence remain separate.
			server, err := daemoninfo.Start(f.cfg.Loop.StateDir, "fixture-build", true)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			due := time.Now().Add(-2 * time.Minute)
			server.Update("waiting", &due)
			before, _ := os.ReadFile(filepath.Join(f.cfg.Loop.StateDir, control.FileName))
			oldVersion := version
			version = "another-build"
			defer func() { version = oldVersion }()
			n := len(managerCalls())
			for _, jsonMode := range []bool{true, false} {
				args := []string{}
				if jsonMode {
					args = append(args, "--json")
				}
				c, o, e := f.call(t, "status", args...)
				if c != 0 || strings.Contains(o+e, doctorPrivate) {
					t.Fatalf("status %d %s %s", c, o, e)
				}
				if jsonMode {
					var r serviceReport
					if err := json.Unmarshal([]byte(o), &r); err != nil {
						t.Fatal(err)
					}
					if r.Health == nil || r.Health.Status != "warning" {
						t.Fatalf("no assessment: %+v", r)
					}
					var codes []string
					for _, f := range r.Health.Findings {
						codes = append(codes, f.Code)
					}
					want := "daemon_build_differs,daemon_dry_run,daemon_check_overdue"
					if backend == "systemd" {
						want = "service_binding_unavailable," + want
					}
					if strings.Join(codes, ",") != want {
						t.Fatalf("findings %v", codes)
					}
				} else if !strings.Contains(o, "service health: warning") || !strings.Contains(o, "daemon_build_differs") || !strings.Contains(o, "Next:") {
					t.Fatal("missing human guidance", o)
				}
			}
			for _, args := range managerCalls()[n:] {
				if args[0] != "show" && args[0] != "print" {
					t.Fatal("status mutated manager", args)
				}
			}
			after, _ := os.ReadFile(filepath.Join(f.cfg.Loop.StateDir, control.FileName))
			if !bytes.Equal(before, after) || f.f.read("calls") != "" || f.f.read("probes") != "" {
				t.Fatal("status changed controls or called provider")
			}
		})
	}
}

func TestServiceHealthAbsentAndUnknownDoNotCreateFiles(t *testing.T) {
	f := newServiceFixture(t)
	for _, unknown := range []bool{false, true} {
		f.manager.unknown = unknown
		c, o, e := f.call(t, "status", "--json")
		wantCode, wantHealth := 0, "not_installed"
		if unknown {
			wantCode, wantHealth = 1, "unavailable"
		}
		var r serviceReport
		if c != wantCode || json.Unmarshal([]byte(o), &r) != nil || r.Health == nil || r.Health.Status != wantHealth {
			t.Fatalf("status %d %s %s", c, o, e)
		}
		for _, path := range []string{f.cfg.Loop.StateDir, f.host.home} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("status created files", err)
			}
		}
	}
}

func TestServiceHealthReportsFailedSystemdUnit(t *testing.T) {
	f, m := newSystemdFixture(t)
	f.installed(t)
	m.properties = map[string]string{"ActiveState": "failed", "UnitFileState": "disabled"}
	n := len(m.calls)
	c, o, e := f.call(t, "status", "--json")
	var r serviceReport
	if c != 0 || json.Unmarshal([]byte(o), &r) != nil || r.Health == nil || r.Health.Status != "warning" {
		t.Fatalf("status %d %s %s", c, o, e)
	}
	var codes []string
	for _, f := range r.Health.Findings {
		codes = append(codes, f.Code)
	}
	if strings.Join(codes, ",") != "unit_failed,unit_disabled,service_binding_unavailable,daemon_unverified" {
		t.Fatalf("findings %v", codes)
	}
	for _, args := range m.calls[n:] {
		if args[0] != "show" {
			t.Fatal("manager changed", args)
		}
	}
}
