package timing

import (
	"fmt"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/allowance"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

var now = time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)

func h(n float64) time.Time { return now.Add(time.Duration(n * float64(time.Hour))) }

func dur(n float64) config.Duration {
	return config.Duration{Duration: time.Duration(n * float64(time.Hour))}
}

// steps cheapest to strongest: example-small, primary, example-large.
func cfg() config.Timing {
	return config.Timing{Enabled: true, Windows: []string{"seven_day", "five_hour"}, NearReset: dur(6), ProbeEvery: dur(12),
		Steps: []string{"example-small", "primary", "example-large"}, Ceiling: "example-large", UnusedAtLeast: 0.25, DownAt: 0.95, MinOnStep: dur(2)}
}

type win struct {
	key    string
	used   float64
	resets float64 // hours from now
}

func reading(age float64, ws ...win) *probe.Usage {
	u := &probe.Usage{At: h(-age), Source: probe.UsageSourceClaude, Identity: "account-a"}
	for _, w := range ws {
		at := h(w.resets)
		u.Windows = append(u.Windows, probe.UsageWindow{Key: w.key, Used: w.used, ResetsAt: &at})
	}
	return u
}

// history is one series: readings (hours from now, used) of the window
// instance resetting at resets hours from now.
func history(key string, resets float64, points ...[2]float64) *allowance.History {
	s := allowance.Series{Key: key, ResetsAt: h(resets)}
	for _, p := range points {
		s.Samples = append(s.Samples, allowance.Sample{At: h(p[0]), Used: p[1]})
	}
	return &allowance.History{Identity: "account-a", Windows: []allowance.Series{s}}
}

func base() Input {
	return Input{Now: now, Timing: cfg(), ProbeTTL: 6 * time.Hour, Current: First, TurnEnded: true}
}

func with(f func(*Input)) Input { in := base(); f(&in); return in }

// A seven-day window resetting in 72 h, read at 40% just now, rising 1% an
// hour over the last 24 h: projected 112% at reset.
func risingWeek(in *Input) {
	in.Usage = reading(0, win{"seven_day", 0.40, 72})
	in.History = history("seven_day", 72, [2]float64{-24, 0.16}, [2]float64{-12, 0.28}, [2]float64{0, 0.40})
}

// A seven-day window resetting in 2 h, read 20 min ago at 60%: 40% unused.
func nearWeek(in *Input) {
	in.Usage = reading(1.0/3, win{"seven_day", 0.60, 2})
}

func TestDecideTable(t *testing.T) {
	for _, c := range []struct {
		name         string
		in           Input
		action, to   string
		rule, reason string
	}{
		// 1. Gates: the check's outcome rules timing out, before any reading is read.
		{"no steps", with(func(in *Input) { nearWeek(in); in.Timing.Steps = nil }), Stay, "", RuleGate, NoSteps},
		{"held", with(func(in *Input) { nearWeek(in); in.Held = true }), Stay, "", RuleGate, Held},
		{"in error", with(func(in *Input) { nearWeek(in); in.InError = true }), Stay, "", RuleGate, InError},
		{"another move this check", with(func(in *Input) { nearWeek(in); in.OtherMove = true }), Stay, "", RuleGate, OtherMove},
		{"unknown state", with(func(in *Input) { nearWeek(in); in.RefusalOrUnknown = true }), Stay, "", RuleGate, RefusalOrUnknown},
		{"turn running", with(func(in *Input) { nearWeek(in); in.TurnEnded = false }), Stay, "", RuleGate, TurnNotEnded},
		{"on a fallback the failover rules chose", with(func(in *Input) { nearWeek(in); in.Current, in.MovedByFailover = "codex-main", true }), Stay, "", RuleGate, OnFallbackByFailover},
		{"failover onto a listed step, reset passed", with(func(in *Input) {
			nearWeek(in)
			in.Current, in.MovedByFailover = "example-large", true
			in.LastMove = &Move{At: h(-30), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(-1)}
		}), Stay, "", RuleGate, OnFallbackByFailover},
		{"a person moved it, reset not reached", with(func(in *Input) {
			nearWeek(in)
			at := h(2)
			in.Retarget = &Retarget{At: h(-1), ResetsAt: &at}
		}), Stay, "", RuleGate, Retargeted},
		{"a person moved it, no reset known", with(func(in *Input) { nearWeek(in); in.Retarget = &Retarget{At: h(-1)} }), Stay, "", RuleGate, Retargeted},
		{"a person moved it, that reset passed", with(func(in *Input) {
			nearWeek(in)
			at := h(-1)
			in.Retarget = &Retarget{At: h(-30), ResetsAt: &at}
		}), Up, "example-large", RuleStepUp, NearResetUnused},
		{"not on a step", with(func(in *Input) { nearWeek(in); in.Current = "codex-main" }), Stay, "", RuleGate, NotOnStep},

		// 2. Return at the reset, whatever the time on the step.
		{"stepped up, reset passed", with(func(in *Input) {
			nearWeek(in)
			in.Current = "example-large"
			in.LastMove = &Move{At: h(-1.0 / 60), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(0)}
		}), Back, First, RuleReturnAtReset, ResetPassed},
		{"stepped down, reset passed", with(func(in *Input) {
			in.Current = "example-small"
			in.LastMove = &Move{At: h(-50), From: First, To: "example-small", Action: Down, Window: "seven_day", ResetsAt: h(-0.5)}
		}), Back, First, RuleReturnAtReset, ResetPassed},

		// 3. Stepped up, and the readings since then run out before the reset.
		{"stepped up, now runs out", with(func(in *Input) {
			in.Current = "example-large"
			in.LastMove = &Move{At: h(-6.5), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(1)}
			in.Usage = reading(0.1, win{"seven_day", 0.97, 1})
			in.History = history("seven_day", 1, [2]float64{-6.5, 0.70}, [2]float64{-3, 0.85}, [2]float64{-0.1, 0.97})
		}), Back, First, RuleStepBackEarly, RunsOutSinceStepUp},
		{"stepped up, runs out, too soon", with(func(in *Input) {
			in.Timing.MinOnStep = dur(8)
			in.Current = "example-large"
			in.LastMove = &Move{At: h(-6.5), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(1)}
			in.Usage = reading(0.1, win{"seven_day", 0.97, 1})
			in.History = history("seven_day", 1, [2]float64{-6.5, 0.70}, [2]float64{-3, 0.85}, [2]float64{-0.1, 0.97})
		}), Stay, "", RuleStepBackEarly, TooSoon},
		{"readings before the step up do not count", with(func(in *Input) {
			in.Current = "example-large"
			in.LastMove = &Move{At: h(-1), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(1)}
			in.Usage = reading(0.1, win{"seven_day", 0.97, 1})
			in.History = history("seven_day", 1, [2]float64{-12, 0.40}, [2]float64{-6, 0.70}, [2]float64{-0.1, 0.97})
		}), Stay, "", RuleNothingDue, CannotProject},

		// 4. Step down: projected at or over down_at with more than near_reset left.
		{"projected to run out", with(risingWeek), Down, "example-small", RuleStepDown, ProjectedOverDownAt},
		{"projected exactly at down_at", with(func(in *Input) {
			in.Usage = reading(0, win{"seven_day", 0.47, 48})
			in.History = history("seven_day", 48, [2]float64{-12, 0.35}, [2]float64{-6, 0.41}, [2]float64{0, 0.47})
		}), Down, "example-small", RuleStepDown, ProjectedOverDownAt},
		{"projected just under down_at", with(func(in *Input) {
			in.Usage = reading(0, win{"seven_day", 0.47, 47.5})
			in.History = history("seven_day", 47.5, [2]float64{-12, 0.35}, [2]float64{-6, 0.41}, [2]float64{0, 0.47})
		}), Stay, "", RuleNothingDue, ProjectedUnder},
		{"projected over, but the reset is within near_reset", with(func(in *Input) {
			in.Usage = reading(1.5, win{"seven_day", 0.80, 6})
			in.History = history("seven_day", 6, [2]float64{-13.5, 0.56}, [2]float64{-7.5, 0.68}, [2]float64{-1.5, 0.80})
		}), Stay, "", RuleNothingDue, ReadingTooOld},
		{"projected over, at the cheapest step", with(func(in *Input) { risingWeek(in); in.Current = "example-small" }), Stay, "", RuleStepDown, AtFloor},
		{"projected over, too soon", with(func(in *Input) {
			risingWeek(in)
			in.Timing.MinOnStep = dur(48)
			in.Current = "example-small"
			in.Timing.Steps = []string{"example-tiny", "example-small", "primary", "example-large"}
			in.LastMove = &Move{At: h(-24.5), From: First, To: "example-small", Action: Down, Window: "seven_day", ResetsAt: h(72)}
		}), Stay, "", RuleStepDown, TooSoon},
		{"a stale newest reading does not step down", with(func(in *Input) {
			risingWeek(in)
			in.Usage.At = h(-7)
			in.History = history("seven_day", 72, [2]float64{-31, 0.16}, [2]float64{-19, 0.28}, [2]float64{-7, 0.40})
		}), Stay, "", RuleNothingDue, CannotProject},

		// 5. Step up: near a reset, enough unused, a fresh reading.
		{"near the reset with 40% unused", with(nearWeek), Up, "example-large", RuleStepUp, NearResetUnused},
		{"reset exactly near_reset away", with(func(in *Input) { in.Usage = reading(0.1, win{"seven_day", 0.60, 6}) }), Up, "example-large", RuleStepUp, NearResetUnused},
		{"reset just beyond near_reset", with(func(in *Input) { in.Usage = reading(0.1, win{"seven_day", 0.60, 6.02}) }), Stay, "", RuleNothingDue, CannotProject},
		{"reading exactly 1 h old", with(func(in *Input) { in.Usage = reading(1, win{"seven_day", 0.60, 2}) }), Up, "example-large", RuleStepUp, NearResetUnused},
		{"reading just over 1 h old", with(func(in *Input) { in.Usage = reading(1.02, win{"seven_day", 0.60, 2}) }), Stay, "", RuleNothingDue, ReadingTooOld},
		{"exactly 25% unused", with(func(in *Input) { in.Usage = reading(0.1, win{"seven_day", 0.75, 2}) }), Up, "example-large", RuleStepUp, NearResetUnused},
		{"just under 25% unused", with(func(in *Input) { in.Usage = reading(0.1, win{"seven_day", 0.76, 2}) }), Stay, "", RuleNothingDue, CannotProject},
		{"already at the ceiling", with(func(in *Input) { nearWeek(in); in.Current = "example-large" }), Stay, "", RuleStepUp, AtCeiling},
		{"from a step down, straight to the ceiling", with(func(in *Input) {
			nearWeek(in)
			in.Current = "example-small"
			in.LastMove = &Move{At: h(-30), From: First, To: "example-small", Action: Down, Window: "seven_day", ResetsAt: h(2)}
		}), Up, "example-large", RuleStepUp, NearResetUnused},
		{"already stepped up for this instance", with(func(in *Input) {
			nearWeek(in)
			in.LastMove = &Move{At: h(-3), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(2)}
		}), Stay, "", RuleStepUp, AlreadySteppedUp},
		{"another named window near its limit", with(func(in *Input) {
			in.Usage = reading(0.1, win{"seven_day", 0.60, 2}, win{"five_hour", 0.96, 4})
		}), Stay, "", RuleStepUp, OtherWindowHigh},
		{"a window not named does not block", with(func(in *Input) {
			in.Timing.Windows = []string{"seven_day"}
			in.Usage = reading(0.1, win{"seven_day", 0.60, 2}, win{"five_hour", 0.99, 4})
		}), Up, "example-large", RuleStepUp, NearResetUnused},
		{"too soon after a step down", with(func(in *Input) {
			nearWeek(in)
			in.Current = "example-small"
			in.LastMove = &Move{At: h(-1), From: First, To: "example-small", Action: Down, Window: "seven_day", ResetsAt: h(2)}
		}), Stay, "", RuleStepUp, TooSoon},

		// A handoff from a step, then switch-back to primary: no second step
		// up for the same window instance, and one for the next instance.
		{"on the fallback a handoff from a step chose", with(func(in *Input) {
			nearWeek(in)
			in.Current, in.MovedByFailover = "claude-other", true
			in.LastMove = &Move{At: h(-1), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(2)}
		}), Stay, "", RuleGate, OnFallbackByFailover},
		{"switched back to primary, same instance", with(func(in *Input) {
			nearWeek(in)
			in.LastMove = &Move{At: h(-1), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(2)}
		}), Stay, "", RuleStepUp, AlreadySteppedUp},
		{"switched back to primary, the next instance", with(func(in *Input) {
			nearWeek(in)
			in.LastMove = &Move{At: h(-170), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(-166)}
		}), Up, "example-large", RuleStepUp, NearResetUnused},

		{"up, back early, near the same reset again: no second step up", with(func(in *Input) {
			nearWeek(in)
			in.LastStepUp = &Move{At: h(-5), From: First, To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(2)}
			in.LastMove = &Move{At: h(-1), From: "example-large", To: First, Action: Back, Window: "seven_day", ResetsAt: h(2)}
		}), Stay, "", RuleStepUp, AlreadySteppedUp},
		{"timing gave up on this step up", with(func(in *Input) {
			nearWeek(in)
			in.GaveUp = &Move{To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(2)}
		}), Stay, "", RuleStepUp, GaveUp},
		{"gave up for an earlier instance", with(func(in *Input) {
			nearWeek(in)
			in.GaveUp = &Move{To: "example-large", Action: Up, Window: "seven_day", ResetsAt: h(-166)}
		}), Up, "example-large", RuleStepUp, NearResetUnused},

		// 6. Nothing applies.
		{"no reading", base(), Stay, "", RuleNothingDue, NoReading},
		{"no named window in the reading", with(func(in *Input) { in.Usage = reading(0.1, win{"example_window", 0.5, 30}) }), Stay, "", RuleNothingDue, NoNamedWindow},
		{"projected under", with(func(in *Input) {
			in.Usage = reading(0, win{"seven_day", 0.40, 72})
			in.History = history("seven_day", 72, [2]float64{-24, 0.36}, [2]float64{-12, 0.38}, [2]float64{0, 0.40})
		}), Stay, "", RuleNothingDue, ProjectedUnder},
		{"too early to project", with(func(in *Input) {
			in.Usage = reading(0, win{"seven_day", 0.40, 72})
			in.History = history("seven_day", 72, [2]float64{-12, 0.28}, [2]float64{-6, 0.34}, [2]float64{0, 0.40})
		}), Stay, "", RuleNothingDue, CannotProject},
		{"one reading cannot be projected", with(func(in *Input) { in.Usage = reading(0.1, win{"seven_day", 0.40, 72}) }), Stay, "", RuleNothingDue, CannotProject},
	} {
		d := Decide(c.in)
		if d.Action != c.action || d.To != c.to || d.Rule != c.rule || d.Reason != c.reason {
			t.Errorf("%s: got %s %q by %s/%s, want %s %q by %s/%s", c.name, d.Action, d.To, d.Rule, d.Reason, c.action, c.to, c.rule, c.reason)
		}
		if d.Action == Stay && d.To != "" || d.Action != Stay && d.To == "" {
			t.Errorf("%s: action %s with target %q", c.name, d.Action, d.To)
		}
		// No row may move a loop the failover rules put where it is.
		in := c.in
		in.MovedByFailover = true
		if f := Decide(in); f.Action != Stay || f.Rule != RuleGate {
			t.Errorf("%s, on a route the failover rules chose: %s %q by %s/%s", c.name, f.Action, f.To, f.Rule, f.Reason)
		}
	}
}

// The sentences status shows, word for word.
func TestDecisionText(t *testing.T) {
	span := func(d time.Duration) string { return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes())) }
	for _, c := range []struct {
		in   Input
		want string
	}{
		{with(nearWeek), "step up to example-large: the seven_day allowance resets in 120m with 40% unused (reading 20m old)"},
		{with(risingWeek), "step down to example-small: at the recent rate seven_day runs out about 720m before it resets (3 readings over 1440m)"},
		{with(func(in *Input) { nearWeek(in); in.Current, in.MovedByFailover = "codex-main", true }), "stay: the loop is on codex-main because the failover rules moved it there; timing does not move it off"},
		{with(func(in *Input) { nearWeek(in); in.Timing.Steps = nil }), "stay: no steps are configured, so timing only collects readings"},
		{with(func(in *Input) { in.Usage = reading(1.02, win{"seven_day", 0.60, 2}) }), "stay: the seven_day allowance resets in 120m, but the newest reading is 61m old, older than 60m"},
		{base(), "stay: no usage reading of the first route's account is recorded"},
		{with(func(in *Input) {
			in.Usage = reading(0, win{"seven_day", 0.40, 72})
			in.History = history("seven_day", 72, [2]float64{-12, 0.28}, [2]float64{-6, 0.34}, [2]float64{0, 0.40})
		}), "stay: seven_day cannot be projected: too early in the window"},
	} {
		if got := Decide(c.in).Text(span); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}
