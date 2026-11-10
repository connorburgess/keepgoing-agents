//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestSystemdExecutionStatusReadOnly(t *testing.T) {
	f, m := newSystemdFixture(t)
	ctl := control.File{Enabled: true, Paused: true, Stop: true, Completed: true, AwaitingUser: true, Ownership: &control.Ownership{ThreadID: "thr_execution_fixture", Generation: 2}}
	t.Setenv("BB_THREAD_ID", "thr_execution_fixture")
	if err := control.Save(f.cfg.Loop.StateDir, ctl); err != nil {
		t.Fatal(err)
	}
	f.installed(t)
	paths := []string{filepath.Join(f.cfg.Loop.StateDir, control.FileName), filepath.Join(f.cfg.Loop.StateDir, f.host.recordName()), m.p.Definition}
	before := make([][]byte, len(paths))
	for i, path := range paths {
		before[i], _ = os.ReadFile(path)
	}
	m.properties = map[string]string{"ActiveState": "failed", "Result": "exit-code", "ExecMainCode": "1", "ExecMainStatus": "203"}
	n := len(m.calls)
	c, o, e := f.call(t, "status", "--json")
	var r serviceReport
	if c != 0 || json.Unmarshal([]byte(o), &r) != nil || r.Execution == nil || r.Execution.Result != "exit-code" || r.Execution.MainStatus == nil || *r.Execution.MainStatus != 203 {
		t.Fatalf("%d %s %s", c, o, e)
	}
	if c, o, e := f.call(t, "status"); c != 0 || !strings.Contains(o, "manager result: exit-code; main exit: exited; status: 203") {
		t.Fatalf("human status %d %s %s", c, o, e)
	}
	// An unreadable diagnostic must not change the verified registration or
	// prevent explicit removal: the mutation path uses its own identity query.
	m.properties["Result"] = "PRIVATE_RESULT"
	c, o, e = f.call(t, "status", "--json")
	if c != 0 || json.Unmarshal([]byte(o), &r) != nil || r.Registration != "loaded" || r.Execution.State != "unavailable" || strings.Contains(o+e, "PRIVATE") {
		t.Fatalf("unknown diagnostic %d %s %s", c, o, e)
	}
	for _, call := range m.calls[n:] {
		if call[0] != "show" {
			t.Fatal("manager mutation", call)
		}
	}
	for i, path := range paths {
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before[i], after) {
			t.Fatal("file changed", path)
		}
	}
	if f.f.read("calls") != "" || f.f.read("probes") != "" {
		t.Fatal("provider called")
	}
	m.properties = map[string]string{"Result": "PRIVATE_RESULT"}
	if c, o, e := f.call(t, "remove"); c != 0 {
		t.Fatalf("removal %d %s %s", c, o, e)
	}
}

func TestSystemdExecutionRejectsUnverifiedReply(t *testing.T) {
	f, m := newSystemdFixture(t)
	f.installed(t)
	base, code, err := m.query(context.Background(), "show", "--all", "--property="+systemdExecutionProperties, m.p.Label)
	if code != 0 || err != nil {
		t.Fatal(code, err)
	}
	for _, tt := range []struct {
		name string
		edit func(string) string
		code int
		err  error
	}{
		{"valid", func(s string) string { return s }, 0, nil},
		{"duplicate", func(s string) string { return s + "Result=success\n" }, 0, nil},
		{"extra", func(s string) string { return s + "Private=SECRET\n" }, 0, nil},
		{"missing", func(s string) string { return strings.Replace(s, "Result=success\n", "", 1) }, 0, nil},
		{"foreign id", func(s string) string { return strings.Replace(s, "Id="+m.p.Label, "Id=foreign.service", 1) }, 0, nil},
		{"foreign file", func(s string) string {
			return strings.Replace(s, "FragmentPath="+m.p.Definition, "FragmentPath=/private/foreign.service", 1)
		}, 0, nil},
		{"drop in", func(s string) string {
			return strings.Replace(s, "DropInPaths=", "DropInPaths=/private/override.conf", 1)
		}, 0, nil},
		{"state changed", func(s string) string { return strings.Replace(s, "ActiveState=active", "ActiveState=failed", 1) }, 0, nil},
		{"unloaded", func(s string) string { return strings.Replace(s, "LoadState=loaded", "LoadState=not-found", 1) }, 0, nil},
		{"malformed", func(s string) string { return "SECRET\n" }, 0, nil},
		{"oversized", func(s string) string { return s + strings.Repeat("X", 65536) }, 0, nil},
		{"exit failure", func(s string) string { return s }, 1, nil},
		{"query error", func(s string) string { return s }, 0, errors.New("SECRET")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := f.host
			h.query = func(context.Context, ...string) ([]byte, int, error) {
				return []byte(tt.edit(string(base))), tt.code, tt.err
			}
			x := inspectSystemdExecution(context.Background(), h, m.p, "active")
			want := "unavailable"
			if tt.name == "valid" {
				want = "available"
			}
			if x.State != want {
				t.Fatalf("%+v", x)
			}
			b, _ := json.Marshal(x)
			if strings.Contains(string(b), "SECRET") {
				t.Fatal("raw output leaked")
			}
		})
	}
}

func TestSystemdExecutionSkippedWithoutManagedRegistration(t *testing.T) {
	f, m := newSystemdFixture(t)
	c, o, e := f.call(t, "status", "--json")
	var r serviceReport
	if c != 0 || json.Unmarshal([]byte(o), &r) != nil || r.Execution != nil || len(m.calls) != 1 {
		t.Fatalf("%d %s %s calls=%v", c, o, e, m.calls)
	}
}
