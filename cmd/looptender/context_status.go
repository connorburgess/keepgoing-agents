package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/state"
)

// contextStatus is the active target's context-size readings as status
// shows them: the latest and a bounded history (one summary per UTC day and
// the last few readings, oldest first). It is diagnostic only: no decision,
// send, allowance or hold reads it. Availability is recorded or
// unavailable; Reason says why a reading is unavailable: not_supported (the
// target's adapter reports none) or not_recorded (no check has recorded one
// for this target yet, or bb could not give one). Version is the layout of
// this object, state.ContextVersion.
type contextStatus struct {
	Version      int                    `json:"version"`
	Availability string                 `json:"availability"`
	Reason       string                 `json:"reason,omitempty"`
	Target       string                 `json:"target,omitempty"`
	UsedTokens   *int64                 `json:"usedTokens,omitempty"`
	WindowTokens *int64                 `json:"windowTokens,omitempty"`
	Estimated    bool                   `json:"estimated,omitempty"`
	ReadAt       *time.Time             `json:"readAt,omitempty"`
	AgeSeconds   *int64                 `json:"ageSeconds,omitempty"`
	Age          string                 `json:"age,omitempty"` // AgeSeconds in words: "12 min ago"
	Text         string                 `json:"text"`
	Days         []state.ContextDay     `json:"days"`
	Recent       []state.ContextReading `json:"recent"`
}

// contextDaysShown is how many days the text output lists, newest first;
// status --json has them all.
const contextDaysShown = 3

// describeContext reads the record for the active target. A record of
// another target is not shown as this one's. isBB says the active target is
// a BB thread, the only adapter that can be read.
func describeContext(r *state.ContextRecord, target string, isBB bool, now time.Time) contextStatus {
	out := contextStatus{Version: state.ContextVersion, Availability: "unavailable", Days: []state.ContextDay{}, Recent: []state.ContextReading{}}
	switch {
	case !isBB:
		out.Reason, out.Text = "not_supported", "unavailable; this target's agent reports no context size"
		return out
	case r == nil || r.Target != target || r.Latest.At.IsZero() || r.Latest.WindowTokens <= 0:
		out.Reason, out.Text = "not_recorded", "unavailable; BB has not given a reading for this thread at a check yet"
		return out
	}
	l := r.Latest
	at, used, window := l.At, l.UsedTokens, l.WindowTokens
	out.Availability, out.Target, out.UsedTokens, out.WindowTokens, out.Estimated = "recorded", r.Target, &used, &window, l.Estimated
	out.ReadAt, out.AgeSeconds, out.Age, out.Text = &at, secondsSince(now, at), dockAgo(now, at), contextFigures(used, window, l.Estimated)
	out.Days = append(out.Days, r.Days...)
	out.Recent = append(out.Recent, r.Recent...)
	return out
}

// contextFigures words a reading: "48,000 of 200,000 tokens (24%), estimated
// by BB". BB's own word is kept: a figure it marks as an estimate is never
// shown as a measurement.
func contextFigures(used, window int64, estimated bool) string {
	return fmt.Sprintf("%s of %s tokens (%d%%), %s", groupDigits(used), groupDigits(window), used*100/window, contextSource(estimated))
}

func contextSource(estimated bool) string {
	if estimated {
		return "estimated by BB"
	}
	return "reported by BB"
}

// groupDigits writes 48000 as "48,000".
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := ""
	if n < 0 {
		neg, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return neg + s
}

func printContext(w io.Writer, c contextStatus) {
	if c.Availability != "recorded" {
		fmt.Fprintf(w, "context:   %s\n", c.Text)
		return
	}
	fmt.Fprintf(w, "context:   %s; read %s, at the check of %s; diagnostic only, no decision uses it\n", c.Text, c.Age, when(*c.ReadAt))
	if len(c.Days) == 0 {
		return
	}
	estimated := false
	var days []string
	for i := len(c.Days) - 1; i >= 0; i-- {
		d := c.Days[i]
		estimated = estimated || d.Estimated
		if len(days) < contextDaysShown {
			days = append(days, fmt.Sprintf("%s lowest %s, highest %s, last %s (%s)", d.Day, groupDigits(d.Lowest), groupDigits(d.Highest), groupDigits(d.Last), dockPlural(d.Count, "reading", "readings")))
		}
	}
	more := ""
	if n := len(c.Days) - len(days); n > 0 {
		more = fmt.Sprintf("; %s more in status --json", dockPlural(n, "day", "days"))
	}
	fmt.Fprintf(w, "context by day (UTC, tokens, %s): %s%s\n", contextSource(estimated), strings.Join(days, "; "), more)
}
