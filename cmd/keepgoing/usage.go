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
	// ProbesOnly is set for a BB route: BB does not pass the provider's
	// rate-limit events through, so its readings come only from its probes.
	ProbesOnly bool `json:"readingsFromProbesOnly,omitempty"`
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
// UsedAtReset may exceed 1. RunsOutAt is set when the window is projected to
// be fully used before it resets.
type projectionView struct {
	State       string     `json:"state"`
	Reason      string     `json:"reason,omitempty"`
	UsedAtReset *float64   `json:"usedAtReset,omitempty"`
	RunsOutAt   *time.Time `json:"runsOutAt,omitempty"`
	Readings    int        `json:"readings"`
	SpanSeconds int64      `json:"spanSeconds"`
	// MinSpanSeconds and MaxAgeSeconds are the rules the projection used.
	MinSpanSeconds int64 `json:"minSpanSeconds"`
	MaxAgeSeconds  int64 `json:"maxAgeSeconds"`
}

// projectionRules are the display rules: readings at least six hours apart,
// the newest no older than the probe TTL.
func projectionRules(fb *config.Fallbacks) allowance.Rules {
	r := allowance.Rules{MinSpan: allowance.DefaultMinSpan}
	if fb != nil {
		r.MaxAge = fb.Probe.TTL.Duration
	}
	return r
}

// usageOf reads the candidate's recorded reading. A reading taken for another
// account identity (the candidate was repointed) is not shown.
func usageOf(reg *health.Registry, c config.Candidate, now time.Time, rules allowance.Rules) usageView {
	v := usageView{ID: c.ID, State: "unavailable", Windows: []usageWindowView{}, ProbesOnly: c.Adapter == "bb"}
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
		v.Windows = append(v.Windows, usageWindowView{UsageWindow: w, Stale: stale, Projection: projectionOf(history, w, now, rules)})
	}
	return v
}

// projectionOf projects the window from the recorded readings of the same
// window instance: the series with its key whose reset time matches.
func projectionOf(h *allowance.History, w probe.UsageWindow, now time.Time, rules allowance.Rules) projectionView {
	pv := projectionView{State: "unavailable", MinSpanSeconds: int64(rules.MinSpan / time.Second), MaxAgeSeconds: int64(rules.MaxAge / time.Second)}
	if w.ResetsAt == nil {
		pv.Reason = allowance.NoResetTime
		return pv
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
	p := allowance.Project(s, now, rules)
	pv.Reason, pv.Readings, pv.SpanSeconds = p.Reason, p.Readings, int64(p.Span/time.Second)
	if p.Projected {
		pv.State = "projected"
		used := p.UsedAtReset
		pv.UsedAtReset = &used
		pv.RunsOutAt = p.RunsOutAt
	}
	return pv
}

// projectionReasons says why there is no projection, in words.
var projectionReasons = map[string]string{
	allowance.NoHistory:    "no earlier reading of this window is recorded",
	allowance.NoResetTime:  "no reset time was reported",
	allowance.WindowReset:  "the window has reset",
	allowance.OneReading:   "one reading in this window",
	allowance.ClockBehind:  "the newest reading is after this clock's time (check the host clock)",
	allowance.NewestTooOld: "the newest reading is older than %s",
	allowance.SpanTooShort: "the readings cover less than %s",
	allowance.UsedWentDown: "a reading is lower than an earlier one",
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

func (v usageView) projectionText() string {
	parts := make([]string, 0, len(v.Windows))
	for _, w := range v.Windows {
		p := w.Projection
		over := fmt.Sprintf("%d readings over %s", p.Readings, usageSpan(time.Duration(p.SpanSeconds)*time.Second))
		switch {
		case p.State != "projected":
			reason := projectionReasons[p.Reason]
			switch p.Reason {
			case allowance.NewestTooOld:
				reason = fmt.Sprintf(reason, usageSpan(time.Duration(p.MaxAgeSeconds)*time.Second))
			case allowance.SpanTooShort:
				reason = fmt.Sprintf(reason, usageSpan(time.Duration(p.MinSpanSeconds)*time.Second))
			}
			if reason == "" {
				reason = p.Reason
			}
			if v.ProbesOnly {
				reason += "; readings for a BB thread come only from its probes"
			}
			parts = append(parts, w.Key+" can't project: "+reason)
		case p.RunsOutAt != nil && w.ResetsAt != nil:
			parts = append(parts, fmt.Sprintf("%s runs out about %s before it resets, at %s (%s)", w.Key, usageSpan(w.ResetsAt.Sub(*p.RunsOutAt)), p.RunsOutAt.UTC().Format("2006-01-02 15:04Z"), over))
		default:
			parts = append(parts, fmt.Sprintf("%s %d%% at reset (%s)", w.Key, usagePercent(min(1, *p.UsedAtReset)), over))
		}
	}
	return strings.Join(parts, "; ")
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
