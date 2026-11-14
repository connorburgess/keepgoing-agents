package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestStatusShowsARecordedLongLimitWait(t *testing.T) {
	f := newDoctorFixture(t)
	cfg, err := config.Load(f.config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := func(asJSON bool) (string, limitWaitStatus) {
		t.Helper()
		var out bytes.Buffer
		if status(env{context.Background(), &out, &out}, cfg, now, asJSON, defaultStaleAfter) != exitOK {
			t.Fatal(out.String())
		}
		var v struct {
			LimitWait limitWaitStatus `json:"limitWait"`
		}
		if asJSON && json.Unmarshal(out.Bytes(), &v) != nil {
			t.Fatal(out.String())
		}
		return out.String(), v.LimitWait
	}
	// Nothing recorded: the configured threshold and subscription are still shown in JSON.
	text, _ := run(false)
	_, view := run(true)
	if strings.Contains(text, "long limit wait") || view.State != "none" || view.ReportWaitAfter != "6h0m0s" || view.Target != "" {
		t.Fatalf("%+v\n%s", view, text)
	}
	for _, tc := range []struct {
		outcome, state, line string
		resetIn              time.Duration
	}{
		{state.LimitWaitRequested, "reported", "a limit_wait report was requested for this wait (attempt 1 of at most 3", 60 * time.Hour},
		{state.LimitWaitFailed, "report_failed", "FAILED and is not retried", 60 * time.Hour},
		{state.LimitWaitNotSubscribed, "not_subscribed", "report.events does not include limit_wait", 60 * time.Hour},
		{state.LimitWaitRequested, "reported", "(now passed; the next check decides)", -time.Minute},
	} {
		rec := &state.LimitWaitReport{Target: "primary", ResetsAt: now.Add(tc.resetIn), DetectedAt: now.Add(-12 * time.Hour), Outcome: tc.outcome}
		if err := state.Save(f.state, state.File{LimitWaitReport: rec}); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(f.state, state.FileName))
		text, _ = run(false)
		_, view = run(true)
		if view.State != tc.state || view.Target != "primary" || !view.ResetsAt.Equal(rec.ResetsAt) || !view.DetectedAt.Equal(rec.DetectedAt) || view.ResetPassed != (tc.resetIn < 0) {
			t.Fatalf("%s: %+v", tc.outcome, view)
		}
		for _, want := range []string{"long limit wait: primary was waiting on a provider limit with no handoff possible", tc.line, "The loop is not held and keeps checking."} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s: status lacks %q:\n%s", tc.outcome, want, text)
			}
		}
		if after, _ := os.ReadFile(filepath.Join(f.state, state.FileName)); string(after) != string(before) {
			t.Fatal("status changed state.json")
		}
	}
	// A malformed record is not presented as a wait.
	if got := describeLimitWait(&state.LimitWaitReport{Outcome: state.LimitWaitRequested}, cfg.Loop, now); got.State != "none" {
		t.Fatalf("%+v", got)
	}
}

func TestTimelineShowsTheLongLimitWaitReport(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	on := &incident.Controls{Enabled: true}
	primary := incident.TargetKey(state.Primary)
	for i, mark := range []string{"requested", "", "", "", "delivery_failed", "", "not_subscribed", ""} {
		appendTimelineRecord(t, dir, incident.Record{Kind: "tick", Adapter: "bb", TargetKey: primary, TargetKeyAfter: primary, CheckStartedAt: now.Add(time.Duration(i-10) * 10 * time.Minute), Action: "none", Outcome: "succeeded",
			Cause: "limit_failover_disabled", LimitWaitReport: mark, ControlBefore: on, ControlAfter: on, Observation: incident.Observation{Availability: "observed", State: "error", ErrorClass: "rate_limit"},
			Probes: incident.Probes{Availability: "captured"}, Decisions: incident.Decisions{Availability: "captured"}})
	}
	// An unknown marker is not stored as a report.
	appendTimelineRecord(t, dir, incident.Record{Kind: "tick", CheckStartedAt: now, Action: "none", Outcome: "succeeded", LimitWaitReport: "PRIVATE_MARKER", ControlBefore: on, ControlAfter: on})
	all := timelineJSON(t, dir)
	if all.SchemaVersion != 9 || all.Summary.LastLimitWaitReport == nil || all.Summary.LastLimitWaitReport.Sequence != 7 || all.Summary.LimitWaitChecks != 8 {
		t.Fatalf("%+v", all.Summary)
	}
	got := timelineJSON(t, dir, "--kind", "limit_wait_report")
	if !reflect.DeepEqual(timelineSequences(got), []uint64{1, 5, 7}) {
		t.Fatalf("%v", timelineSequences(got))
	}
	for i, want := range []string{"one report was requested for this wait", "the one report for this wait failed and is not retried", "nothing was sent because report.events does not include limit_wait"} {
		e := got.Events[i]
		if !strings.Contains(e.Description, "LONG LIMIT WAIT") || !strings.Contains(e.Description, want) || !strings.Contains(e.Description, "The loop was not held") || !reflect.DeepEqual(e.Kinds, []string{"check", "limit_wait", "limit_wait_report"}) || e.Incident.Tier != incident.TierCondition {
			t.Fatalf("%v %s", e.Kinds, e.Description)
		}
	}
	// The checks between the reports still collapse; a reporting check never does.
	if !reflect.DeepEqual(timelineSequences(all), []uint64{1, 2, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("%v", timelineSequences(all))
	}
	code, out, errs := timelineCLI(t, dir)
	if code != exitOK || errs != "" || strings.Contains(out, "PRIVATE_MARKER") || !strings.Contains(out, "last recorded long limit-wait report (one per wait; not proof of receipt): incidents.json #7") {
		t.Fatalf("%d %s\n%s", code, errs, out)
	}
}
