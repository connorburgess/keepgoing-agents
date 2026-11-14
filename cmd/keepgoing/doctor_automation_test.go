package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func automationFixture() map[string]any {
	return map[string]any{
		"id": "auto_example", "projectId": "proj_example", "enabled": true,
		"execution": map[string]any{"mode": "script", "script": doctorPrivate, "storedScriptPath": doctorPrivate},
		"name":      doctorPrivate, "lastError": doctorPrivate, "prompt": doctorPrivate,
		"nextRunAt": int64(2000000060000), "lastRunAt": int64(1999999940000), "lastRunStatus": "skipped",
	}
}

func TestDoctorAutomationMetadataAndRedaction(t *testing.T) {
	exe, _ := os.Executable()
	for _, tc := range []struct {
		name, state, check string
		modify             func(map[string]any)
	}{
		{"enabled", "registered", "ok", func(map[string]any) {}},
		{"disabled", "registered", "warning", func(v map[string]any) { v["enabled"] = false }},
		{"failed", "registered", "warning", func(v map[string]any) { v["lastRunStatus"] = "failed" }},
		{"due", "registered", "warning", func(v map[string]any) { v["nextRunAt"] = int64(1999999999999) }},
		{"no_next_run", "registered", "warning", func(v map[string]any) { v["nextRunAt"] = nil }},
		{"unknown_outcome", "registered", "ok", func(v map[string]any) { v["lastRunStatus"] = doctorPrivate }},
		{"wrong_id", "unavailable", "unavailable", func(v map[string]any) { v["id"] = "auto_other" }},
		{"wrong_project", "unavailable", "unavailable", func(v map[string]any) { v["projectId"] = "proj_other" }},
		{"missing_enabled", "unavailable", "unavailable", func(v map[string]any) { delete(v, "enabled") }},
		{"agent_automation", "unavailable", "unavailable", func(v map[string]any) { v["execution"] = map[string]any{"mode": "agent", "prompt": doctorPrivate} }},
		{"unknown_mode", "unavailable", "unavailable", func(v map[string]any) { v["execution"] = map[string]any{"mode": doctorPrivate} }},
		{"wrong_timestamp_type", "unavailable", "unavailable", func(v map[string]any) { v["nextRunAt"] = doctorPrivate }},
		{"invalid_timestamp", "unavailable", "unavailable", func(v map[string]any) { v["nextRunAt"] = -1 }},
		{"overflow_timestamp", "unavailable", "unavailable", func(v map[string]any) { v["lastRunAt"] = int64(253402300800000) }},
		{"error_envelope", "unavailable", "unavailable", func(v map[string]any) { v["error"] = map[string]any{"message": doctorPrivate} }},
		{"not_ok", "unavailable", "unavailable", func(v map[string]any) { v["ok"] = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := automationFixture()
			tc.modify(v)
			b, _ := json.Marshal(v)
			calls := 0
			s := inspectBBAutomation(context.Background(), exe, "auto_example", "proj_example", false, time.Second, func(ctx context.Context, cli string, args []string, env map[string]string, dir string) ([]byte, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > time.Second || cli != exe || env != nil || dir != "" || !reflect.DeepEqual(args, []string{"automation", "show", "auto_example", "--project", "proj_example", "--json"}) {
					t.Fatal("query did not preserve the bounded, read-only command contract")
				}
				return b, nil
			})
			r := doctorReport{CheckedAt: time.UnixMilli(2000000000000), BBAutomation: &s, NextCheck: unknownNextCheck}
			doctorAutomationResult(&r, s)
			out, _ := json.Marshal(r)
			if calls != 1 || s.State != tc.state || doctorCheckByID(t, r, "scheduler.bb").Status != tc.check || s.LoopVerified || r.NextCheck != unknownNextCheck || strings.Contains(string(out), doctorPrivate) {
				t.Fatalf("unexpected report: %s", out)
			}
		})
	}
}

func TestDoctorAutomationUnavailableDoesNotTrustOutput(t *testing.T) {
	exe, _ := os.Executable()
	valid, _ := json.Marshal(automationFixture())
	for _, tc := range []struct {
		out []byte
		err error
	}{
		{valid, errors.New(doctorPrivate)}, {[]byte(doctorPrivate), nil}, {[]byte(`null`), nil},
	} {
		s := inspectBBAutomation(context.Background(), exe, "auto_example", "proj_example", false, time.Second, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
			return tc.out, tc.err
		})
		if s.State != "unavailable" || s.Enabled != nil || strings.Contains(s.Message, doctorPrivate) {
			t.Fatalf("%+v", s)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := inspectBBAutomation(ctx, exe, "auto_example", "proj_example", false, time.Second, func(context.Context, string, []string, map[string]string, string) ([]byte, error) { return valid, nil })
	if s.State != "unavailable" {
		t.Fatal("cancelled query trusted its result")
	}
	s = inspectBBAutomation(context.Background(), filepath.Join(t.TempDir(), "missing"), "auto_example", "proj_example", false, time.Second, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		t.Fatal("missing CLI ran")
		return nil, nil
	})
	if s.State != "unavailable" {
		t.Fatal("missing CLI not reported")
	}
}

func TestDoctorAutomationOfflinePreservesHeldState(t *testing.T) {
	f := newDoctorFixture(t)
	if err := control.Save(f.state, control.File{Enabled: true, Paused: true, Stop: true, Completed: true, Note: doctorPrivate}); err != nil {
		t.Fatal(err)
	}
	before := f.stateFiles(t)
	_, r, _ := f.run(t, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		t.Fatal("offline query executed")
		return nil, nil
	}, "--offline", "--bb-automation", "auto_example", "--bb-project", "proj_example", "--bb-cli", filepath.Join(f.root, "missing"))
	after := f.stateFiles(t)
	if r.BBAutomation == nil || r.BBAutomation.State != "offline" || r.NextCheck != unknownNextCheck || !reflect.DeepEqual(before, after) {
		t.Fatalf("offline query changed state: %+v", r)
	}
}

func TestDoctorAutomationFlagsAndIntegration(t *testing.T) {
	for _, args := range [][]string{
		{"--bb-automation", "auto_example"}, {"--bb-project", "proj_example"},
		{"--bb-automation", "--bad", "--bb-project", "proj_example"},
		{"--bb-automation", "auto_example", "--bb-project", "invalid"}, {"--bb-cli", "example"},
	} {
		var out bytes.Buffer
		if doctor(env{context.Background(), &out, &out}, args, nil) != exitUsage {
			t.Fatalf("invalid selection accepted: %v", args)
		}
	}
	f := newDoctorFixture(t)
	seen := false
	_, r, _ := f.run(t, func(ctx context.Context, cli string, args []string, env map[string]string, dir string) ([]byte, error) {
		if args[0] == "automation" {
			seen = true
			return json.Marshal(automationFixture())
		}
		return successfulDoctorRun(ctx, cli, args, env, dir)
	}, "--bb-automation", "auto_example", "--bb-project", "proj_example", "--bb-cli", f.executable)
	if !seen || r.BBAutomation == nil || r.BBAutomation.State != "registered" {
		t.Fatalf("selection not queried: %+v", r)
	}
	var out bytes.Buffer
	printDoctor(env{context.Background(), &out, &out}, r, false)
	if !strings.Contains(out.String(), "scheduler.bb") || !strings.Contains(out.String(), "loop binding") || strings.Contains(out.String(), doctorPrivate) {
		t.Fatal("text report lost scope or leaked private fields")
	}
}
