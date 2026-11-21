package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/report"
)

func TestProcessDeliveryPresentation(t *testing.T) {
	now := time.Now().UTC()
	for _, outcome := range []string{"process_accepted", "process_failed", "process_protocol_failed"} {
		d := report.Delivery{Event: "test", Outcome: outcome, UpdatedAt: now, Receipt: "unverified", Attempts: []report.Attempt{{StartedAt: now, FinishedAt: now, Outcome: outcome}}}
		s := report.DeliveryStatus{State: "recorded", Latest: &d}
		if outcome != "process_accepted" {
			s.Warnings = []report.Delivery{d}
		}
		var out bytes.Buffer
		printDeliveryStatus(&out, s)
		text := out.String() + describeDelivery(d)
		if strings.Contains(text, "HTTP") || !strings.Contains(text, "receipt unverified") {
			t.Fatalf("incorrect transport/receipt status: %s", text)
		}
		var doctor doctorReport
		doctorDeliveries(&doctor, s)
		b, err := json.Marshal(doctor.Checks)
		if err != nil || bytes.Contains(b, []byte("HTTP")) || len(doctor.Checks) != 1 {
			t.Fatalf("incorrect doctor evidence: %s %v", b, err)
		}
		want := "ok"
		if outcome != "process_accepted" {
			want = "warning"
		}
		if doctor.Checks[0].Status != want {
			t.Fatalf("doctor status %s, want %s", doctor.Checks[0].Status, want)
		}
		var event dockEvent
		dockWordDelivery(&event, d)
		if outcome == "process_accepted" {
			if !strings.Contains(event.Detail, "notification command") || !strings.Contains(event.Detail, "not known") {
				t.Fatal(event)
			}
		} else if !strings.Contains(event.Phrase, "not confirmed") {
			t.Fatal(event)
		}
	}
}
