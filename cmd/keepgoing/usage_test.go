//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	want := "claude-other-account: example_window 80% resets 2030-03-17 18:46Z allowed_warning; seven_day 22% resets 2030-03-17 17:46Z allowed; observed 0s ago (claude_rate_limit_event)\n"
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
		len(claude.Windows) != 2 || claude.Windows[1].Key != "seven_day" || claude.Windows[1].Used != 0.22 {
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
}

// Readings are display only: a candidate reported as fully used but passing
// its probe is still healthy, and a stored value that is not a reading shows
// as unavailable without breaking the list.
func TestUsageReadingIsDisplayOnly(t *testing.T) {
	f := newFixture(t)
	if err := os.Mkdir(filepath.Join(f.dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.dir, "state", "health.json"), `{"candidates":{
 "claude-other-account":{"last":{"at":"2026-11-09T11:00:00Z","tier":"manual","kind":"claude","ok":true,"latencyMs":5,"exitCode":0},
  "usage":{"at":"2026-11-09T09:30:00Z","source":"claude_rate_limit_event","windows":[{"key":"seven_day","used":1,"status":"rejected"}]}},
 "codex-main":{"last":{"at":"2026-11-09T11:00:00Z","tier":"manual","kind":"codex","ok":true,"latencyMs":5,"exitCode":0},
  "usage":"not a reading"}}}`, 0o600)
	code, out, errs := f.run("fallbacks", "list", "-now", "2026-11-09T12:00:00Z")
	if code != exitOK || !strings.Contains(out, "claude-other-account: seven_day 100% rejected; observed 2h30m0s ago (claude_rate_limit_event)\n") ||
		!strings.Contains(out, "codex-main: unavailable\n") || !strings.Contains(out, "2 healthy of 2 eligible") || !strings.Contains(out, "handoffs allowed") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
}
