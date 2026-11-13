//go:build unix

package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

const fakeClaudeWithUsage = `#!/bin/sh
echo "claude $*" >> "$FAKE_BB/probes"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s"}' \
 '{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.22,"resetsAt":1900000000,"status":"allowed"}}}}' \
 '{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"example_window":{"utilization":0.8,"resetsAt":1900003600,"status":"allowed_warning"}}}}' \
 '{"type":"result","subtype":"success","is_error":false,"session_id":"s","result":"OK"}'
`

// A manual check of a Claude candidate records the reading its probe carried.
// The Codex candidate's healthy probe carries none, and the primary was never
// probed, so both stay unavailable.
func TestUsageReadingsInFallbacksListAndStatus(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.dir, "bin", "claude"), fakeClaudeWithUsage, 0o755)
	unavailable := func(out string) {
		t.Helper()
		for _, id := range []string{"claude-other-account", "codex-main"} {
			if !strings.Contains(out, id+": unavailable\n") {
				t.Fatalf("%s not unavailable before any probe: %s", id, out)
			}
		}
	}
	_, out, _ := f.run("fallbacks", "list")
	unavailable(out)
	_, out, _ = f.run("status")
	unavailable(out)
	if !strings.Contains(out, "usage:     primary: unavailable\n") {
		t.Fatalf("status: %s", out)
	}

	if code, _, errs := f.run("fallbacks", "check"); code != exitOK {
		t.Fatalf("check: %d %s", code, errs)
	}
	probes := f.read("probes")
	if strings.Count(probes, "\n") != 2 || !strings.Contains(probes, "--output-format stream-json --verbose") {
		t.Fatalf("one probe per candidate, no extra calls: %q", probes)
	}
	want := "claude-other-account: example_window 80% resets 2030-03-17 18:46Z allowed_warning; seven_day 22% resets 2030-03-17 17:46Z allowed; observed under a minute ago (claude_rate_limit_event)\n"
	for _, cmd := range [][]string{{"fallbacks", "list"}, {"status"}} {
		code, out, errs := f.run(cmd...)
		if code != exitOK || !strings.Contains(out, want) || !strings.Contains(out, "codex-main: unavailable\n") {
			t.Fatalf("%v: %d %s %s", cmd, code, out, errs)
		}
	}

	var list fallbacksOut
	_, out, _ = f.run("fallbacks", "list", "--json")
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Candidates) != 2 {
		t.Fatalf("%v %s", err, out)
	}
	claude, codex := list.Candidates[0].Usage, list.Candidates[1].Usage
	if claude.State != "available" || claude.ObservedAt == nil || claude.AgeSeconds == nil || *claude.AgeSeconds < 0 || claude.Source != "claude_rate_limit_event" ||
		len(claude.Windows) != 2 || claude.Windows[1].Key != "seven_day" || claude.Windows[1].Used != 0.22 || claude.Windows[1].Stale {
		t.Fatalf("claude: %+v", claude)
	}
	if codex.State != "unavailable" || codex.ObservedAt != nil || codex.AgeSeconds != nil || !strings.Contains(out, `"windows": []`) {
		t.Fatalf("codex: %+v %s", codex, out)
	}

	var status statusOut
	_, out, _ = f.run("status", "--json")
	if err := json.Unmarshal([]byte(out), &status); err != nil || len(status.Usage) != 3 {
		t.Fatalf("%v %s", err, out)
	}
	if status.Usage[0].ID != "primary" || status.Usage[0].State != "unavailable" || status.Usage[1].ID != "claude-other-account" || status.Usage[1].State != "available" || status.Usage[2].State != "unavailable" {
		t.Fatalf("status usage: %+v", status.Usage)
	}
	// The stored record holds the reading but none of the probe's output.
	health, _ := os.ReadFile(filepath.Join(f.dir, "state", "health.json"))
	if !strings.Contains(string(health), `"usage"`) || strings.Contains(string(health), "rate_limit_info") || strings.Contains(string(health), "unifiedWindows") {
		t.Fatalf("health record: %s", health)
	}

	// Pointing the same candidate ID at another account hides the reading at
	// once, before any new probe, and the next probe replaces it.
	fallbacks, _ := os.ReadFile(filepath.Join(f.dir, "fallbacks.yaml"))
	write(t, filepath.Join(f.dir, "fallbacks.yaml"), strings.Replace(string(fallbacks), "group: claude-account-b", "group: claude-account-c", 1), 0o600)
	if _, out, _ := f.run("fallbacks", "list"); !strings.Contains(out, "claude-other-account: unavailable\n") {
		t.Fatalf("reading shown for another account: %s", out)
	}
}

// Without fallbacks nothing is probed, so status lists no readings.
func TestStatusListsNoUsageWithoutFallbacks(t *testing.T) {
	f := newFixture(t)
	cfg, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(cfg), "  fallbacks: fallbacks.yaml\n", "", 1), 0o600)
	code, out, errs := f.run("status")
	if code != exitOK || strings.Contains(out, "usage:") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	_, out, _ = f.run("status", "--json")
	if !strings.Contains(out, `"usage": []`) {
		t.Fatalf("json: %s", out)
	}
}

// Readings are display only, shown with their age, and never past their
// window's reset. A candidate reported as fully used but passing its probe is
// still healthy, and a stored value that is not a reading shows as unavailable.
func TestUsageReadingIsDisplayOnlyAndGoesStale(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	claude, _ := cfg.FallbackSet.Find("claude-other-account")
	if err := os.Mkdir(filepath.Join(f.dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.dir, "state", "health.json"), `{"candidates":{
 "claude-other-account":{"last":{"at":"2026-11-09T11:00:00Z","tier":"manual","kind":"claude","ok":true,"latencyMs":5,"exitCode":0},
  "usage":{"at":"2026-11-09T09:30:00Z","source":"claude_rate_limit_event","identity":"`+probe.UsageIdentity(claude)+`","windows":[
   {"key":"full","used":1,"status":"rejected"},
   {"key":"nearly","used":0.995,"resetsAt":"2026-11-09T12:00:01Z"},
   {"key":"past","used":0.8,"resetsAt":"2026-11-09T12:00:00Z","status":"allowed_warning"}]}},
 "codex-main":{"last":{"at":"2026-11-09T11:00:00Z","tier":"manual","kind":"codex","ok":true,"latencyMs":5,"exitCode":0},
  "usage":"not a reading"}}}`, 0o600)
	code, out, errs := f.run("fallbacks", "list", "-now", "2026-11-09T12:00:00Z")
	want := "claude-other-account: full 100% rejected; nearly 99% resets 2026-11-09 12:00Z; past stale (window reset 2026-11-09 12:00Z); observed 2h 30m ago (claude_rate_limit_event)\n"
	if code != exitOK || !strings.Contains(out, want) || !strings.Contains(out, "codex-main: unavailable\n") || !strings.Contains(out, "2 healthy of 2 eligible") || !strings.Contains(out, "handoffs allowed") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	var list fallbacksOut
	_, out, _ = f.run("fallbacks", "list", "--json", "-now", "2026-11-09T12:00:00Z")
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	u := list.Candidates[0].Usage
	if u.State != "available" || *u.AgeSeconds != 9000 || u.Windows[0].Stale || u.Windows[1].Stale || !u.Windows[2].Stale {
		t.Fatalf("%+v", u)
	}
	// Once every window with a reset time has reset and none is open-ended,
	// the whole reading is stale; before the observation, the age is not guessed.
	write(t, filepath.Join(f.dir, "state", "health.json"), `{"candidates":{"claude-other-account":{
  "usage":{"at":"2026-11-09T09:30:00Z","source":"claude_rate_limit_event","identity":"`+probe.UsageIdentity(claude)+`","windows":[
   {"key":"past","used":0.8,"resetsAt":"2026-11-09T12:00:00Z"}]}}}}`, 0o600)
	for now, want := range map[string]string{
		"2026-11-16T13:30:00Z": "claude-other-account: past stale (window reset 2026-11-09 12:00Z); observed 7d 4h ago (claude_rate_limit_event)\n",
		"2026-11-09T09:00:00Z": "claude-other-account: past 80% resets 2026-11-09 12:00Z; observed at a time after this clock's (check the host clock) (claude_rate_limit_event)\n",
		"2026-11-09T09:30:20Z": "claude-other-account: past 80% resets 2026-11-09 12:00Z; observed under a minute ago (claude_rate_limit_event)\n",
		"2026-11-09T10:15:00Z": "claude-other-account: past 80% resets 2026-11-09 12:00Z; observed 45m ago (claude_rate_limit_event)\n",
	} {
		if _, out, _ := f.run("fallbacks", "list", "-now", now); !strings.Contains(out, want) {
			t.Fatalf("at %s: %s", now, out)
		}
	}
	_, out, _ = f.run("fallbacks", "list", "--json", "-now", "2026-11-16T13:30:00Z")
	if !strings.Contains(out, `"state": "stale"`) {
		t.Fatalf("stale state: %s", out)
	}
}

// Each check whose probe carries a reading adds it to the history; no extra
// probe or provider call is made for it. Two readings seconds apart are too
// close to give a rate, and the BB route says where its readings come from.
func TestUsageHistoryFromChecksAndProjectionLines(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.dir, "bin", "claude"), fakeClaudeWithUsage, 0o755)
	for i := 0; i < 2; i++ {
		if code, _, errs := f.run("fallbacks", "check"); code != exitOK {
			t.Fatalf("check: %d %s", code, errs)
		}
	}
	if probes := f.read("probes"); strings.Count(probes, "\n") != 4 {
		t.Fatalf("one probe per candidate per check, no extra calls: %q", probes)
	}
	reg, err := health.Load(filepath.Join(f.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	h := reg.Candidates["claude-other-account"].UsageHistory
	if s := h.Find("seven_day"); s == nil || len(s.Samples) != 2 || h.Find("example_window") == nil {
		t.Fatalf("history: %+v", h)
	}
	want := "projected claude-other-account: example_window can't project: the readings cover less than 6h 0m; readings for a BB thread come only from its probes; seven_day can't project: the readings cover less than 6h 0m; readings for a BB thread come only from its probes\n"
	if _, out, _ := f.run("fallbacks", "list"); !strings.Contains(out, want) || strings.Contains(out, "projected codex-main") {
		t.Fatalf("list: %s", out)
	}
	if _, out, _ := f.run("status"); !strings.Contains(out, "projected: claude-other-account: example_window can't project") {
		t.Fatalf("status: %s", out)
	}
}

// A recorded history projects each window from its own readings, with the
// rules it used, in text and JSON. A reading of another window instance, a
// window without a reset time, and a reset window are not projected.
func TestProjectionFromRecordedHistory(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	claude, _ := cfg.FallbackSet.Find("claude-other-account")
	id := probe.UsageIdentity(claude)
	if err := os.Mkdir(filepath.Join(f.dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.dir, "state", "health.json"), `{"candidates":{"claude-other-account":{
  "usage":{"at":"2026-11-09T12:00:00Z","source":"claude_rate_limit_event","identity":"`+id+`","windows":[
   {"key":"fast","used":0.4,"resetsAt":"2026-11-13T00:00:00Z"},
   {"key":"moved","used":0.3,"resetsAt":"2026-11-20T00:00:00Z"},
   {"key":"open","used":0.5},
   {"key":"slow","used":0.2,"resetsAt":"2026-11-13T00:00:00Z"}]},
  "usageHistory":{"identity":"`+id+`","windows":[
   {"key":"fast","resetsAt":"2026-11-13T00:00:00Z","samples":[{"at":"2026-11-09T00:00:00Z","used":0.1},{"at":"2026-11-09T12:00:00Z","used":0.4}]},
   {"key":"moved","resetsAt":"2026-11-13T00:00:00Z","samples":[{"at":"2026-11-09T00:00:00Z","used":0.1},{"at":"2026-11-09T12:00:00Z","used":0.3}]},
   {"key":"slow","resetsAt":"2026-11-13T00:00:00Z","samples":[{"at":"2026-11-09T00:00:00Z","used":0.1},{"at":"2026-11-09T06:00:00Z","used":0.12},{"at":"2026-11-09T12:00:00Z","used":0.2}]}]}}}}`, 0o600)
	code, out, errs := f.run("fallbacks", "list", "-now", "2026-11-09T13:00:00Z")
	want := "projected claude-other-account: fast runs out about 2d 12h before it resets, at 2026-11-10 12:00Z (2 readings over 12h 0m); " +
		"moved can't project: no earlier reading of this window is recorded; readings for a BB thread come only from its probes; " +
		"open can't project: no reset time was reported; readings for a BB thread come only from its probes; " +
		"slow 90% at reset (3 readings over 12h 0m)\n"
	if code != exitOK || !strings.Contains(out, want) {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	// Seven hours after the newest reading it is older than the probe TTL.
	if _, out, _ := f.run("fallbacks", "list", "-now", "2026-11-09T19:00:01Z"); !strings.Contains(out, "slow can't project: the newest reading is older than 6h 0m") {
		t.Fatalf("too old: %s", out)
	}
	var list fallbacksOut
	_, out, _ = f.run("fallbacks", "list", "--json", "-now", "2026-11-09T13:00:00Z")
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	u := list.Candidates[0].Usage
	fast, moved, slow := u.Windows[0].Projection, u.Windows[1].Projection, u.Windows[3].Projection
	if !u.ProbesOnly || fast.State != "projected" || fast.RunsOutAt == nil || !fast.RunsOutAt.Equal(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)) ||
		moved.State != "unavailable" || moved.Reason != "no_history" || slow.UsedAtReset == nil || math.Abs(*slow.UsedAtReset-0.9) > 1e-9 ||
		slow.Readings != 3 || slow.SpanSeconds != 43200 || slow.MinSpanSeconds != 21600 || slow.MaxAgeSeconds != 21600 {
		t.Fatalf("%+v", u)
	}
	// The same projection is shown whether or not a probe passed; nothing reads it.
	if !strings.Contains(out, `"readingsFromProbesOnly": true`) || list.Candidates[0].Healthy {
		t.Fatalf("json: %s", out)
	}
}

// A route that is not a BB thread is not told its readings come from probes.
func TestProjectionTextOutsideBB(t *testing.T) {
	resets := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	v := usageView{ID: "headless", State: "available", Windows: []usageWindowView{{
		UsageWindow: probe.UsageWindow{Key: "seven_day", Used: 0.2, ResetsAt: &resets},
		Projection:  projectionView{State: "unavailable", Reason: "one_reading", Readings: 1},
	}}}
	if got := v.projectionText(); got != "seven_day can't project: one reading in this window" {
		t.Fatal(got)
	}
}
