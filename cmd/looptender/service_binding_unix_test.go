//go:build unix

package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSystemdBindingBracketsChallenge(t *testing.T) {
	f, m := newSystemdFixture(t)
	f.installed(t)
	m.properties = map[string]string{"MainPID": "1234", "InvocationID": fixtureInvocation}
	for _, change := range []bool{false, true} {
		m.properties["InvocationID"] = fixtureInvocation
		var order []string
		m.before = func(args []string) {
			if !reflect.DeepEqual(args, []string{"show", "--all", "--property=" + systemdBindingProperties, m.p.Label}) {
				t.Fatal(args)
			}
			order = append(order, "manager")
		}
		d, b := inspectSystemdBinding(context.Background(), f.host, m.p, func() daemonStatus {
			order = append(order, "challenge")
			if change {
				m.properties["InvocationID"] = otherInvocation
			}
			return daemonStatus{State: "responsive", processID: 1234, invocationID: fixtureInvocation}
		})
		want := "verified"
		if change {
			want = "unavailable"
		}
		if b.State != want || d.State != "responsive" || strings.Join(order, ",") != "manager,challenge,manager" {
			t.Fatalf("%+v %v", b, order)
		}
	}
}

func TestSystemdBindingRejectsUnverifiedProperties(t *testing.T) {
	f, m := newSystemdFixture(t)
	f.installed(t)
	m.properties = map[string]string{"MainPID": "1234", "InvocationID": fixtureInvocation}
	base, code, err := m.query(context.Background(), "show", "--all", "--property="+systemdBindingProperties, m.p.Label)
	if code != 0 || err != nil {
		t.Fatal(code, err)
	}
	for _, tt := range []struct{ name, old, new string }{
		{"PID zero", "MainPID=1234", "MainPID=0"},
		{"PID overflow", "MainPID=1234", "MainPID=2147483648"},
		{"PID sign", "MainPID=1234", "MainPID=+1234"},
		{"PID text", "MainPID=1234", "MainPID=PRIVATE"},
		{"empty invocation", fixtureInvocation, ""},
		{"zero invocation", fixtureInvocation, strings.Repeat("0", 32)},
		{"uppercase", fixtureInvocation, strings.ToUpper(fixtureInvocation)},
		{"foreign id", m.p.Label, "foreign.service"},
		{"foreign fragment", m.p.Definition, "/private/foreign.service"},
		{"drop-ins", "DropInPaths=", "DropInPaths=/private/override.conf"},
		{"not loaded", "LoadState=loaded", "LoadState=not-found"},
		{"inactive", "ActiveState=active", "ActiveState=inactive"},
		{"transition", "ActiveState=active", "ActiveState=activating"},
		{"duplicate", "MainPID=1234", "MainPID=1234\nMainPID=1234"},
		{"extra", "MainPID=1234", "MainPID=1234\nPrivate=SECRET"},
		{"missing", "MainPID=1234\n", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := f.host
			h.query = func(context.Context, ...string) ([]byte, int, error) {
				return []byte(strings.Replace(string(base), tt.old, tt.new, 1)), 0, nil
			}
			if p := inspectSystemdProcess(context.Background(), h, m.p); p.pid != 0 {
				t.Fatal("unverified properties accepted")
			}
		})
	}
	for _, tt := range []struct {
		out  []byte
		code int
		err  error
	}{
		{base, 1, nil}, {base, 0, errors.New("SECRET")}, {[]byte(strings.Repeat("X", 65537)), 0, nil}, {[]byte("SECRET"), 0, nil},
	} {
		h := f.host
		h.query = func(context.Context, ...string) ([]byte, int, error) { return tt.out, tt.code, tt.err }
		queried := false
		_, b := inspectSystemdBinding(context.Background(), h, m.p, func() daemonStatus {
			queried = true
			return daemonStatus{State: "responsive", processID: 1234, invocationID: fixtureInvocation}
		})
		if !queried || b.State != "unavailable" || strings.Contains(b.Message, "SECRET") {
			t.Fatal(b)
		}
	}
}
