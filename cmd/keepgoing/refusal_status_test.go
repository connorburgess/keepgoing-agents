package main

import (
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The timeline shows each refusal, each recovery and each model fallback
// after a refusal, under the refusal kind.
func TestTimelineShowsRefusalsAndRecoveries(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	on := &incident.Controls{Enabled: true}
	primary := incident.TargetKey(state.Primary)
	add := func(i int, r incident.Record) {
		r.Kind, r.Adapter, r.TargetKey, r.TargetKeyAfter, r.Outcome = "tick", "bb", primary, primary, "succeeded"
		r.CheckStartedAt, r.ControlBefore, r.ControlAfter = now.Add(time.Duration(i-10)*time.Minute), on, on
		r.Probes, r.Decisions = incident.Probes{Availability: "captured"}, incident.Decisions{Availability: "captured"}
		appendTimelineRecord(t, dir, r)
	}
	add(0, incident.Record{Action: "none", Cause: "refused_wait", Refusal: "refused", Observation: incident.Observation{Availability: "observed", State: "error", ErrorClass: "refused"}})
	add(1, incident.Record{Action: "reprompt", Cause: "refused_continue", Observation: incident.Observation{Availability: "observed", State: "error", ErrorClass: "refused"}})
	add(2, incident.Record{Action: "none", Refusal: "recovered", Observation: incident.Observation{Availability: "observed", State: "idle"}})
	add(3, incident.Record{Action: "none", ModelFallback: true, Observation: incident.Observation{Availability: "observed", State: "idle"}})
	got := timelineJSON(t, dir, "--kind", "refusal")
	if got.SchemaVersion != 8 || len(got.Events) != 3 {
		t.Fatalf("%+v", got)
	}
	_, out, _ := timelineCLI(t, dir, "--kind", "refusal")
	for _, want := range []string{"REFUSAL: the model or provider declined the turn", "REFUSAL RECOVERED", "MODEL FALLBACK: the provider fell back to another model after a refusal"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
}

// status and the panel read the gauge in one line, and an open question in
// another, without any text from the session.
func TestRefusalStatusWording(t *testing.T) {
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	g := state.RefusalGauge{Total: 9, Recovered: 7, Streak: 1, LongestStreak: 3, LastAt: now.Add(-time.Minute)}
	for i := 0; i < 7; i++ {
		g.Recent = append(g.Recent, now.Add(-time.Duration(i)*time.Hour))
	}
	for i := 0; i < 6; i++ {
		g.RecentRecovered = append(g.RecentRecovered, now.Add(-time.Duration(i)*time.Hour))
	}
	g.Recent = append(g.Recent, now.Add(-30*time.Hour)) // outside the day
	r := describeRefusals(g, nil, now)
	if !strings.HasPrefix(r.Text, "7 in the last 24h, 6 recovered by continue; current streak 1") || r.Question != "" {
		t.Fatalf("%+v", r)
	}
	if r := describeRefusals(state.RefusalGauge{}, nil, now); r.Text != "" {
		t.Fatalf("a loop with no refusals: %+v", r)
	}
	q := &control.Question{Version: 1, Kind: control.QuestionRefused, RaisedAt: now, Options: control.QuestionOptions(control.QuestionRefused)}
	if r := describeRefusals(g, q, now); r.QuestionKind != "refused" || !strings.Contains(r.Question, "Answers: continue, fresh_session, pause.") {
		t.Fatalf("%+v", r)
	}
}
