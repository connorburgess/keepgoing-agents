package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDescribeSystemdExecution(t *testing.T) {
	for _, tt := range []struct {
		name, result, code, status, exit string
		available                        bool
	}{
		{"no exit", "success", "0", "0", "not_reported", true},
		{"clean exit", "success", "1", "0", "exited", true},
		{"nonzero", "exit-code", "1", "1", "exited", true},
		{"setup code", "exit-code", "1", "203", "exited", true},
		{"max exit", "exit-code", "1", "255", "exited", true},
		{"signal", "signal", "2", "15", "killed", true},
		{"core", "core-dump", "3", "11", "dumped", true},
		{"no main exit on failure", "start-limit-hit", "0", "0", "not_reported", true},
		{"future result", "PRIVATE_RESULT", "1", "1", "", false},
		{"future code", "signal", "9", "9", "", false},
		{"negative exit", "exit-code", "1", "-1", "", false},
		{"large exit", "exit-code", "1", "256", "", false},
		{"zero signal", "signal", "2", "0", "", false},
		{"unknown signal", "signal", "2", "129", "", false},
		{"contradictory no exit", "success", "0", "1", "", false},
		{"noncanonical", "exit-code", "1", "+1", "", false},
		{"text status", "exit-code", "1", "PRIVATE_STATUS", "", false},
		{"overflow", "exit-code", "1", "99999999999999999999999", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			x := describeSystemdExecution(tt.result, tt.code, tt.status)
			if (x.State == "available") != tt.available || x.MainExit != tt.exit {
				t.Fatalf("unexpected result: %+v", x)
			}
			if tt.available && ((x.MainStatus == nil) != (tt.code == "0")) {
				t.Fatal("exit status presence", x)
			}
			b, _ := json.Marshal(x)
			if strings.Contains(string(b), "PRIVATE") {
				t.Fatal("raw text leaked")
			}
		})
	}
	for _, result := range []string{"resources", "protocol", "timeout", "watchdog", "oom-kill", "exec-condition"} {
		if x := describeSystemdExecution(result, "0", "0"); x.State != "available" {
			t.Fatalf("result %s unavailable", result)
		}
	}
}

func TestServiceExecutionHealth(t *testing.T) {
	for _, tt := range []struct{ result, want string }{
		{"success", ""}, {"exit-code", "unit_result"}, {"exec-condition", "unit_result"}, {"PRIVATE", "unit_result_unavailable"},
	} {
		x := describeSystemdExecution(tt.result, "0", "0")
		// An active unit and responsive daemon must not hide an earlier result.
		r := serviceReport{Backend: "systemd", Recorded: true, DefinitionPresent: true, Registration: "loaded", ActiveState: "active", UnitFileState: "enabled", Execution: &x, Daemon: &daemonStatus{State: "responsive", BinaryComparison: "match"}}
		h := assessServiceHealth(r)
		if tt.want == "" {
			if h.Status != "ok" || len(h.Findings) != 0 {
				t.Fatalf("%+v", h)
			}
		} else if len(h.Findings) != 1 || h.Findings[0].Code != tt.want {
			t.Fatalf("%s: %+v", tt.result, h)
		}
	}
}
