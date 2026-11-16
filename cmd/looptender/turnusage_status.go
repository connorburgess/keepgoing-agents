package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/adapter/runner"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/state"
)

// turnUsageStatus is the token counts the active target's agent reported for
// its finished turns, as status shows them: the latest turn and one sum per
// UTC day, oldest first. They are the agent's own counts per turn, not a
// context window size, not an account's usage window and not money. It is
// diagnostic only: no decision, send, allowance or hold reads it.
// Availability is recorded or unavailable; Reason says why a reading is
// unavailable: not_supported (the target's agent reports no per-turn usage
// that looptender reads) or not_recorded (no check has recorded a turn of
// this target yet). Version is the layout of this object,
// state.TurnUsageVersion.
type turnUsageStatus struct {
	Version      int                  `json:"version"`
	Availability string               `json:"availability"`
	Reason       string               `json:"reason,omitempty"`
	Target       string               `json:"target,omitempty"`
	Latest       *turnUsageLatest     `json:"latest,omitempty"`
	Text         string               `json:"text"`
	Days         []state.TurnUsageDay `json:"days"`
}

// turnUsageLatest is the latest recorded turn. A count the agent did not
// report is left out.
type turnUsageLatest struct {
	Input           *int64    `json:"inputTokens,omitempty"`
	CachedInput     *int64    `json:"cachedInputTokens,omitempty"`
	CacheWriteInput *int64    `json:"cacheWriteInputTokens,omitempty"`
	Output          *int64    `json:"outputTokens,omitempty"`
	ReasoningOutput *int64    `json:"reasoningOutputTokens,omitempty"`
	RecordedAt      time.Time `json:"recordedAt"`
	AgeSeconds      *int64    `json:"ageSeconds,omitempty"`
	Age             string    `json:"age,omitempty"`
}

// turnUsageDaysShown is how many days the text output lists, newest first;
// status --json has them all.
const turnUsageDaysShown = 3

// targetReportsTurnUsage says whether the active target's agent is one whose
// per-turn usage looptender reads.
func targetReportsTurnUsage(cfg *config.Config, st state.File) bool {
	a := cfg.Loop.Agent
	if t := st.Target(); t != state.Primary {
		c, ok := currentRoutes(cfg).Find(t)
		if !ok {
			return false
		}
		a = c.Agent
	}
	spec, ok := runner.SpecFor(a.Adapter)
	return ok && spec.Capabilities.TurnUsage
}

// describeTurnUsage reads the record for the active target. A record of
// another target is not shown as this one's.
func describeTurnUsage(r *state.TurnUsageRecord, target string, reports bool, now time.Time) turnUsageStatus {
	out := turnUsageStatus{Version: state.TurnUsageVersion, Availability: "unavailable", Days: []state.TurnUsageDay{}}
	switch {
	case !reports:
		out.Reason, out.Text = "not_supported", "unavailable; this target's agent reports no per-turn token counts that looptender reads"
		return out
	case r == nil || r.Target != target || r.Latest.Turn == "" || r.Latest.At.IsZero():
		out.Reason, out.Text = "not_recorded", "unavailable; no finished turn of this target has been recorded yet"
		return out
	}
	l := r.Latest
	out.Availability, out.Target = "recorded", r.Target
	out.Latest = &turnUsageLatest{Input: l.Input, CachedInput: l.CachedInput, CacheWriteInput: l.CacheWriteInput, Output: l.Output, ReasoningOutput: l.ReasoningOutput,
		RecordedAt: l.At, AgeSeconds: secondsSince(now, l.At), Age: dockAgo(now, l.At)}
	out.Text = turnUsageFigures(l)
	out.Days = append(out.Days, r.Days...)
	return out
}

// turnUsageFigures words the counts a turn reported, in a fixed order:
// "input 21,000, cached input 13,000, output 5 tokens, as the agent reported".
func turnUsageFigures(l state.TurnUsageReading) string {
	var parts []string
	for _, f := range []struct {
		name string
		n    *int64
	}{{"input", l.Input}, {"cached input", l.CachedInput}, {"cache write input", l.CacheWriteInput}, {"output", l.Output}, {"reasoning output", l.ReasoningOutput}} {
		if f.n != nil {
			parts = append(parts, f.name+" "+groupDigits(*f.n))
		}
	}
	return strings.Join(parts, ", ") + " tokens, as the agent reported"
}

// turnUsageDaySums words one day's sums, saying when a count covers fewer
// turns than the day had.
func turnUsageDaySums(d state.TurnUsageDay) string {
	var parts []string
	for _, f := range []struct {
		name string
		s    state.TurnUsageSum
	}{{"input", d.Input}, {"cached input", d.CachedInput}, {"output", d.Output}} {
		if f.s.Turns == 0 {
			continue
		}
		part := f.name + " " + groupDigits(f.s.Tokens)
		if f.s.Turns < d.Turns {
			part += fmt.Sprintf(" (from %d)", f.s.Turns)
		}
		parts = append(parts, part)
	}
	head := d.Day + " " + dockPlural(d.Turns, "turn", "turns")
	if len(parts) == 0 {
		return head
	}
	return head + ": " + strings.Join(parts, ", ")
}

func printTurnUsage(w io.Writer, u turnUsageStatus) {
	if u.Availability != "recorded" {
		fmt.Fprintf(w, "tokens per turn: %s\n", u.Text)
		return
	}
	fmt.Fprintf(w, "tokens per turn: latest turn %s; recorded %s, at the check of %s; diagnostic only, no decision uses it\n", u.Text, u.Latest.Age, when(u.Latest.RecordedAt))
	if len(u.Days) == 0 {
		return
	}
	var days []string
	for i := len(u.Days) - 1; i >= 0 && len(days) < turnUsageDaysShown; i-- {
		days = append(days, turnUsageDaySums(u.Days[i]))
	}
	more := ""
	if n := len(u.Days) - len(days); n > 0 {
		more = fmt.Sprintf("; %s more in status --json", dockPlural(n, "day", "days"))
	}
	fmt.Fprintf(w, "tokens per turn by day (UTC, tokens summed over the turns that reported them): %s%s\n", strings.Join(days, "; "), more)
}
