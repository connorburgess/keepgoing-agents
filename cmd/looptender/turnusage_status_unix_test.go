//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/state"
)

func tokens(n int64) *int64 { return &n }

// Each record, through status, to the exact lines shown.
func TestTurnUsagePhrasesFromTheirRecord(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	at := now.Add(-12 * time.Minute)
	record := func(target string, rs ...state.TurnUsageReading) *state.TurnUsageRecord {
		var f state.File
		for _, r := range rs {
			f.NoteTurnUsage(target, r)
		}
		return f.TurnUsage
	}
	full := state.TurnUsageReading{Turn: "turn-b", At: at, Input: tokens(21_000), CachedInput: tokens(13_000), CacheWriteInput: tokens(0), Output: tokens(5), ReasoningOutput: tokens(0)}
	partial := state.TurnUsageReading{Turn: "turn-a", At: at.Add(-time.Hour), Input: tokens(10), Output: tokens(2)}
	var month []state.TurnUsageReading
	for i := range 5 {
		month = append(month, state.TurnUsageReading{Turn: "turn-" + string(rune('c'+i)), At: at.Add(time.Duration(i-4) * 24 * time.Hour), Output: tokens(int64(i + 1))})
	}
	cases := []struct {
		name, status, reason, avail string
		r                           *state.TurnUsageRecord
		reports                     bool
	}{
		{"two turns, one without cached input",
			"tokens per turn: latest turn input 21,000, cached input 13,000, cache write input 0, output 5, reasoning output 0 tokens, as the agent reported; recorded 12 min ago, at the check of 2026-11-16T11:48:00Z; diagnostic only, no decision uses it\n" +
				"tokens per turn by day (UTC, tokens summed over the turns that reported them): 2026-11-16 2 turns: input 21,010, cached input 13,000 (from 1), output 7",
			"", "recorded", record("primary", partial, full), true},
		{"five days, three shown",
			"tokens per turn: latest turn output 5 tokens, as the agent reported; recorded 12 min ago, at the check of 2026-11-16T11:48:00Z; diagnostic only, no decision uses it\n" +
				"tokens per turn by day (UTC, tokens summed over the turns that reported them): 2026-11-16 1 turn: output 5; 2026-11-15 1 turn: output 4; 2026-11-14 1 turn: output 3; 2 days more in status --json",
			"", "recorded", record("primary", month...), true},
		{"nothing recorded yet", "tokens per turn: unavailable; no finished turn of this target has been recorded yet", "not_recorded", "unavailable", nil, true},
		{"a record of another target", "tokens per turn: unavailable; no finished turn of this target has been recorded yet", "not_recorded", "unavailable", record("codex-other", full), true},
		{"an agent that reports none", "tokens per turn: unavailable; this target's agent reports no per-turn token counts that looptender reads", "not_supported", "unavailable", record("primary", full), false},
	}
	for _, c := range cases {
		got := describeTurnUsage(c.r, "primary", c.reports, now)
		var b bytes.Buffer
		printTurnUsage(&b, got)
		if out := strings.TrimSuffix(b.String(), "\n"); out != c.status {
			t.Errorf("%s: status\n got %q\nwant %q", c.name, out, c.status)
		}
		if got.Availability != c.avail || got.Reason != c.reason || got.Version != state.TurnUsageVersion || got.Days == nil {
			t.Errorf("%s: %+v", c.name, got)
		}
		if c.avail == "unavailable" && (len(got.Days) != 0 || got.Latest != nil) {
			t.Errorf("%s: figures shown for an unavailable reading", c.name)
		}
		b.Reset()
		json.NewEncoder(&b).Encode(got)
		if s := b.String(); strings.Contains(s, "turn-") || strings.Contains(s, "$") || strings.Contains(strings.ToLower(s), "cost") {
			t.Errorf("%s: the JSON shows a turn identity or money: %s", c.name, s)
		}
	}
}

// Codex is the only agent whose per-turn usage is read; Claude Code's result
// shape has not been captured.
func TestTargetReportsTurnUsageByAgent(t *testing.T) {
	for adapter, want := range map[string]bool{"codex": true, "claude": false, "bb": false, "cursor": false, "process": false, "custom": false} {
		cfg := &config.Config{Loop: config.Loop{StateDir: t.TempDir(), Agent: config.Agent{Adapter: adapter}}}
		if got := targetReportsTurnUsage(cfg, state.File{}); got != want {
			t.Errorf("%s: %v, want %v", adapter, got, want)
		}
	}
}

// status --json carries the object under its own name, beside contextWindow.
func TestStatusJSONHasTurnUsage(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	_, out, _ := f.run("status", "--json")
	var v map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &v); err != nil || v["turnUsage"] == nil || v["contextWindow"] == nil {
		t.Fatalf("%v: %s", err, out)
	}
	var u turnUsageStatus
	if err := json.Unmarshal(v["turnUsage"], &u); err != nil || u.Version != 1 || u.Reason != "not_supported" {
		t.Fatalf("turnUsage %+v %v", u, err)
	}
	if _, text, _ := f.run("status"); !strings.Contains(text, "tokens per turn: unavailable; this target's agent reports no per-turn token counts") {
		t.Fatalf("status:\n%s", text)
	}
}
