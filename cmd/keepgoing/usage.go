package main

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/allowance"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

// usageView is a candidate's latest recorded usage reading for display. It is
// historical: a reading is only as fresh as the probe that carried it.
//
// State is "available" when at least one window is still current, "stale"
// when every window's reset time has passed, and "unavailable" when no
// reading is recorded for the candidate's current account. Unavailable and
// stale say nothing about how much quota is used.
type usageView struct {
	ID         string            `json:"id,omitempty"`
	State      string            `json:"state"`
	ObservedAt *time.Time        `json:"observedAt,omitempty"`
	AgeSeconds *int64            `json:"ageSeconds,omitempty"`
	Source     string            `json:"source,omitempty"`
	Windows    []usageWindowView `json:"windows"`
	// ProbesOnly says the route's readings come only from its probes. Today
	// that holds for every route: only probes record readings. BB does not
	// pass the provider's rate-limit events through, and headless turns do
	// not record them yet (roadmap Q3); when they do, this narrows per adapter.
	ProbesOnly bool `json:"readingsFromProbesOnly"`
}

// usageWindowView adds Stale: the window's reset time is at or before now, so
// the recorded fraction describes a window that has since reset. Projection is
// what the recorded readings of this window instance say about its reset.
type usageWindowView struct {
	probe.UsageWindow
	Stale      bool           `json:"stale"`
	Projection projectionView `json:"projection"`
}

// projectionView is allowance.Project for display. State is "projected" or
// "unavailable"; Reason is one of the allowance reason codes when unavailable.
// UsedAtReset may exceed 1. UsedUpSince is set when the window was already
// used up. RunsOutAt is set when the fitted rate reaches 100% before the reset
// for every rate within the readings' rounding; it may be before now.
// WithinRounding is set when the fit reaches 100% but that rounding allows less.
type projectionView struct {
	State          string     `json:"state"`
	Reason         string     `json:"reason,omitempty"`
	UsedAtReset    *float64   `json:"usedAtReset,omitempty"`
	UsedUpSince    *time.Time `json:"usedUpSince,omitempty"`
	RunsOutAt      *time.Time `json:"runsOutAt,omitempty"`
	WithinRounding bool       `json:"withinRounding,omitempty"`
	Readings       int        `json:"readings"`
	// Ignored counts readings stamped after this clock's time.
	Ignored         int   `json:"ignoredLaterReadings,omitempty"`
	SpanSeconds     int64 `json:"spanSeconds"`
	ResetsInSeconds int64 `json:"resetsInSeconds,omitempty"`
	// The rules the projection used.
	MinReadings    int     `json:"minReadings"`
	MinSpanSeconds int64   `json:"minSpanSeconds"`
	MaxAgeSeconds  int64   `json:"maxAgeSeconds"`
	MaxAhead       float64 `json:"maxAheadSpans"`
	Rounding       float64 `json:"rounding"`
	// now is when the projection was made, for wording a run-out time that
	// has already passed.
	now time.Time
}

// projectionRules are the display rules, with the newest reading no older
// than the probe TTL.
func projectionRules(fb *config.Fallbacks) allowance.Rules {
	var ttl time.Duration
	if fb != nil {
		ttl = fb.Probe.TTL.Duration
	}
	return allowance.DisplayRules(ttl)
}

// usageOf reads the candidate's recorded reading. A reading taken for another
// account identity (the candidate was repointed) is not shown.
func usageOf(reg *health.Registry, c config.Candidate, now time.Time, rules allowance.Rules) usageView {
	v := usageView{ID: c.ID, State: "unavailable", Windows: []usageWindowView{}, ProbesOnly: true}
	if reg == nil {
		return v
	}
	identity := probe.UsageIdentity(c)
	e, ok := reg.Candidates[c.ID]
	if !ok || !e.Usage.For(identity) {
		return v
	}
	var history *allowance.History
	if e.UsageHistory.For(identity) {
		history = e.UsageHistory
	}
	at, age := e.Usage.At, int64(now.Sub(e.Usage.At)/time.Second)
	v.State, v.ObservedAt, v.AgeSeconds, v.Source = "stale", &at, &age, e.Usage.Source
	for _, w := range e.Usage.Windows {
		stale := w.ResetsAt != nil && !w.ResetsAt.After(now)
		if !stale {
			v.State = "available"
		}
		v.Windows = append(v.Windows, usageWindowView{UsageWindow: w, Stale: stale, Projection: projectionOf(history, w, at, now, rules)})
	}
	return v
}

// projectionOf projects the window from the recorded readings of the same
// window instance: the series with its key whose reset time matches.
func projectionOf(h *allowance.History, w probe.UsageWindow, readAt, now time.Time, rules allowance.Rules) projectionView {
	pv := projectionView{State: "unavailable", MinReadings: rules.MinReadings, MinSpanSeconds: int64(rules.MinSpan / time.Second),
		MaxAgeSeconds: int64(rules.MaxAge / time.Second), MaxAhead: rules.MaxAhead, Rounding: rules.Rounding, now: now}
	if w.ResetsAt == nil {
		pv.Reason = allowance.NoResetTime
		return pv
	}
	if d := w.ResetsAt.Sub(now); d > 0 {
		pv.ResetsInSeconds = int64(d / time.Second)
	}
	s := h.Find(w.Key)
	if s != nil {
		if d := s.ResetsAt.Sub(*w.ResetsAt); d > allowance.SameReset || d < -allowance.SameReset {
			s = nil
		}
	}
	if s == nil && !w.ResetsAt.After(now) {
		pv.Reason = allowance.WindowReset
		return pv
	}
	// With no history of the instance, the latest reading alone can still
	// say the window is used up; it needs no rate.
	if s == nil && w.Used >= 1 {
		used, since := 1.0, readAt.UTC()
		pv.State, pv.UsedAtReset, pv.UsedUpSince, pv.Readings = "projected", &used, &since, 1
		return pv
	}
	p := allowance.Project(s, now, rules)
	pv.Reason, pv.Readings, pv.Ignored, pv.SpanSeconds = p.Reason, p.Readings, p.Ignored, int64(p.Span/time.Second)
	if p.Projected {
		pv.State = "projected"
		used := p.UsedAtReset
		pv.UsedAtReset, pv.UsedUpSince, pv.RunsOutAt, pv.WithinRounding = &used, p.UsedUpSince, p.RunsOutAt, p.WithinRounding
	}
	return pv
}

// projectionReasons says why there is no projection, in words.
var projectionReasons = map[string]string{
	allowance.NoHistory:      "no earlier reading of this window is recorded",
	allowance.NoResetTime:    "no reset time was reported",
	allowance.WindowReset:    "the window has reset",
	allowance.TooFewReadings: "%d of the %d readings needed in this window",
	allowance.NewestTooOld:   "the newest reading is older than %s",
	allowance.SpanTooShort:   "the readings cover less than %s",
	allowance.TooEarly:       "too early to project: %s of readings for a window that resets in %s",
	allowance.UsedWentDown:   "a reading is lower than an earlier one",
}

// usageSpan prints a duration in the largest two useful units.
func usageSpan(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
}

// words is one window's projection as parts, without the window's name:
// strings, and each time as stamp gives it. Status and the panel both word a
// projection with it, so they say the same.
func (p projectionView) words(resetsAt *time.Time, stamp func(time.Time) any) []any {
	span := func(n int64) string { return usageSpan(time.Duration(n) * time.Second) }
	over := fmt.Sprintf("%d readings over %s", p.Readings, span(p.SpanSeconds))
	if p.Ignored > 0 {
		over += fmt.Sprintf(", %d stamped after this clock's time ignored", p.Ignored)
	}
	switch {
	case p.State != "projected":
		reason := projectionReasons[p.Reason]
		switch p.Reason {
		case "":
			reason = "unknown"
		case allowance.TooFewReadings:
			reason = fmt.Sprintf(reason, p.Readings, p.MinReadings)
		case allowance.NewestTooOld:
			reason = fmt.Sprintf(reason, span(p.MaxAgeSeconds))
		case allowance.SpanTooShort:
			reason = fmt.Sprintf(reason, span(p.MinSpanSeconds))
		case allowance.TooEarly:
			reason = fmt.Sprintf(reason, span(p.SpanSeconds), span(p.ResetsInSeconds))
		}
		if reason == "" {
			reason = p.Reason
		}
		return []any{"can't project: " + reason}
	case p.UsedUpSince != nil:
		return []any{"used up since ", stamp(*p.UsedUpSince)}
	case p.RunsOutAt != nil && resetsAt != nil && !p.RunsOutAt.After(p.now):
		return []any{"projected to have run out at ", stamp(*p.RunsOutAt), ", " + usageSpan(resetsAt.Sub(*p.RunsOutAt)) + " before it resets (" + over + ")"}
	case p.RunsOutAt != nil && resetsAt != nil:
		return []any{"runs out about " + usageSpan(resetsAt.Sub(*p.RunsOutAt)) + " before it resets, at ", stamp(*p.RunsOutAt), " (" + over + ")"}
	case p.WithinRounding:
		return []any{"may run out before it resets, within the readings' rounding (" + over + ")"}
	case p.UsedAtReset == nil:
		return []any{"can't project: unknown"}
	}
	return []any{fmt.Sprintf("%d%% at reset (%s)", usagePercent(min(1, *p.UsedAtReset)), over)}
}

// text words one window's projection for status, with times in UTC.
func (p projectionView) text(key string, resetsAt *time.Time) string {
	var b strings.Builder
	b.WriteString(key + " ")
	for _, part := range p.words(resetsAt, func(t time.Time) any { return t.UTC().Format("2006-01-02 15:04Z") }) {
		b.WriteString(part.(string))
	}
	return b.String()
}

// readingsNote is said once for a route when one of its windows could not be
// projected for want of readings: a window that has reset needs none.
const readingsNote = "Readings for this route come only from its probes."

// needsReadings reports whether the projection is missing for want of readings.
func (p projectionView) needsReadings() bool {
	return p.State != "projected" && p.Reason != allowance.WindowReset
}

// projectionText is one route's projected line. When a window cannot be
// projected for want of readings, it ends by saying once where the route's
// readings come from.
func (v usageView) projectionText() string {
	parts := make([]string, 0, len(v.Windows))
	note := false
	for _, w := range v.Windows {
		parts = append(parts, w.Projection.text(w.Key, w.ResetsAt))
		note = note || w.Projection.needsReadings()
	}
	out := strings.Join(parts, "; ")
	if note && v.ProbesOnly {
		out += ". " + readingsNote
	}
	return out
}

// usagePercent rounds down, so only a full window prints as 100%.
func usagePercent(used float64) int {
	return int(math.Floor(used*100 + 1e-9))
}

// usageAge prints an age in the largest two useful units.
func usageAge(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case seconds < 0:
		return "at a time after this clock's (check the host clock)"
	case d < time.Minute:
		return "under a minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm ago", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dd %dh ago", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
}

func (v usageView) text() string {
	if v.State == "unavailable" {
		return "unavailable"
	}
	parts := make([]string, 0, len(v.Windows))
	for _, w := range v.Windows {
		if w.Stale {
			parts = append(parts, fmt.Sprintf("%s stale (window reset %s)", w.Key, w.ResetsAt.UTC().Format("2006-01-02 15:04Z")))
			continue
		}
		s := fmt.Sprintf("%s %d%%", w.Key, usagePercent(w.Used))
		if w.ResetsAt != nil {
			s += " resets " + w.ResetsAt.UTC().Format("2006-01-02 15:04Z")
		}
		if w.Status != "" {
			s += " " + w.Status
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ") + "; observed " + usageAge(*v.AgeSeconds) + " (" + v.Source + ")"
}

// printUsage prints each reading and, for a reading with windows, what the
// recorded readings project for each window at its reset.
func printUsage(w io.Writer, label, projectedLabel string, views []usageView) {
	for _, v := range views {
		fmt.Fprintf(w, "%s%s: %s\n", label, v.ID, v.text())
		if len(v.Windows) > 0 {
			fmt.Fprintf(w, "%s%s: %s\n", projectedLabel, v.ID, v.projectionText())
		}
	}
}
