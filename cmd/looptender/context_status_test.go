package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/state"
)

// Each record, through status, to the exact lines shown.
func TestContextPhrasesFromTheirRecord(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	at := now.Add(-12 * time.Minute)
	record := func(target string, rs ...state.ContextReading) *state.ContextRecord {
		var f state.File
		for _, r := range rs {
			f.NoteContext(target, r)
		}
		return f.Context
	}
	r := func(used, window int64, estimated bool, at time.Time) state.ContextReading {
		return state.ContextReading{UsedTokens: used, WindowTokens: window, Estimated: estimated, At: at}
	}
	var month []state.ContextReading
	for i := range 5 {
		month = append(month, r(int64(10_000*(i+1)), 200_000, true, at.Add(time.Duration(i-4)*24*time.Hour)))
	}
	cases := []struct {
		name   string
		r      *state.ContextRecord
		isBB   bool
		status string
		reason string
		avail  string
	}{
		{"estimated by BB", record("primary", r(90_000, 200_000, true, at.Add(-time.Hour)), r(48_000, 200_000, true, at)), true,
			"context:   48,000 of 200,000 tokens (24%), estimated by BB; read 12 min ago, at the check of 2026-11-16T11:48:00Z; diagnostic only, no decision uses it\n" +
				"context by day (UTC, tokens, estimated by BB): 2026-11-16 lowest 48,000, highest 90,000, last 48,000 (2 readings)", "", "recorded"},
		{"not marked as an estimate", record("primary", r(1_234_567, 2_000_000, false, at)), true,
			"context:   1,234,567 of 2,000,000 tokens (61%), reported by BB; read 12 min ago, at the check of 2026-11-16T11:48:00Z; diagnostic only, no decision uses it\n" +
				"context by day (UTC, tokens, reported by BB): 2026-11-16 lowest 1,234,567, highest 1,234,567, last 1,234,567 (1 reading)", "", "recorded"},
		{"five days, three shown", record("primary", month...), true,
			"context:   50,000 of 200,000 tokens (25%), estimated by BB; read 12 min ago, at the check of 2026-11-16T11:48:00Z; diagnostic only, no decision uses it\n" +
				"context by day (UTC, tokens, estimated by BB): 2026-11-16 lowest 50,000, highest 50,000, last 50,000 (1 reading); 2026-11-15 lowest 40,000, highest 40,000, last 40,000 (1 reading); 2026-11-14 lowest 30,000, highest 30,000, last 30,000 (1 reading); 2 days more in status --json", "", "recorded"},
		{"no reading yet", nil, true,
			"context:   unavailable; BB has not given a reading for this thread at a check yet", "not_recorded", "unavailable"},
		{"a record of another target", record("claude-other", r(10, 100, true, at)), true,
			"context:   unavailable; BB has not given a reading for this thread at a check yet", "not_recorded", "unavailable"},
		{"a reading with no window", record("primary", r(10, 0, true, at)), true,
			"context:   unavailable; BB has not given a reading for this thread at a check yet", "not_recorded", "unavailable"},
		{"not a BB target", record("primary", r(10, 100, true, at)), false,
			"context:   unavailable; this target's agent reports no context size", "not_supported", "unavailable"},
	}
	for _, c := range cases {
		got := describeContext(c.r, "primary", c.isBB, now)
		var b bytes.Buffer
		printContext(&b, got)
		if out := strings.TrimSuffix(b.String(), "\n"); out != c.status {
			t.Errorf("%s: status\n got %q\nwant %q", c.name, out, c.status)
		}
		if got.Availability != c.avail || got.Reason != c.reason || got.Version != state.ContextVersion || got.Days == nil || got.Recent == nil {
			t.Errorf("%s: %+v", c.name, got)
		}
		if c.avail == "unavailable" && (len(got.Days) != 0 || len(got.Recent) != 0) {
			t.Errorf("%s: history shown for an unavailable reading", c.name)
		}
	}
}

func TestGroupDigits(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 48000: "48,000", 1234567: "1,234,567", -1000: "-1,000"} {
		if got := groupDigits(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}

// Through the real command with a stand-in bb: a wake reads the context
// once, for the supervised thread only; status shows it; a paused check
// reads nothing; a malformed answer changes nothing and warns nothing.
func TestContextReadingThroughStatus(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.context"), `{"usage":{"estimated":true,"modelContextWindow":200000,"usedTokens":48000}}`, 0o600)
	if v := f.tick(); v["action"] != "wake" || v["warnings"] != nil {
		t.Fatalf("wake: %v", v)
	}
	if calls := f.read("calls"); strings.Count(calls, "thread context") != 1 || !strings.Contains(calls, "thread context thr_primary --json\n") {
		t.Fatalf("context calls: %q", calls)
	}
	_, text, _ := f.run("status")
	if !strings.Contains(text, "context:   48,000 of 200,000 tokens (24%), estimated by BB; read ") || !strings.Contains(text, "diagnostic only, no decision uses it") {
		t.Fatalf("status:\n%s", text)
	}
	_, out, _ := f.run("status", "--json")
	var v struct {
		Context contextStatus `json:"context"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Context.Availability != "recorded" || *v.Context.UsedTokens != 48000 || *v.Context.WindowTokens != 200000 || !v.Context.Estimated ||
		v.Context.Version != 1 || len(v.Context.Days) != 1 || v.Context.Days[0].Count != 1 || len(v.Context.Recent) != 1 {
		t.Fatalf("status --json context: %+v %v", v.Context, err)
	}

	f.run("pause")
	before := strings.Count(f.read("calls"), "thread context")
	f.tick()
	if strings.Count(f.read("calls"), "thread context") != before {
		t.Fatal("a paused check read the context")
	}
	f.run("resume")

	write(t, filepath.Join(f.bbDir, "thr_primary.context"), `{"usage":{"estimated":true,"modelContextWindow":"200000","usedTokens":1}}`, 0o600)
	os.Remove(filepath.Join(f.bbDir, "thr_primary.show")) // idle
	before = strings.Count(f.read("calls"), "thread context")
	if v := f.tick(); v["action"] != "wake" || v["warnings"] != nil || v["outcome"] == "failed" {
		t.Fatalf("malformed reading: %v", v)
	}
	if strings.Count(f.read("calls"), "thread context") != before+1 {
		t.Fatal("the malformed reading was not asked for")
	}
	if _, text, _ := f.run("status"); !strings.Contains(text, "context:   48,000 of 200,000 tokens (24%)") {
		t.Fatalf("the last good reading was not kept:\n%s", text)
	}
	_, out, _ = f.run("status", "--json")
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Context.Recent) != 1 || v.Context.Days[0].Count != 1 {
		t.Fatalf("an unavailable reading entered the history: %+v %v", v.Context, err)
	}
}
