//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
)

func TestSystemdBindingWithLocalResponder(t *testing.T) {
	f, m := newSystemdFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_binding_fixture")
	t.Setenv("INVOCATION_ID", fixtureInvocation)
	ctl := control.File{Enabled: true, Paused: true, Stop: true, Completed: true, AwaitingUser: true, Ownership: &control.Ownership{ThreadID: "thr_binding_fixture", Generation: 3}}
	if err := control.Save(f.cfg.Loop.StateDir, ctl); err != nil {
		t.Fatal(err)
	}
	f.installed(t)
	s, err := daemoninfo.Start(f.cfg.Loop.StateDir, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m.properties = map[string]string{"MainPID": strconv.Itoa(os.Getpid()), "InvocationID": fixtureInvocation}
	paths := []string{filepath.Join(f.cfg.Loop.StateDir, control.FileName), filepath.Join(f.cfg.Loop.StateDir, f.host.recordName()), m.p.Definition}
	before := make([][]byte, len(paths))
	for i, p := range paths {
		before[i], _ = os.ReadFile(p)
	}
	n := len(m.calls)
	for _, want := range []string{"verified", "different", "unavailable"} {
		if want == "different" {
			m.properties["InvocationID"] = otherInvocation
		}
		if want == "unavailable" {
			s.Close()
		}
		c, o, e := f.call(t, "status", "--json")
		var r serviceReport
		if c != 0 || json.Unmarshal([]byte(o), &r) != nil || r.Binding == nil || r.Binding.State != want {
			t.Fatalf("%d %s %s", c, o, e)
		}
		if strings.Contains(o+e, fixtureInvocation) || strings.Contains(o+e, otherInvocation) || strings.Contains(o, "processID") {
			t.Fatal("identity leaked")
		}
		if c, o, e := f.call(t, "status"); c != 0 || !strings.Contains(o, "service binding: "+want) {
			t.Fatalf("%d %s %s", c, o, e)
		}
	}
	for _, args := range m.calls[n:] {
		if args[0] != "show" {
			t.Fatal("manager changed", args)
		}
	}
	for i, p := range paths {
		after, _ := os.ReadFile(p)
		if !bytes.Equal(before[i], after) {
			t.Fatal("file changed")
		}
	}
	if f.f.read("calls") != "" || f.f.read("probes") != "" {
		t.Fatal("provider called")
	}
}
