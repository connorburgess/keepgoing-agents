//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/allowance"
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
// probe or provider call is made for it. Two readings are too few for a rate,
// and the line says once where the route's readings come from.
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
	want := "projected claude-other-account: example_window can't project: 2 of the 3 readings needed in this window; seven_day can't project: 2 of the 3 readings needed in this window. Readings for this route come only from its probes.\n"
	if _, out, _ := f.run("fallbacks", "list"); !strings.Contains(out, want) || strings.Contains(out, "projected codex-main") {
		t.Fatalf("list: %s", out)
	}
	if _, out, _ := f.run("status"); !strings.Contains(out, "projected: claude-other-account: example_window can't project") {
		t.Fatalf("status: %s", out)
	}
}

// A recorded history projects each window from its own readings, with the
// rules it used, in text and JSON. A reading of another window instance, a
// window without a reset time, and too few readings are not projected.
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
	series := func(key string, used ...float64) string {
		at := []string{"2026-11-09T00:00:00Z", "2026-11-09T06:00:00Z", "2026-11-09T12:00:00Z"}[3-len(used):]
		var samples []string
		for i, u := range used {
			samples = append(samples, fmt.Sprintf(`{"at":%q,"used":%g}`, at[i], u))
		}
		return fmt.Sprintf(`{"key":%q,"resetsAt":"2026-11-11T12:00:00Z","samples":[%s]}`, key, strings.Join(samples, ","))
	}
	write(t, filepath.Join(f.dir, "state", "health.json"), `{"candidates":{"claude-other-account":{
  "usage":{"at":"2026-11-09T12:00:00Z","source":"claude_rate_limit_event","identity":"`+id+`","windows":[
   {"key":"fast","used":0.4,"resetsAt":"2026-11-11T12:00:00Z"},
   {"key":"full","used":1,"resetsAt":"2026-11-11T12:00:00Z"},
   {"key":"moved","used":0.3,"resetsAt":"2026-11-20T00:00:00Z"},
   {"key":"open","used":0.5},
   {"key":"pair","used":0.3,"resetsAt":"2026-11-11T12:00:00Z"},
   {"key":"slow","used":0.2,"resetsAt":"2026-11-11T12:00:00Z"}]},
  "usageHistory":{"identity":"`+id+`","windows":[`+strings.Join([]string{
		series("fast", 0.1, 0.25, 0.4), series("full", 0.6, 1, 1), series("moved", 0.1, 0.2, 0.3),
		series("pair", 0.2, 0.3), series("slow", 0.1, 0.15, 0.2)}, ",")+`]}}}}`, 0o600)
	code, out, errs := f.run("fallbacks", "list", "-now", "2026-11-09T13:00:00Z")
	want := "projected claude-other-account: fast runs out about 24h 0m before it resets, at 2026-11-10 12:00Z (3 readings over 12h 0m); " +
		"full used up since 2026-11-09 06:00Z; " +
		"moved can't project: no earlier reading of this window is recorded; " +
		"open can't project: no reset time was reported; " +
		"pair can't project: 2 of the 3 readings needed in this window; " +
		"slow 60% at reset (3 readings over 12h 0m). Readings for this route come only from its probes.\n"
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
	fast, full, moved, slow := u.Windows[0].Projection, u.Windows[1].Projection, u.Windows[2].Projection, u.Windows[5].Projection
	if !u.ProbesOnly || fast.State != "projected" || fast.RunsOutAt == nil || !fast.RunsOutAt.Equal(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)) ||
		full.UsedUpSince == nil || !full.UsedUpSince.Equal(time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC)) ||
		moved.State != "unavailable" || moved.Reason != "no_history" || slow.UsedAtReset == nil || math.Abs(*slow.UsedAtReset-0.6) > 1e-9 ||
		slow.Readings != 3 || slow.SpanSeconds != 43200 || slow.ResetsInSeconds != 169200 || slow.MinReadings != 3 || slow.MinSpanSeconds != 21600 ||
		slow.MaxAgeSeconds != 21600 || slow.MaxAhead != 4 || slow.Rounding != 0.01 {
		t.Fatalf("%+v", u)
	}
	// Every route says so today, and the projection does not make a route healthy.
	if strings.Count(out, `"readingsFromProbesOnly": true`) != 2 || list.Candidates[0].Healthy {
		t.Fatalf("json: %s", out)
	}
}

// Each kind of projection in words, including a run-out time already past,
// which is not worded as if it were still to come.
func TestProjectionWording(t *testing.T) {
	d := func(h int) time.Time { return time.Date(2026, 11, 9, h, 0, 0, 0, time.UTC) }
	ptr := func(t time.Time) *time.Time { return &t }
	used := func(f float64) *float64 { return &f }
	resets := d(36)
	base := projectionView{State: "projected", Readings: 3, SpanSeconds: 6 * 3600, MinReadings: 3, MinSpanSeconds: 6 * 3600, MaxAgeSeconds: 6 * 3600, now: d(11)}
	with := func(f func(*projectionView)) projectionView { p := base; f(&p); return p }
	for want, p := range map[string]projectionView{
		"w projected to have run out at 2026-11-09 09:00Z, 27h 0m before it resets (3 readings over 6h 0m)": with(func(p *projectionView) { p.UsedAtReset, p.RunsOutAt = used(2.4), ptr(d(9)) }),
		"w runs out about 24h 0m before it resets, at 2026-11-09 12:00Z (3 readings over 6h 0m)":            with(func(p *projectionView) { p.UsedAtReset, p.RunsOutAt = used(2), ptr(d(12)) }),
		"w used up since 2026-11-09 06:00Z":                                                           with(func(p *projectionView) { p.UsedAtReset, p.UsedUpSince = used(1), ptr(d(6)) }),
		"w may run out before it resets, within the readings' rounding (3 readings over 6h 0m)":       with(func(p *projectionView) { p.UsedAtReset, p.WithinRounding = used(1.05), true }),
		"w 100% at reset (3 readings over 6h 0m, 1 stamped after this clock's time ignored)":          with(func(p *projectionView) { p.UsedAtReset, p.Ignored = used(1.2), 1 }),
		"w can't project: too early to project: 6h 0m of readings for a window that resets in 6d 18h": with(func(p *projectionView) { p.State, p.Reason, p.ResetsInSeconds = "unavailable", "too_early", 162*3600 }),
		"w can't project: the readings cover less than 6h 0m":                                         with(func(p *projectionView) { p.State, p.Reason = "unavailable", "span_too_short" }),
		"w can't project: a reading is lower than an earlier one":                                     with(func(p *projectionView) { p.State, p.Reason = "unavailable", "used_went_down" }),
		"w can't project: the window has reset":                                                       with(func(p *projectionView) { p.State, p.Reason = "unavailable", "window_reset" }),
	} {
		if got := p.text("w", &resets); got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	}
	// A route that is not a BB thread is told the same, once, at the end.
	v := usageView{ProbesOnly: true, Windows: []usageWindowView{
		{UsageWindow: probe.UsageWindow{Key: "a"}, Projection: projectionView{State: "unavailable", Reason: "no_reset_time"}},
		{UsageWindow: probe.UsageWindow{Key: "b"}, Projection: projectionView{State: "unavailable", Reason: "no_reset_time"}},
	}}
	if got := v.projectionText(); got != "a can't project: no reset time was reported; b can't project: no reset time was reported. Readings for this route come only from its probes." {
		t.Fatal(got)
	}
	headless := config.Candidate{ID: "headless", Agent: config.Agent{Adapter: "claude", Provider: "claude-code"}}
	if !usageOf(nil, headless, d(11), allowance.DisplayRules(0)).ProbesOnly {
		t.Fatal("a headless route's readings also come only from probes today")
	}
}
