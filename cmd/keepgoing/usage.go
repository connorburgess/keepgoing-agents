package main

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"

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
}

// usageWindowView adds Stale: the window's reset time is at or before now, so
// the recorded fraction describes a window that has since reset.
type usageWindowView struct {
	probe.UsageWindow
	Stale bool `json:"stale"`
}

// usageOf reads the candidate's recorded reading. A reading taken for another
// account identity (the candidate was repointed) is not shown.
func usageOf(reg *health.Registry, c config.Candidate, now time.Time) usageView {
	v := usageView{ID: c.ID, State: "unavailable", Windows: []usageWindowView{}}
	if reg == nil {
		return v
	}
	e, ok := reg.Candidates[c.ID]
	if !ok || !e.Usage.For(probe.UsageIdentity(c)) {
		return v
	}
	at, age := e.Usage.At, int64(now.Sub(e.Usage.At)/time.Second)
	v.State, v.ObservedAt, v.AgeSeconds, v.Source = "stale", &at, &age, e.Usage.Source
	for _, w := range e.Usage.Windows {
		stale := w.ResetsAt != nil && !w.ResetsAt.After(now)
		if !stale {
			v.State = "available"
		}
		v.Windows = append(v.Windows, usageWindowView{UsageWindow: w, Stale: stale})
	}
	return v
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

func printUsage(w io.Writer, label string, views []usageView) {
	for _, v := range views {
		fmt.Fprintf(w, "%s%s: %s\n", label, v.ID, v.text())
	}
}
