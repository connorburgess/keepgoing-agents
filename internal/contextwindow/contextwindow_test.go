package contextwindow

import (
	"strings"
	"testing"
	"time"
)

var (
	now    = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	readAt = now.Add(-3 * time.Hour)
)

func ptr[T any](v T) *T { return &v }

// ready is an input that would reset: every rule passes.
func ready() Input {
	return Input{
		Now:       now,
		ResetAt:   ptr(0.8),
		Supported: true,
		// Read at a turn end three hours ago, after the wake that began
		// the turn; idle since.
		Reading: &Reading{UsedTokens: 170_000, WindowTokens: 200_000, Estimated: ptr(true), At: readAt,
			TurnEndsTracked: true, TurnEndSeenAt: readAt},
		LastSentAt: readAt.Add(-40 * time.Minute),
		Session:    Session{Observed: true, State: "idle"},
	}
}

func dur(d time.Duration) string { return d.String() }

// One row per rule and reason code, in the order the rules are tried. The
// wording is part of the table: each sentence is tied to the record it
// comes from.
func TestDecide(t *testing.T) {
	cases := []struct {
		name       string
		change     func(*Input)
		rule, code string
		text       string
	}{
		{"no threshold", func(in *Input) { in.ResetAt = nil }, RuleConfiguration, NotConfigured, "not configured"},
		{"control.json unreadable", func(in *Input) { in.Controls.Unreadable = true }, RuleControls, ControlsUnreadable, "control.json could not be read"},
		{"stop wins over everything", func(in *Input) { in.Controls = Controls{Stop: true, Paused: true}; in.Reading = nil }, RuleControls, Stopped, "the loop is stopped"},
		{"pause", func(in *Input) { in.Controls.Paused = true }, RuleControls, Paused, "the loop is paused"},
		{"hold", func(in *Input) { in.Controls.Held = true }, RuleControls, Held, "held for a person"},
		{"target reports no reading", func(in *Input) { in.Supported = false }, RuleReading, NotSupported, "reports no context window size"},
		{"no reading", func(in *Input) { in.Reading = nil }, RuleReading, NoReading, "no context window reading is recorded"},
		{"unknown record layout", func(in *Input) { in.Reading.Malformed = true }, RuleReading, MalformedReading, "not one this build can read"},
		{"zero window", func(in *Input) { in.Reading.WindowTokens = 0 }, RuleReading, MalformedReading, "not one this build can read"},
		{"negative used", func(in *Input) { in.Reading.UsedTokens = -1 }, RuleReading, MalformedReading, "not one this build can read"},
		{"reading with no time", func(in *Input) { in.Reading.At = time.Time{} }, RuleReading, MalformedReading, "not one this build can read"},
		{"reading from the future", func(in *Input) { in.Reading.At = now.Add(time.Minute) }, RuleReading, MalformedReading, "not one this build can read"},
		{"estimated flag absent", func(in *Input) { in.Reading.Estimated = nil }, RuleReading, EstimateFlagMissing, "does not say whether BB estimated it"},
		{"record keeps no turn ends", func(in *Input) { in.Reading.TurnEndsTracked = false }, RuleReading, FreshnessUnknown, "cannot show whether a turn has run"},
		{"a send after the reading", func(in *Input) { in.LastSentAt = readAt.Add(time.Minute) }, RuleReading, ReadingTooOld, "looptender sent to the session at or after it"},
		{"read in the check that sent", func(in *Input) { in.LastSentAt = readAt }, RuleReading, ReadingTooOld, "looptender sent to the session at or after it"},
		{"a turn end after the reading", func(in *Input) { in.Reading.TurnEndSeenAt = readAt.Add(time.Minute) }, RuleReading, ReadingTooOld, "a check saw a turn end after it"},
		{"past the safety limit", func(in *Input) {
			in.Reading.At, in.Reading.TurnEndSeenAt, in.LastSentAt = now.Add(-SafetyLimit-time.Second), time.Time{}, time.Time{}
		}, RuleReading, PastSafetyLimit, "24h0m1s old, over the 24h0m0s safety limit"},
		{"no observation", func(in *Input) { in.Session = Session{} }, RuleSession, NoObservation, "no check has observed"},
		{"error", func(in *Input) { in.Session.State = "error"; in.Session.LimitWait = true }, RuleSession, InError, "the session is in error"},
		{"limit wait", func(in *Input) { in.Session.LimitWait = true }, RuleSession, LimitWait, "waiting out a provider limit"},
		{"retry", func(in *Input) { in.Session.Retrying = true }, RuleSession, Retrying, "being retried"},
		{"refusal", func(in *Input) { in.Session.Refusal = true }, RuleSession, Refusal, "the last turn was refused"},
		{"awaiting input", func(in *Input) { in.Session.AwaitingInput = true }, RuleSession, AwaitingInput, "waiting for a person's answer"},
		{"turn running", func(in *Input) { in.Session.State = "active" }, RuleSession, NotIdle, "last seen active, not idle"},
		{"unknown state", func(in *Input) { in.Session.State = "unknown" }, RuleSession, NotIdle, "last seen unknown"},
		{"lanes unverified", func(in *Input) { in.Lanes = Lanes{Unverified: true, Working: 2} }, RuleLanes, LanesUnverified, "could not verify the lanes"},
		{"one lane working", func(in *Input) { in.Lanes.Working = 1 }, RuleLanes, LanesActive, "1 lane is working"},
		{"lanes working", func(in *Input) { in.Lanes.Working = 3 }, RuleLanes, LanesActive, "3 lanes are working"},
		{"below threshold", func(in *Input) { in.Reading.UsedTokens = 159_999 }, RuleThreshold, BelowThreshold, "79% of the window used (159,999 of 200,000 tokens, estimated by BB), below the 80% threshold"},
		{"too soon", func(in *Input) { in.LastReset = ptr(now.Add(-time.Hour)) }, RuleSpacing, TooSoon, "1h0m0s ago, under the 6h0m0s"},
		{"daily cap", func(in *Input) { in.LastReset = ptr(now.Add(-MinInterval)); in.ResetsToday = DailyCap }, RuleSpacing, DailyCapUsed, "2 of 2 resets made today"},
		{"would reset", func(*Input) {}, RuleWouldReset, AtOrAboveThreshold, "would reset: 85% of the window used (170,000 of 200,000 tokens, estimated by BB), at or above the 80% threshold"},
		{"exactly at the threshold", func(in *Input) { in.Reading.UsedTokens = 160_000 }, RuleWouldReset, AtOrAboveThreshold, "80% of the window used"},
		{"reported, not estimated", func(in *Input) { in.Reading.Estimated = ptr(false) }, RuleWouldReset, AtOrAboveThreshold, "reported by BB"},
		{"spaced and under the cap", func(in *Input) { in.LastReset = ptr(now.Add(-MinInterval)); in.ResetsToday = DailyCap - 1 }, RuleWouldReset, AtOrAboveThreshold, "would reset"},
		{"read at a turn end long ago, still idle", func(in *Input) {
			in.Reading.At, in.Reading.TurnEndSeenAt, in.LastSentAt = now.Add(-SafetyLimit), now.Add(-SafetyLimit), now.Add(-SafetyLimit-time.Hour)
		}, RuleWouldReset, AtOrAboveThreshold, "would reset"},
		{"no send and no turn end recorded", func(in *Input) { in.LastSentAt, in.Reading.TurnEndSeenAt = time.Time{}, time.Time{} }, RuleWouldReset, AtOrAboveThreshold, "would reset"},
	}
	for _, c := range cases {
		in := ready()
		c.change(&in)
		d := Decide(in)
		if d.Rule != c.rule || d.Reason != c.code {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, d.Rule, d.Reason, c.rule, c.code)
		}
		if want := c.code == AtOrAboveThreshold; d.WouldReset != want {
			t.Errorf("%s: would reset %v, want %v", c.name, d.WouldReset, want)
		}
		if got := d.Text(dur); !strings.Contains(got, c.text) {
			t.Errorf("%s: text %q lacks %q", c.name, got, c.text)
		}
		if c.code != NotConfigured && (d.SafetyLimit != SafetyLimit || d.MinInterval != MinInterval || d.DailyCap != DailyCap) {
			t.Errorf("%s: the limits are not stated: %+v", c.name, d)
		}
	}
}

// Only the would_reset rule ever says would reset, and every other reason
// says why not.
func TestOnlyOneReasonResets(t *testing.T) {
	for _, code := range []string{NotConfigured, ControlsUnreadable, Stopped, Paused, Held, NotSupported, NoReading, MalformedReading,
		EstimateFlagMissing, FreshnessUnknown, ReadingTooOld, PastSafetyLimit, NoObservation, InError, LimitWait, Retrying, Refusal, AwaitingInput, NotIdle,
		LanesUnverified, LanesActive, BelowThreshold, DailyCapUsed} {
		d := Decision{Reason: code, Estimated: ptr(true)}
		if txt := d.Text(dur); code != NotConfigured && !strings.HasPrefix(txt, "would not reset: ") {
			t.Errorf("%s: %q", code, txt)
		}
	}
}

// BB's estimated flag is carried into the wording, never dropped.
func TestEstimatedFlagIsWorded(t *testing.T) {
	for est, want := range map[bool]string{true: "estimated by BB", false: "reported by BB"} {
		in := ready()
		in.Reading.Estimated = ptr(est)
		d := Decide(in)
		if d.Estimated == nil || *d.Estimated != est || !strings.Contains(d.Text(dur), want) {
			t.Errorf("estimated=%v: %+v %q", est, d, d.Text(dur))
		}
	}
}
