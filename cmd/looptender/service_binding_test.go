package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const fixtureInvocation = "1234567890abcdef1234567890abcdef"
const otherInvocation = "abcdef1234567890abcdef1234567890"

func TestServiceBindingAssessment(t *testing.T) {
	p := systemdProcess{1234, fixtureInvocation}
	d := daemonStatus{State: "responsive", processID: 1234, invocationID: fixtureInvocation}
	for _, tt := range []struct {
		name          string
		before, after systemdProcess
		daemon        daemonStatus
		want          string
	}{
		{"matching", p, p, d, "verified"},
		{"unknown manager", systemdProcess{}, p, d, "unavailable"},
		{"lost manager", p, systemdProcess{}, d, "unavailable"},
		{"reused PID new invocation", p, systemdProcess{1234, otherInvocation}, d, "unavailable"},
		{"changed PID", p, systemdProcess{2345, fixtureInvocation}, d, "unavailable"},
		{"nonresponsive", p, p, daemonStatus{State: "unavailable"}, "unavailable"},
		{"older daemon", p, p, daemonStatus{State: "responsive"}, "unavailable"},
		{"manual daemon", p, p, daemonStatus{State: "responsive", processID: 1234}, "unavailable"},
		{"different PID", p, p, daemonStatus{State: "responsive", processID: 2345, invocationID: fixtureInvocation}, "different"},
		{"different invocation", p, p, daemonStatus{State: "responsive", processID: 1234, invocationID: otherInvocation}, "different"},
		{"invalid invocation", p, p, daemonStatus{State: "responsive", processID: 1234, invocationID: "PRIVATE"}, "unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := assessServiceBinding(tt.before, tt.after, tt.daemon)
			if b.State != tt.want {
				t.Fatal(b)
			}
			out, _ := json.Marshal(struct {
				Daemon  daemonStatus
				Binding serviceBinding
			}{tt.daemon, b})
			for _, secret := range []string{fixtureInvocation, otherInvocation, "PRIVATE", "1234", "2345"} {
				if strings.Contains(string(out), secret) {
					t.Fatal("identity leaked", string(out))
				}
			}
			r := serviceReport{Backend: "launchd", Recorded: true, DefinitionPresent: true, Registration: "loaded", Daemon: &daemonStatus{State: "responsive", BinaryComparison: "match"}, Binding: &b}
			h := assessServiceHealth(r)
			want := "ok"
			if tt.want == "different" {
				want = "warning"
			}
			if tt.want == "unavailable" {
				want = "unavailable"
			}
			if h.Status != want {
				t.Fatalf("%+v", h)
			}
		})
	}
}
