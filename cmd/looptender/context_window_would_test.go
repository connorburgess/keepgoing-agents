package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/rungraph"
	"github.com/looptender/looptender/internal/state"
)

var cwNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func cwConfig(resetAt *float64) *config.Config {
	cfg := &config.Config{}
	cfg.Loop.Adapter = "bb"
	cfg.Loop.Agent.BB = &config.BB{Thread: "thr_example123"}
	if resetAt != nil {
		cfg.Loop.ContextWindow = &config.ContextWindow{ResetAt: resetAt}
	}
	return cfg
}

// cwRecords are records on which the preview would reset: a fresh reading
// at 85%, an idle observation of the primary and a complete lane listing
// with only an errored lane, all from the same check.
func cwRecords() (state.File, control.File, *rungraph.Graph) {
	// The turn began with a wake five hours ago and ended at a check three
	// hours ago, which read the context; the session has been idle since,
	// with a quiet check a minute ago.
	readAt, checked := cwNow.Add(-3*time.Hour), cwNow.Add(-time.Minute)
	st := state.File{LastSentAt: cwNow.Add(-5 * time.Hour)}
	st.NoteTurnEnd(state.Primary, readAt) // no record yet: nothing to note
	st.NoteContext(state.Primary, state.ContextReading{UsedTokens: 170_000, WindowTokens: 200_000, Estimated: true, At: readAt})
	st.NoteTurnEnd(state.Primary, readAt)
	st.LastCheck = &state.Check{At: checked, Target: state.Primary, ObservedState: "idle", Action: "none", Outcome: "succeeded"}
	g := &rungraph.Graph{Waiting: &rungraph.Waiting{Target: state.Primary, CheckedAt: checked, Listing: rungraph.ListingComplete,
		Children: []rungraph.Child{{Key: "lane-a", Status: rungraph.ChildErrored}}}}
	return st, control.File{Enabled: true}, g
}

// Each row changes one record and names the code and words status shows.
func TestContextWindowWouldFromRecords(t *testing.T) {
	threshold := 0.8
	cases := []struct {
		name       string
		resetAt    *float64
		change     func(*state.File, *control.File, **rungraph.Graph, *bool)
		rule, code string
		text       string
	}{
		{"would reset", &threshold, nil, "would_reset", "at_or_above_threshold",
			"context window reset at the next turn boundary: would reset: 85% of the window used (170,000 of 200,000 tokens, estimated by BB), at or above the 80% threshold, the session idle after a turn that ended normally and no lane working; preview only: this build never resets, compacts, sends or asks for a checkpoint"},
		{"not configured", nil, nil, "configuration", "not_configured", "not configured"},
		{"stopped", &threshold, func(_ *state.File, c *control.File, _ **rungraph.Graph, _ *bool) { c.Stop = true }, "controls", "stopped", "the loop is stopped"},
		{"paused", &threshold, func(_ *state.File, c *control.File, _ **rungraph.Graph, _ *bool) { c.Paused = true }, "controls", "paused", "the loop is paused"},
		{"disabled", &threshold, func(_ *state.File, c *control.File, _ **rungraph.Graph, _ *bool) { c.Enabled = false }, "controls", "held", "held for a person"},
		{"control.json unreadable", &threshold, func(_ *state.File, _ *control.File, _ **rungraph.Graph, u *bool) { *u = true }, "controls", "controls_unreadable", "control.json could not be read"},
		{"no reading", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) { s.Context = nil }, "reading", "no_reading", "no context window reading"},
		{"reading of another target", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) {
			s.Context.Target = "example-fallback"
		}, "reading", "no_reading", "no context window reading"},
		{"unknown record layout", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) { s.Context.Version = 2 }, "reading", "malformed_reading", "not one this build can read"},
		{"a send after the reading", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) {
			s.LastSentAt = cwNow.Add(-2 * time.Hour)
		}, "reading", "reading_too_old", "a turn has run since the latest reading (looptender sent"},
		{"a turn end after the reading", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) {
			s.NoteTurnEnd(state.Primary, cwNow.Add(-10*time.Minute))
		}, "reading", "reading_too_old", "a check saw a turn end after it"},
		{"record without turn ends", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) {
			s.Context.TurnEndsTracked, s.Context.TurnEndSeenAt = false, time.Time{}
		}, "reading", "freshness_unknown", "cannot show whether a turn has run"},
		{"past the safety limit", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) {
			s.Context.Latest.At, s.Context.TurnEndSeenAt, s.LastSentAt = cwNow.Add(-25*time.Hour), cwNow.Add(-25*time.Hour), cwNow.Add(-26*time.Hour)
		}, "reading", "past_safety_limit", "25h 0m old, over the 24h 0m safety limit"},
		{"target not BB", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) { s.ActiveTarget = "example-headless" }, "reading", "not_supported", "reports no context window size"},
		{"no check", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) { s.LastCheck = nil }, "session", "no_observation", "no check has observed"},
		{"error", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) { s.LastCheck.ObservedState = "error" }, "session", "in_error", "in error"},
		{"limit wait", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) {
			s.LimitResetsAt = cwNow.Add(time.Hour)
		}, "session", "limit_wait", "provider limit"},
		{"retry signal", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) { s.LastCheck.RetrySignal = "expired" }, "session", "retrying", "being retried"},
		{"turn retried at the check", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) {
			s.LastTurnRetry = &state.TurnRetry{CheckAt: s.LastCheck.At, Target: state.Primary, Outcome: "sent"}
		}, "session", "retrying", "being retried"},
		{"refusal streak", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) { s.Refusals.Streak = 1 }, "session", "refusal", "refused"},
		{"awaiting input", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) { s.LastCheck.AwaitingInput = true }, "session", "awaiting_input", "a person's answer"},
		{"turn running", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) {
			s.LastCheck.ObservedState = "active"
		}, "session", "not_idle", "last seen active"},
		{"no lane listing", &threshold, func(_ *state.File, _ *control.File, g **rungraph.Graph, _ *bool) { *g = nil }, "lanes", "lanes_unverified", "could not verify the lanes"},
		{"incomplete listing", &threshold, func(_ *state.File, _ *control.File, g **rungraph.Graph, _ *bool) {
			(*g).Waiting.Listing, (*g).Waiting.Children = rungraph.ListingIncomplete, nil
		}, "lanes", "lanes_unverified", "could not verify the lanes"},
		{"listing older than the check", &threshold, func(_ *state.File, _ *control.File, g **rungraph.Graph, _ *bool) {
			(*g).Waiting.CheckedAt = cwNow.Add(-time.Hour)
		}, "lanes", "lanes_unverified", "could not verify the lanes"},
		{"lanes working", &threshold, func(_ *state.File, _ *control.File, g **rungraph.Graph, _ *bool) {
			(*g).Waiting.Children = append((*g).Waiting.Children, rungraph.Child{Key: "lane-b", Status: rungraph.ChildActive}, rungraph.Child{Key: "lane-c", Status: rungraph.ChildAwaiting})
		}, "lanes", "lanes_active", "2 lanes are working"},
		{"below threshold", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) {
			s.Context.Latest.UsedTokens = 100_000
		}, "threshold", "below_threshold", "50% of the window used"},
		{"reported, not estimated", &threshold, func(s *state.File, _ *control.File, _ **rungraph.Graph, _ *bool) { s.Context.Latest.Estimated = false }, "would_reset", "at_or_above_threshold", "reported by BB"},
	}
	for _, c := range cases {
		st, ctl, g := cwRecords()
		unreadable := false
		if c.change != nil {
			c.change(&st, &ctl, &g, &unreadable)
		}
		got := contextWindowWouldOf(cwConfig(c.resetAt), st, ctl, unreadable, g, cwNow)
		if got.Rule != c.rule || got.Reason != c.code || got.WouldReset != (c.code == "at_or_above_threshold") {
			t.Errorf("%s: got %s/%s would=%v, want %s/%s", c.name, got.Rule, got.Reason, got.WouldReset, c.rule, c.code)
		}
		if !strings.Contains(got.Text, c.text) {
			t.Errorf("%s: text %q lacks %q", c.name, got.Text, c.text)
		}
		if !got.Preview || got.Version != 1 {
			t.Errorf("%s: not marked as a preview: %+v", c.name, got)
		}
		if c.resetAt != nil && (!strings.HasSuffix(got.Text, contextWindowPreviewNote) || got.SafetyLimitSeconds != 86400 || got.MinIntervalSeconds != 6*3600 || got.DailyCap != 2) {
			t.Errorf("%s: preview note or limits missing: %+v", c.name, got)
		}
	}
}

// A loop without the key shows nothing new in text, and a fixed value in
// JSON.
func TestContextWindowWouldNotConfigured(t *testing.T) {
	st, ctl, g := cwRecords()
	got := contextWindowWouldOf(cwConfig(nil), st, ctl, false, g, cwNow)
	b, _ := json.Marshal(got)
	if want := `{"version":1,"preview":true,"configured":false,"wouldReset":false,"rule":"configuration","reason":"not_configured","text":"not configured"}`; string(b) != want {
		t.Fatalf("json %s, want %s", b, want)
	}
	var buf bytes.Buffer
	printContextWindowWould(&buf, got)
	if buf.Len() != 0 {
		t.Fatalf("printed %q for a loop without the key", buf.String())
	}
	threshold := 0.8
	printContextWindowWould(&buf, contextWindowWouldOf(cwConfig(&threshold), st, ctl, false, g, cwNow))
	if !strings.HasPrefix(buf.String(), "context window reset at the next turn boundary: would reset") {
		t.Fatalf("printed %q", buf.String())
	}
}
