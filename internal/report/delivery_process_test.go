package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func processRecord(outcome string) Delivery {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return Delivery{Event: "test", StartedAt: now, UpdatedAt: now, Outcome: outcome, Receipt: "unverified", Attempts: []Attempt{{StartedAt: now, FinishedAt: now, Outcome: outcome}}}
}

func TestProcessDeliveryValidation(t *testing.T) {
	for _, outcome := range []string{"process_accepted", "process_failed", "process_protocol_failed"} {
		t.Run(outcome, func(t *testing.T) {
			d := processRecord(outcome)
			if !validDelivery(d) || DeliveryAccepted(d) != (outcome == "process_accepted") {
				t.Fatal("valid process outcome refused or misclassified")
			}
			for _, mutate := range []func(*Delivery){
				func(d *Delivery) { d.Attempts[0].HTTPStatus = 200 },
				func(d *Delivery) { d.Attempts[0].FinishedAt = time.Time{} },
				func(d *Delivery) { d.Attempts[0].StartedAt = time.Time{} },
				func(d *Delivery) { d.Attempts = append(d.Attempts, d.Attempts[0]) },
				func(d *Delivery) { d.Outcome = "attempt_started" },
				func(d *Delivery) { d.Attempts = nil },
				func(d *Delivery) { d.Attempts[0].Outcome = "http_accepted"; d.Attempts[0].HTTPStatus = 200 },
			} {
				bad := processRecord(outcome)
				mutate(&bad)
				if validDelivery(bad) {
					t.Fatalf("accepted invalid process record: %+v", bad)
				}
			}
		})
	}
}

func TestProcessDeliveryHistoryPromotionAndMixedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	http := processRecord("http_accepted")
	http.Attempts[0].HTTPStatus = 202
	if _, err := saveDelivery(dir, http); err != nil {
		t.Fatal(err)
	}
	if history(t, dir).SchemaVersion != 1 {
		t.Fatal("HTTP-only history was unnecessarily promoted")
	}
	for _, outcome := range []string{"process_accepted", "process_failed", "process_protocol_failed"} {
		if _, err := saveDelivery(dir, processRecord(outcome)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := saveDelivery(dir, http); err != nil {
		t.Fatal(err)
	}
	h := history(t, dir)
	if h.SchemaVersion != 2 || len(h.Records) != 5 || h.Records[0].Outcome != "http_accepted" || h.Records[4].Attempts[0].HTTPStatus != 202 {
		t.Fatalf("mixed history lost or downcast: %+v", h)
	}
	s := InspectDeliveries(dir)
	if s.State != "recorded" || len(s.Warnings) != 2 {
		t.Fatalf("process acceptance counted as warning: %+v", s)
	}
	path := filepath.Join(dir, DeliveryFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Frozen pre-process reader's schema gate: even with otherwise readable
	// fields it refuses any version except 1 before projecting records.
	var oldHeader struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if json.Unmarshal(before, &oldHeader) != nil || oldHeader.SchemaVersion == 1 {
		t.Fatal("old reader would not refuse the promoted envelope")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read/refusal changed history bytes")
	}
	// Process fields cannot be disguised as an old-version record.
	if err := os.WriteFile(path, bytes.Replace(before, []byte(`"schemaVersion":2`), []byte(`"schemaVersion":1`), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeliveries(dir); err != ErrDeliveryHistory {
		t.Fatal("version 1 accepted process outcomes")
	}
}

func TestProcessDeliveryAmbiguousHistoryRefusesSaveWithoutRepair(t *testing.T) {
	dir := t.TempDir()
	if _, err := saveDelivery(dir, processRecord("process_accepted")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, DeliveryFile)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{`"schemaVersion":2`, `"schemaVersion":1,"SchemaVersion":2`},
		{`"outcome":"process_accepted"`, `"outcome":"process_accepted","outcome":null`},
		{`"outcome":"process_accepted"`, `"outcome":"process_failed","Outcome":"process_accepted"`},
		{`"attempts":[{`, `"attempts":[{"httpStatus":200,"HTTPStatus":0,`},
		{`"receipt":"unverified"`, `"receipt":"unverified","Receipt":"unverified"`},
		{`"schemaVersion":2`, `"schemaVersion":null`},
		{`"schemaVersion":2`, `"schemaVersion":2,"unknown":0`},
	} {
		bad := bytes.Replace(good, []byte(pair[0]), []byte(pair[1]), 1)
		if bytes.Equal(good, bad) {
			t.Fatal("fixture failed to mutate history")
		}
		if err := os.WriteFile(path, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDeliveries(dir); err != ErrDeliveryHistory {
			t.Fatalf("accepted damaged history: %s", bad)
		}
		http := processRecord("http_accepted")
		http.Attempts[0].HTTPStatus = 202
		if _, err := saveDelivery(dir, http); err != ErrDeliveryHistory {
			t.Fatal("save repaired damaged history")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bad, after) {
			t.Fatal("refusal changed history bytes")
		}
	}
}
