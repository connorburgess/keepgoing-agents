package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestServiceHealthAssessments(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	due := now.Add(time.Minute)
	base := func() serviceReport {
		return serviceReport{Backend: "systemd", Recorded: true, DefinitionPresent: true, Registration: "loaded", ActiveState: "active", UnitFileState: "enabled", Daemon: &daemonStatus{State: "responsive", Phase: "waiting", ObservedAt: &now, NextCheckAt: &due, BinaryComparison: "match"}}
	}
	tests := []struct {
		name   string
		change func(*serviceReport)
		status string
		codes  []string
	}{
		{"observations clear", func(r *serviceReport) {}, "ok", nil},
		{"launchd unsupported observations", func(r *serviceReport) {
			r.Backend = "launchd"
			r.ActiveState, r.UnitFileState = "", ""
			r.Binding = &serviceBinding{State: "unavailable", Message: "Unsupported macOS service binding."}
			r.Execution = &serviceExecution{State: "unavailable"}
		}, "unavailable", []string{"service_binding_unavailable", "launchd_result_unavailable"}},
		{"not installed", func(r *serviceReport) {
			r.Recorded = false
			r.DefinitionPresent = false
			r.Registration = "absent"
			r.Daemon = nil
		}, "not_installed", nil},
		{"manual daemon without service", func(r *serviceReport) {
			r.Recorded = false
			r.DefinitionPresent = false
			r.Registration = "absent"
			r.Daemon.BinaryComparison = "different"
		}, "not_installed", nil},
		{"unknown unrecorded", func(r *serviceReport) { r.Recorded = false; r.DefinitionPresent = false; r.Registration = "unknown" }, "unavailable", []string{"registration_unknown"}},
		{"unmanaged registration", func(r *serviceReport) { r.Recorded = false; r.DefinitionPresent = false }, "warning", []string{"record_missing"}},
		{"missing definition", func(r *serviceReport) { r.DefinitionPresent = false }, "warning", []string{"definition_missing"}},
		{"missing registration despite responder", func(r *serviceReport) { r.Registration = "absent" }, "warning", []string{"registration_missing"}},
		{"unknown registration", func(r *serviceReport) { r.Registration = "unknown" }, "unavailable", []string{"registration_unknown"}},
		{"failed despite responder", func(r *serviceReport) { r.ActiveState = "failed" }, "warning", []string{"unit_failed"}},
		{"inactive despite responder", func(r *serviceReport) { r.ActiveState = "inactive" }, "warning", []string{"unit_inactive"}},
		{"activating", func(r *serviceReport) { r.ActiveState = "activating" }, "warning", []string{"unit_transitioning"}},
		{"deactivating", func(r *serviceReport) { r.ActiveState = "deactivating" }, "warning", []string{"unit_transitioning"}},
		{"reloading", func(r *serviceReport) { r.ActiveState = "reloading" }, "warning", []string{"unit_transitioning"}},
		{"unknown active state", func(r *serviceReport) { r.ActiveState = "unrecognized" }, "unavailable", []string{"unit_state_unknown"}},
		{"disabled while active", func(r *serviceReport) { r.UnitFileState = "disabled" }, "warning", []string{"unit_disabled"}},
		{"unknown enablement", func(r *serviceReport) { r.UnitFileState = "" }, "unavailable", []string{"unit_enablement_unknown"}},
		{"no daemon snapshot", func(r *serviceReport) { r.Daemon = nil }, "warning", []string{"daemon_unverified"}},
		{"no endpoint", func(r *serviceReport) { r.Daemon.State = "not_recorded" }, "warning", []string{"daemon_unverified"}},
		{"unavailable endpoint", func(r *serviceReport) { r.Daemon.State = "unavailable" }, "warning", []string{"daemon_unverified"}},
		{"different build", func(r *serviceReport) { r.Daemon.BinaryComparison = "different" }, "warning", []string{"daemon_build_differs"}},
		{"unknown build", func(r *serviceReport) { r.Daemon.BinaryComparison = "unknown" }, "unavailable", []string{"daemon_build_unknown"}},
		{"dry run", func(r *serviceReport) { r.Daemon.DryRun = true }, "warning", []string{"daemon_dry_run"}},
		{"due now", func(r *serviceReport) { r.Daemon.NextCheckAt = &now }, "ok", nil},
		{"grace boundary", func(r *serviceReport) { t := now.Add(-time.Minute); r.Daemon.NextCheckAt = &t }, "ok", nil},
		{"overdue waiting", func(r *serviceReport) { t := now.Add(-time.Minute - time.Nanosecond); r.Daemon.NextCheckAt = &t }, "warning", []string{"daemon_check_overdue"}},
		{"long running check", func(r *serviceReport) {
			r.Daemon.Phase = "checking"
			t := now.Add(-time.Hour)
			r.Daemon.NextCheckAt = &t
		}, "ok", nil},
		{"missing observation time", func(r *serviceReport) { r.Daemon.ObservedAt = nil; t := now.Add(-time.Hour); r.Daemon.NextCheckAt = &t }, "ok", nil},
		{"unknown due time", func(r *serviceReport) { r.Daemon.NextCheckAt = nil }, "ok", nil},
		{"warning wins over unknown", func(r *serviceReport) { r.ActiveState = "failed"; r.Daemon.BinaryComparison = "unknown" }, "warning", []string{"unit_failed", "daemon_build_unknown"}},
		{"warning follows unknown", func(r *serviceReport) { r.Registration = "unknown"; r.Daemon.State = "unavailable" }, "warning", []string{"registration_unknown", "daemon_unverified"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base()
			tt.change(&r)
			before, _ := json.Marshal(r)
			got := assessServiceHealth(r)
			var codes []string
			for _, f := range got.Findings {
				codes = append(codes, f.Code)
				if f.Message == "" || f.Fix == "" || (f.Status != "warning" && f.Status != "unavailable") {
					t.Fatalf("incomplete finding %+v", f)
				}
			}
			if got.Status != tt.status || !reflect.DeepEqual(codes, tt.codes) {
				t.Fatalf("got %+v; want %s %v", got, tt.status, tt.codes)
			}
			after, _ := json.Marshal(r)
			if string(before) != string(after) {
				t.Fatal("assessment mutated observations")
			}
			if got.Findings == nil {
				t.Fatal("JSON findings must remain an array")
			}
		})
	}
}

func TestServiceHealthDoesNotEchoRawObservations(t *testing.T) {
	r := serviceReport{Backend: "systemd", Recorded: true, DefinitionPresent: true, Registration: "loaded", ActiveState: doctorPrivate, UnitFileState: doctorPrivate, Daemon: &daemonStatus{State: "responsive", BinaryComparison: doctorPrivate, Message: doctorPrivate, NextCheck: doctorPrivate}}
	b, _ := json.Marshal(assessServiceHealth(r))
	if strings.Contains(string(b), doctorPrivate) {
		t.Fatal("raw observation leaked")
	}
}
