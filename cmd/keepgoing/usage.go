package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

// usageView is a candidate's latest recorded usage reading for display. It is
// historical: a reading is only as fresh as the probe that carried it, and
// "unavailable" means no reading was recorded, not that usage is low.
type usageView struct {
	ID         string              `json:"id,omitempty"`
	State      string              `json:"state"` // available or unavailable
	ObservedAt *time.Time          `json:"observedAt,omitempty"`
	AgeSeconds *int64              `json:"ageSeconds,omitempty"`
	Source     string              `json:"source,omitempty"`
	Windows    []probe.UsageWindow `json:"windows"`
}

func usageOf(reg *health.Registry, id string, now time.Time) usageView {
	v := usageView{State: "unavailable", Windows: []probe.UsageWindow{}}
	if reg == nil {
		return v
	}
	e, ok := reg.Candidates[id]
	if !ok || !e.Usage.Available() {
		return v
	}
	at, age := e.Usage.At, int64(now.Sub(e.Usage.At)/time.Second)
	return usageView{State: "available", ObservedAt: &at, AgeSeconds: &age, Source: e.Usage.Source, Windows: e.Usage.Windows}
}

func (v usageView) text() string {
	if v.State != "available" {
		return "unavailable"
	}
	parts := make([]string, 0, len(v.Windows))
	for _, w := range v.Windows {
		s := fmt.Sprintf("%s %.0f%%", w.Key, w.Used*100)
		if w.ResetsAt != nil {
			s += " resets " + w.ResetsAt.UTC().Format("2006-01-02 15:04Z")
		}
		if w.Status != "" {
			s += " " + w.Status
		}
		parts = append(parts, s)
	}
	age := "observed after this clock's time"
	if *v.AgeSeconds >= 0 {
		age = "observed " + (time.Duration(*v.AgeSeconds) * time.Second).Round(time.Minute).String() + " ago"
	}
	return strings.Join(parts, "; ") + "; " + age + " (" + v.Source + ")"
}

func printUsage(w io.Writer, label string, views []usageView) {
	for _, v := range views {
		fmt.Fprintf(w, "%s%s: %s\n", label, v.ID, v.text())
	}
}
