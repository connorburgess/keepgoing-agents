//go:build unix

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/report"
)

func TestTimelineExplainsProcessFailureHoldAndRejectedAlert(t *testing.T) {
	f := newFixture(t)
	journeyConfig(t, f, `  adapter: process
  process:
    status_command: [sh, -c, 'cat "$FAKE_BB/status"']
    resume_command: [sh, -c, 'cat > "$FAKE_BB/sent"']`, 1)
	b, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.cfg, string(b)+"  report: {events: [await_user]}\n", 0600)
	dir := filepath.Join(f.dir, "state")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		ctl, err := control.Load(dir)
		if err != nil || !ctl.AwaitingUser {
			t.Error("report before hold")
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	t.Setenv("KEEPGOING_WEBHOOK_URL", srv.URL)
	write(t, filepath.Join(f.bbDir, "status"), `{"state":"active","errorClass":"none"}`, 0600)
	journeyCommand(t, f, "enable")
	journeyTick(t, f, 0)
	write(t, filepath.Join(f.bbDir, "status"), `{"state":"error","errorClass":"network","message":"PRIVATE_JOURNEY_CONTENT"}`, 0600)
	journeyTick(t, f, 1)
	journeyTick(t, f, 2)
	// Later healthy provider data cannot clear an already-saved hold.
	write(t, filepath.Join(f.bbDir, "status"), `{"state":"active","errorClass":"none"}`, 0600)
	journeyTick(t, f, 3)
	journeyTick(t, f, 4)
	if requests.Load() != 2 {
		t.Fatal(requests.Load())
	}
	before := timelineFiles(t, dir)
	code, out, errs := f.run("timeline", "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	var got timelineReport
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Control.State != "awaiting_user" || got.Summary.HeldChecks != 2 || got.Summary.NotificationWarnings != 1 || got.Summary.LastHoldEntry == nil || got.Summary.LastObservedActive == nil {
		t.Fatal(got.Summary, got.Control)
	}
	foundFailure, foundHold, foundAlert := false, false, false
	for _, ev := range got.Events {
		if ev.Incident != nil {
			r := ev.Incident
			if r.Observation.Source == "process_json" && r.Observation.ErrorClass == "network" {
				foundFailure = true
			}
			if r.Action == "await_user" && r.ControlAfter.AwaitingUser && r.Decisions.Evaluations[0].Input.Reprompts == 1 {
				foundHold = true
			}
		}
		if ev.Delivery != nil && ev.Delivery.Outcome == "http_rejected" && ev.Delivery.Attempts[1].HTTPStatus == 503 {
			foundAlert = true
		}
	}
	if !foundFailure || !foundHold || !foundAlert {
		t.Fatal("combined evidence missing")
	}
	if strings.Contains(out, journeyPrivate) || strings.Contains(out, f.dir) || strings.Contains(out, srv.URL) {
		t.Fatal("private content exposed")
	}
	if !reflect.DeepEqual(before, timelineFiles(t, dir)) || requests.Load() != 2 {
		t.Fatal("diagnosis mutated state or replayed alert")
	}
	// Isolated copies prove the view needs only keepgoing-owned typed history and
	// control flags. No provider, config, scheduler, logs or daemon are consulted.
	isolated := t.TempDir()
	for _, name := range []string{control.FileName, incident.FileName, report.DeliveryFile} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(isolated, name), string(data), 0600)
	}
	code, text, errs := timelineCLI(t, isolated)
	if code != 0 {
		t.Fatal(errs)
	}
	for _, want := range []string{"current control: awaiting_user", "error network", "re-prompts 1/1", "checks with a pre-existing hold: 2", "webhook HTTP 503", "scheduler: unavailable", "supervisor_liveness: unavailable", "ci_waits: unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	// An explicit recovery produces new evidence without erasing the old failed
	// notification or attributing the previous hold to current enabled flags.
	journeyCommand(t, f, "resume")
	journeyTick(t, f, 5)
	code, out, errs = f.run("timeline", "--json")
	if code != 0 {
		t.Fatal(errs)
	}
	if json.Unmarshal([]byte(out), &got) != nil || got.Control.State != "enabled" || got.Summary.NotificationWarnings != 1 || got.Summary.LastObservedActive.Sequence <= got.Summary.LastHoldEntry.Sequence {
		t.Fatal(got.Summary, got.Control)
	}
}
