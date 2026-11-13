//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Through the real CLI, BB adapter and signed webhook against a fake bb and a
// local receiver: the owner gets one child_error report when children fail,
// with counts only, and status shows it. An explicit events list that does not
// name the event gets nothing.
func TestEndToEndChildErrorReport(t *testing.T) {
	for _, events := range []string{"default", "explicit with child_error", "explicit without child_error"} {
		t.Run(events, func(t *testing.T) {
			f := newFixture(t)
			cfg, _ := os.ReadFile(f.cfg)
			old := "  report: {events: [handoff, degraded, recovered, stop, await_user]}\n"
			if !strings.Contains(string(cfg), old) {
				t.Fatal("fixture config changed")
			}
			replacement := map[string]string{
				"default":                      "  report: {}\n",
				"explicit with child_error":    "  report: {events: [child_error]}\n",
				"explicit without child_error": old,
			}[events]
			write(t, f.cfg, strings.Replace(string(cfg), old, replacement, 1), 0o600)
			f.run("enable")
			child := func(id, status string) {
				write(t, filepath.Join(f.bbDir, id+".show"), fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":"thr_primary","status":%q,"title":"fictional-private-title"}}`, id, status), 0600)
			}
			write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child_a"},{"id":"thr_child_b"},{"id":"thr_child_c"}]`, 0600)
			child("thr_child_a", "error")
			child("thr_child_b", "error")
			child("thr_child_c", "active")
			for i := 0; i < 3; i++ {
				f.tick()
			}
			f.hooks.mu.Lock()
			body, _ := json.Marshal(f.hooks.events)
			f.hooks.mu.Unlock()
			sent := strings.Count(f.hooks.types(), "keepgoing.child_error")
			wantSent := 1
			if events == "explicit without child_error" {
				wantSent = 0
			}
			if sent != wantSent || strings.Contains(string(body), "thr_child") || strings.Contains(string(body), "fictional-private-title") || strings.Contains(string(body), "Continue the fictional loop") {
				t.Fatalf("%d child_error reports: %s", sent, body)
			}
			if wantSent == 1 && (!strings.Contains(string(body), `"erroredChildren":2`) || !strings.Contains(string(body), `"runningChildren":1`) || !strings.Contains(string(body), `"listingComplete":true`)) {
				t.Fatalf("payload: %s", body)
			}

			code, out, errs := f.run("status")
			wantLine := "  a child_error report was requested (report 1 of at most 3 in this episode); receipt unverified, see notifications. The loop is not held.\n"
			if wantSent == 0 {
				wantLine = "  nothing was sent: report.events does not include child_error. The loop is not held.\n"
			}
			if code != exitOK || !strings.Contains(out, "child errors: 2 direct child thread(s) in error and 1 still running at the check of ") || !strings.Contains(out, wantLine) || strings.Contains(out, "thr_child") {
				t.Fatalf("%d %s %s", code, out, errs)
			}
			var status statusOut
			_, out, _ = f.run("status", "--json")
			if err := json.Unmarshal([]byte(out), &status); err != nil {
				t.Fatal(err)
			}
			r := status.ChildErrorReport
			wantState := map[int]string{1: "reported", 0: "not_subscribed"}[wantSent]
			if r.State != wantState || r.ErroredChildren != 2 || r.RunningChildren != 1 || !r.ListingComplete || r.Subscribed != (wantSent == 1) || r.Reports != wantSent {
				t.Fatalf("%+v", r)
			}

			// Every child recovers. The episode is kept until they have stayed out
			// of error for min_wake_interval, then the record clears.
			child("thr_child_a", "idle")
			child("thr_child_b", "idle")
			child("thr_child_c", "idle")
			f.tick()
			// With nothing reported there is no episode to keep.
			if _, out, _ := f.run("status"); strings.Contains(out, "child errors:") != (wantSent == 1) {
				t.Fatalf("status right after recovery: %s", out)
			}
			later := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
			if code, _, errs := f.run("tick", "-now", later); code != exitOK {
				t.Fatalf("tick -now: %s", errs)
			}
			if _, out, _ := f.run("status"); strings.Contains(out, "child errors:") {
				t.Fatalf("status after recovery: %s", out)
			}
			_, out, _ = f.run("status", "--json")
			if err := json.Unmarshal([]byte(out), &status); err != nil || status.ChildErrorReport.State != "none" {
				t.Fatalf("%v %+v", err, status.ChildErrorReport)
			}
		})
	}
}

// Status says why nothing was sent without pointing at a setting the loop
// does not have, and does not claim nothing was sent when earlier reports of
// the same episode went out.
func TestChildErrorReportStatusWording(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		report string // replaces the fixture's report line
		rec    state.ChildErrorReport
		want   string
	}{
		{"no report section", "", state.ChildErrorReport{Outcome: state.ChildReportNotSubscribed},
			"  nothing was sent: this loop has no report section. The loop is not held.\n"},
		{"not subscribed", "  report: {events: [await_user]}\n", state.ChildErrorReport{Outcome: state.ChildReportNotSubscribed},
			"  nothing was sent: report.events does not include child_error. The loop is not held.\n"},
		{"unsubscribed part-way through an episode", "  report: {events: [await_user]}\n", state.ChildErrorReport{Outcome: state.ChildReportNotSubscribed, Reports: 2},
			"  2 child_error report(s) were requested earlier in this episode; the latest failure was not sent: report.events does not include child_error. The loop is not held.\n"},
		{"capped", "  report: {events: [child_error]}\n", state.ChildErrorReport{Outcome: state.ChildReportCapped, Reports: 3},
			"  more children failed after 3 reports; no further child_error report is sent until every child has recovered. The loop is not held.\n"},
		{"failed", "  report: {events: [child_error]}\n", state.ChildErrorReport{Outcome: state.ChildReportFailed, Reports: 1},
			"  the child_error report FAILED and is not retried; see notifications. The loop is not held.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			cfg, _ := os.ReadFile(f.cfg)
			old := "  report: {events: [handoff, degraded, recovered, stop, await_user]}\n"
			if !strings.Contains(string(cfg), old) {
				t.Fatal("fixture config changed")
			}
			write(t, f.cfg, strings.Replace(string(cfg), old, tc.report, 1), 0o600)
			f.run("enable")
			dir := filepath.Join(f.dir, "state")
			st, _ := state.Load(dir)
			rec := tc.rec
			rec.DetectedAt, rec.Errored, rec.Running, rec.Complete = at, 2, 1, true
			st.ChildErrorReport = &rec
			if err := state.Save(dir, st); err != nil {
				t.Fatal(err)
			}
			code, out, errs := f.run("status")
			if code != exitOK || !strings.Contains(out, "child errors: 2 direct child thread(s) in error and 1 still running at the check of ") || !strings.Contains(out, tc.want) {
				t.Fatalf("%d %s %s", code, out, errs)
			}
		})
	}
}
