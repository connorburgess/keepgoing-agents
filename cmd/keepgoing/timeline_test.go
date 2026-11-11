package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/report"
)

func timelineCLI(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := run(context.Background(), append([]string{"timeline", "--state-dir", dir}, args...), &out, &errs)
	return code, out.String(), errs.String()
}
func appendTimelineRecord(t *testing.T, dir string, r incident.Record) {
	t.Helper()
	if err := incident.Append(dir, r); err != nil {
		t.Fatal(err)
	}
}

func TestTimelineOfflineSummaryChronologyAndRedaction(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	enabled := &incident.Controls{Enabled: true}
	held := &incident.Controls{Enabled: true, AwaitingUser: true}
	r := incident.Record{Kind: "tick", Build: "v0.1.2", Adapter: "bb", TargetKey: incident.TargetKey("fictional-private-target"), CheckStartedAt: now.Add(-10 * time.Minute), Action: "none", Outcome: "succeeded", Cause: "retrying", ControlBefore: enabled, ControlAfter: enabled,
		Observation: incident.Observation{Availability: "observed", State: "active", ErrorClass: "network", Retry: "retrying", Source: "bb_provider_error", Category: "stream-disconnected", ProviderCode: "responsestreamdisconnected"}}
	appendTimelineRecord(t, dir, r)
	r.CheckStartedAt = now.Add(-9 * time.Minute)
	r.Action = "reprompt"
	r.Observation.State = "error"
	r.Observation.Retry = "not_retrying"
	appendTimelineRecord(t, dir, r)
	r.CheckStartedAt = now.Add(-8 * time.Minute)
	r.Action = "await_user"
	r.ControlAfter = held
	r.Cause = "other"
	r.Decisions = incident.Decisions{Availability: "captured", Evaluations: []incident.Evaluation{{Stage: "normal", Action: "await_user", Cause: "other", Input: incident.DecisionInput{State: "error", Reprompts: 1, MaxReprompts: 1}}}}
	appendTimelineRecord(t, dir, r)
	r.CheckStartedAt = now.Add(-7 * time.Minute)
	r.ControlBefore = held
	r.Action = "none"
	r.Cause = "awaiting_user"
	r.Observation = incident.Observation{Availability: "not_observed"}
	r.Decisions = incident.Decisions{Availability: "captured"}
	appendTimelineRecord(t, dir, r)
	// Unknown diagnostic strings are normalized by the incident storage reader.
	r.CheckStartedAt = now.Add(-6 * time.Minute)
	r.Build = "PRIVATE_TIMELINE_MARKER"
	r.Cause = "PRIVATE_TIMELINE_MARKER"
	appendTimelineRecord(t, dir, r)
	if err := control.Save(dir, control.File{Enabled: true, AwaitingUser: true, Note: "PRIVATE_TIMELINE_MARKER"}); err != nil {
		t.Fatal(err)
	}
	// Record a real local rejection; no raw request content belongs in the view.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer srv.Close()
	t.Setenv("TIMELINE_URL", srv.URL)
	t.Setenv("TIMELINE_SECRET", "fictional-private-key")
	sender, err := report.NewRecorded(config.Report{URLEnv: "TIMELINE_URL", SecretEnv: "TIMELINE_SECRET", Scheme: "bearer", Events: []string{"await_user"}, Timeout: config.Duration{Duration: time.Second}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if sender.Report(context.Background(), report.Event{Type: "await_user", Text: "PRIVATE_TIMELINE_MARKER"}) == nil {
		t.Fatal("webhook unexpectedly accepted")
	}
	before := timelineFiles(t, dir)
	code, out, errs := timelineCLI(t, dir, "--json")
	if code != 0 || errs != "" {
		t.Fatalf("%d %s", code, errs)
	}
	var got timelineReport
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Control.State != "awaiting_user" || got.Summary.RetainedChecks != 5 || got.Summary.HeldChecks != 2 || got.Summary.NotificationWarnings != 1 {
		t.Fatalf("%+v", got)
	}
	if got.Summary.LastHoldEntry.Sequence != 3 || got.Summary.LastObservedActive.Sequence != 1 || got.Summary.LastSendAction.Sequence != 2 || got.Summary.LatestCheck.Sequence != 5 {
		t.Fatal(got.Summary)
	}
	if len(got.Events) != 6 || got.Events[5].Delivery == nil || got.Events[5].Delivery.Attempts[0].HTTPStatus != 400 {
		t.Fatal(got.Events)
	}
	for i := 1; i < len(got.Events); i++ {
		if got.Events[i].At.Before(got.Events[i-1].At) {
			t.Fatal("events unsorted")
		}
	}
	for _, private := range []string{"PRIVATE_TIMELINE_MARKER", dir, srv.URL, "fictional-private-key", "fictional-private-target"} {
		if strings.Contains(out, private) {
			t.Fatalf("leaked %q", private)
		}
	}
	for _, layer := range got.Layers {
		if layer.Layer == "scheduler" || layer.Layer == "supervisor_liveness" || layer.Layer == "ci_waits" {
			if layer.Availability != "unavailable" {
				t.Fatal(layer)
			}
		}
	}
	code, text, errs := timelineCLI(t, dir)
	if code != 0 {
		t.Fatal(errs)
	}
	for _, want := range []string{"current control: awaiting_user", "checks with a pre-existing hold: 2", "retry retrying", "re-prompts 1/1", "webhook HTTP 400", "receipt unverified", "internet root cause", "not attributed to current flags"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if !reflect.DeepEqual(before, timelineFiles(t, dir)) {
		t.Fatal("timeline changed runtime files")
	}
	// Display limiting does not hide warnings or change source-wide summaries.
	limited := readTimeline(dir, time.Now().UTC(), time.Hour, 1)
	if limited.Omitted != 5 || len(limited.Events) != 1 || limited.Summary.NotificationWarnings != 1 || limited.Summary.LastHoldEntry.Sequence != 3 {
		t.Fatal(limited)
	}
}

func timelineFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestTimelineMissingCorruptAndBoundedSources(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	code, out, errs := timelineCLI(t, dir, "--json")
	if code != 0 || errs != "" {
		t.Fatal(code, errs)
	}
	var got timelineReport
	json.Unmarshal([]byte(out), &got)
	if got.Control.Availability != "not_recorded" || got.Control.State != "disabled" || len(got.Events) != 0 || got.Summary.LatestCheck != nil {
		t.Fatal(got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("missing directory created")
	}
	for _, name := range []string{control.FileName, incident.FileName, report.DeliveryFile} {
		for _, data := range []string{"PRIVATE_BROKEN_CONTENT", strings.Repeat("x", incident.MaxBytes+1)} {
			t.Run(name+data[:1], func(t *testing.T) {
				dir := t.TempDir()
				p := filepath.Join(dir, name)
				os.WriteFile(p, []byte(data), 0600)
				code, out, errs := timelineCLI(t, dir, "--json")
				if code != exitFailed || strings.Contains(out+errs, "PRIVATE_BROKEN_CONTENT") || strings.Contains(out+errs, dir) {
					t.Fatalf("%d %s %s", code, out, errs)
				}
				if json.Unmarshal([]byte(out), &got) != nil {
					t.Fatal("partial report missing")
				}
				after, _ := os.ReadFile(p)
				if string(after) != data {
					t.Fatal("corrupt evidence changed")
				}
			})
		}
	}
}

func TestTimelineClockOrderingDryRunsAndControlPrecedence(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	enabled := &incident.Controls{Enabled: true}
	r := incident.Record{Kind: "tick", CheckStartedAt: now.Add(-time.Hour), Action: "none", Outcome: "succeeded", ControlBefore: enabled, ControlAfter: enabled, Observation: incident.Observation{Availability: "observed", State: "active"}}
	appendTimelineRecord(t, dir, r)
	r.CheckStartedAt = now.Add(time.Hour)
	r.DryRun = true
	r.Outcome = "dry_run"
	r.Action = "wake"
	r.ControlAfter = &incident.Controls{Enabled: true, AwaitingUser: true}
	appendTimelineRecord(t, dir, r)
	// Append-order latest is older by clock; retain both kinds of evidence.
	r.CheckStartedAt = now.Add(-2 * time.Hour)
	r.Observation = incident.Observation{Availability: "not_observed"}
	r.DryRun = false
	r.Outcome = "succeeded"
	r.Action = "none"
	r.ControlBefore = r.ControlAfter
	appendTimelineRecord(t, dir, r)
	control.Save(dir, control.File{Stop: true, Paused: true, AwaitingUser: true, Completed: true, Note: "private"})
	got := readTimeline(dir, now, time.Minute, 320)
	if got.Control.State != "stop_requested" || got.Summary.LatestCheck.Sequence != 3 || got.Summary.LatestCheck.Freshness != "stale" || got.Summary.LastObservedActive.Sequence != 1 || got.Summary.LastSendAction != nil || got.Summary.LastHoldEntry != nil {
		t.Fatal(got.Summary, got.Control)
	}
	if got.Events[2].Freshness != "future" || !strings.Contains(got.Events[2].Description, "DRY RUN") {
		t.Fatal(got.Events)
	}
	if got.Summary.HeldChecks != 1 {
		t.Fatal(got.Summary)
	}
	// Holds remain current even when observation timestamps are old or future.
	for _, tc := range []struct {
		flags incident.Controls
		want  string
	}{
		{incident.Controls{}, "disabled"}, {incident.Controls{Enabled: true, Paused: true, AwaitingUser: true}, "paused"}, {incident.Controls{Enabled: true, AwaitingUser: true, Completed: true}, "awaiting_user"}, {incident.Controls{Enabled: true, Completed: true}, "completed"}, {incident.Controls{Enabled: true}, "enabled"},
	} {
		if g := timelineGate(&tc.flags); g != tc.want {
			t.Fatal(g)
		}
	}
}

func TestTimelineSourceIsolationAndArguments(t *testing.T) {
	dir := t.TempDir()
	appendTimelineRecord(t, dir, incident.Record{Kind: "tick", CheckStartedAt: time.Now(), Action: "none", Outcome: "succeeded"})
	os.WriteFile(filepath.Join(dir, report.DeliveryFile), []byte("corrupt"), 0600)
	code, out, _ := timelineCLI(t, dir, "--json")
	var got timelineReport
	json.Unmarshal([]byte(out), &got)
	if code != exitFailed || len(got.Events) != 1 || got.Sources[1].Availability != "unavailable" {
		t.Fatal(code, got)
	}
	for _, args := range [][]string{{"--limit", "0"}, {"--limit", "321"}, {"--stale-after", "0s"}, {"-c", "elsewhere"}, {"extra"}, {"--now", "2030-01-01T00:00:00Z"}} {
		code, _, _ := timelineCLI(t, dir, args...)
		if code != exitUsage {
			t.Fatal(args, code)
		}
	}
	path := filepath.Join(t.TempDir(), "broken.yaml")
	os.WriteFile(path, []byte("PRIVATE_CONFIG_VALUE: ["), 0600)
	var stdout, stderr bytes.Buffer
	code = run(context.Background(), []string{"timeline", "-c", path}, &stdout, &stderr)
	if code != exitUsage || strings.Contains(stderr.String(), "PRIVATE_CONFIG_VALUE") || strings.Contains(stderr.String(), path) {
		t.Fatal(code, stderr.String())
	}
}

func TestTimelineRetentionAndLegacyEvidence(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().UTC()
	for i := 0; i < incident.MaxRecords+1; i++ {
		appendTimelineRecord(t, dir, incident.Record{Kind: "tick", CheckStartedAt: at.Add(time.Duration(i) * time.Second), Action: "none", Outcome: "succeeded"})
	}
	got := readTimeline(dir, at, time.Hour, 3)
	if got.Sources[0].Dropped != 1 || got.Sources[0].Retained != 256 || got.Omitted != 253 || len(got.Events) != 3 {
		t.Fatal(got.Sources, got.Omitted)
	}
	// Version-1 loading does not fabricate source/decision metadata or mutate it.
	legacy := t.TempDir()
	j := incident.Journal{SchemaVersion: 1, NextSequence: 2, Records: []incident.Record{{Sequence: 1, CheckStartedAt: at, Action: "none", Observation: incident.Observed(decide.Observation{State: decide.Active}, "bb", false)}}}
	b, _ := json.Marshal(j)
	os.WriteFile(filepath.Join(legacy, incident.FileName), b, 0600)
	got = readTimeline(legacy, at, time.Hour, 40)
	if got.Events[0].Incident.Decisions.Availability != "unavailable" || got.Events[0].Incident.Observation.Source != "unavailable" {
		t.Fatal(got.Events)
	}
	after, _ := os.ReadFile(filepath.Join(legacy, incident.FileName))
	if !bytes.Equal(b, after) {
		t.Fatal("legacy read rewrote file")
	}
}

func TestTimelineRefusesControlAliasesWithoutLosingOtherEvidence(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "control.json")
	if err := os.WriteFile(target, []byte(`{"enabled":true,"note":"PRIVATE_ALIAS_NOTE"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, control.FileName)); err != nil {
		t.Skip("symlink unavailable")
	}
	appendTimelineRecord(t, dir, incident.Record{Kind: "tick", CheckStartedAt: time.Now(), Action: "none", Outcome: "succeeded"})
	code, out, errs := timelineCLI(t, dir, "--json")
	var got timelineReport
	if code != exitFailed || json.Unmarshal([]byte(out), &got) != nil || got.Control.Availability != "unavailable" || len(got.Events) != 1 || strings.Contains(out+errs, "PRIVATE_ALIAS_NOTE") {
		t.Fatal(code, out, errs)
	}
}
