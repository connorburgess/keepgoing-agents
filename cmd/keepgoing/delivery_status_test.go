//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/report"
)

func TestDeliveryWarningSurvivesHeldChecksAndFreshStatus(t *testing.T) {
	for _, command := range []string{"await-user", "complete", "tick"} {
		t.Run(command, func(t *testing.T) {
			f := newFixture(t)
			subscribeCompletion(t, f)
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			f.run("enable")
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				ctl, err := control.Load(cfg.Loop.StateDir)
				if err != nil || (!ctl.Completed && !ctl.AwaitingUser) {
					t.Error("notification preceded hold")
				}
				w.WriteHeader(503)
			}))
			defer srv.Close()
			t.Setenv("KEEPGOING_WEBHOOK_URL", srv.URL)
			if command == "tick" {
				write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child"}]`, 0600)
				write(t, filepath.Join(f.bbDir, "thr_child.show"), `{"thread":{"id":"thr_child","parentThreadId":"thr_primary","status":"active"}}`, 0600)
				write(t, filepath.Join(f.bbDir, "thr_child.interactions"), `[{"threadId":"thr_child","status":"pending"}]`, 0600)
			}
			code, out, errs := f.run(command)
			if code != 0 {
				t.Fatalf("%d %s %s", code, out, errs)
			}
			wantCalls := int32(2)
			if command == "complete" {
				wantCalls = 1
			}
			if calls.Load() != wantCalls {
				t.Fatalf("attempts %d; output=%s stderr=%s", calls.Load(), out, errs)
			}
			before, err := os.ReadFile(filepath.Join(cfg.Loop.StateDir, report.DeliveryFile))
			if err != nil {
				t.Fatal(err)
			}
			providerCalls := f.read("calls")
			for i := 0; i < 2; i++ {
				f.tick()
			}
			if calls.Load() != wantCalls || f.read("calls") != providerCalls {
				t.Fatal("held check sent a request or queried provider")
			}
			if command == "complete" {
				f.run("complete")
			}
			// Each run reloads config/control/state. Status also works from a copied
			// ledger alone; no in-memory sender state or provider log is required.
			code, out, errs = f.run("status", "--json")
			if code != 0 {
				t.Fatalf("%s %s", out, errs)
			}
			var status statusOut
			if err = json.Unmarshal([]byte(out), &status); err != nil {
				t.Fatal(err)
			}
			if len(status.Notifications.Warnings) != 1 || status.Notifications.Warnings[0].Outcome != "http_rejected" || status.Notifications.Warnings[0].Attempts[0].HTTPStatus != 503 {
				t.Fatal(status.Notifications)
			}
			if !status.Control.AwaitingUser && !status.Control.Completed {
				t.Fatal("hold lost")
			}
			after, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, report.DeliveryFile))
			if !bytes.Equal(before, after) {
				t.Fatal("held check or status rewrote delivery history")
			}
			isolated := t.TempDir()
			os.WriteFile(filepath.Join(isolated, report.DeliveryFile), before, 0600)
			var text bytes.Buffer
			printDeliveryStatus(&text, report.InspectDeliveries(isolated))
			for _, want := range []string{"notification warning (historical #1)", "HTTP 503", "receipt unverified", "held checks do not retry delivery"} {
				if !strings.Contains(text.String(), want) {
					t.Fatal(text.String())
				}
			}
			if calls.Load() != wantCalls {
				t.Fatal("status replayed a report")
			}
		})
	}
}

func TestNotificationStatusUnavailableAndSetupFailure(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.run("enable")
	t.Setenv("KEEPGOING_WEBHOOK_SECRET", "")
	if code, _, errs := f.run("await-user", "--note", "fictional-private-note"); code != 0 {
		t.Fatal(errs)
	}
	s := report.InspectDeliveries(cfg.Loop.StateDir)
	if len(s.Warnings) != 1 || s.Warnings[0].Outcome != "setup_failed" || len(s.Warnings[0].Attempts) != 0 {
		t.Fatal(s)
	}
	b, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, report.DeliveryFile))
	if strings.Contains(string(b), "fictional-private-note") {
		t.Fatal("note persisted")
	}
	os.WriteFile(filepath.Join(cfg.Loop.StateDir, report.DeliveryFile), []byte("corrupt"), 0600)
	code, out, errs := f.run("status")
	if code != 0 || !strings.Contains(out, "delivery history unavailable") || !strings.Contains(out, "awaiting user") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	// Diagnostic storage failure must not undo an already-saved hold or send twice.
	subscribeCompletion(t, f)
	t.Setenv("KEEPGOING_WEBHOOK_SECRET", secret)
	if code, _, errs = f.run("complete"); code != 0 || !strings.Contains(errs, "delivery history unavailable") {
		t.Fatalf("%d %s", code, errs)
	}
	if f.hooks.types() != "keepgoing.complete" {
		t.Fatal(f.hooks.types())
	}
	f.run("complete")
	if f.hooks.types() != "keepgoing.complete" {
		t.Fatal("corrupt ledger caused replay")
	}
	ctl, err := control.Load(cfg.Loop.StateDir)
	// Completion is saved despite the diagnostic failure, and it ends the
	// wait on a person. The report of that wait was never delivered, so its
	// note must survive the completion.
	if err != nil || !ctl.Completed || ctl.AwaitingUser || ctl.OpenQuestion == nil {
		t.Fatal("diagnostic failure lost the completion, or the open question", ctl)
	}
}

func TestReportTestRecordsAcceptanceButDryRunDoesNot(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, errs := f.run("report", "test", "--dry-run"); code != 0 {
		t.Fatal(errs)
	}
	if s := report.InspectDeliveries(cfg.Loop.StateDir); s.State != "not_recorded" {
		t.Fatal(s)
	}
	if code, out, errs := f.run("report", "test"); code != 0 || !strings.Contains(out, "HTTP accepted; receipt unverified") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	s := report.InspectDeliveries(cfg.Loop.StateDir)
	if s.Latest == nil || s.Latest.Event != "test" || s.Latest.Outcome != "http_accepted" || len(s.Warnings) != 0 {
		t.Fatal(s)
	}
}

func TestDoctorDeliveryWarningUsesLocalEvidenceOnly(t *testing.T) {
	f := newFixture(t)
	t.Setenv("KEEPGOING_WEBHOOK_SECRET", "")
	f.run("await-user")
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, report.DeliveryFile))
	code, out, errs := f.run("doctor", "--offline", "--json")
	if code == exitUsage {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	var doc doctorReport
	if err = json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Notifications == nil || len(doc.Notifications.Warnings) != 1 || doc.Notifications.Warnings[0].Outcome != "setup_failed" {
		t.Fatal(doc.Notifications)
	}
	found := false
	for _, c := range doc.Checks {
		if c.ID == "notifications" && c.Status == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing doctor warning")
	}
	after, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, report.DeliveryFile))
	if !bytes.Equal(before, after) || f.read("calls") != "" || f.hooks.types() != "" {
		t.Fatal("offline doctor mutated or contacted a provider")
	}
}
