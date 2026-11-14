package config

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// TimingSummary is what validate, doctor, status and the probe plan say about
// usage-aware timing, built once from configuration so each says the same.
type TimingSummary struct {
	// Collecting says what timing does today.
	Collecting string `json:"collecting"`
	// Cost says how many probes it makes, on which model, and what that
	// model's readings cover.
	Cost string `json:"cost"`
	// Warnings are said as warnings: where readings come from, or why none
	// will be collected.
	Warnings []string `json:"warnings"`
	// ProbesPerWeek counts the cadence probes alone; one more is made as each
	// named window nears its reset, within the daily cap.
	ProbesPerWeek int      `json:"probesPerWeek"`
	ProbeEvery    string   `json:"probeEvery"`
	ProbeModel    string   `json:"probeModel"`
	RouteModel    string   `json:"routeModel"`
	Windows       []string `json:"windows"`
	DailyCap      int      `json:"dailyCap"`
}

// TimingSummary is nil when timing is off.
func (c *Config) TimingSummary() *TimingSummary {
	l := c.Loop
	if !l.TimingOn() {
		return nil
	}
	t := l.Timing
	first := l.PrimaryCandidate()
	route := "the first route"
	if m := first.Model(); m != "" {
		route += " (" + m + ")"
	}
	s := &TimingSummary{
		Collecting: "timing: collecting usage readings for " + route + "; it does not change routes yet",
		Warnings:   []string{},
		ProbeEvery: t.ProbeEvery.Duration.String(),
		ProbeModel: first.ProbeModel(false),
		RouteModel: first.Model(),
		Windows:    append([]string{}, t.Windows...),
	}
	if every := t.ProbeEvery.Duration; every > 0 {
		s.ProbesPerWeek = int(math.Round(float64(7*24*time.Hour) / float64(every)))
	}
	if fb := c.FallbackSet; fb != nil {
		s.DailyCap = fb.Probe.DailyCap
	}
	model := s.ProbeModel
	switch {
	case model == "":
		model = "the provider's default model"
	case s.ProbeModel == s.RouteModel:
		model += ", the route's own model, so a window for that model alone is read too"
	default:
		model += ", the probe model: readings cover the windows that model reports, and a window for " + orUnnamed(s.RouteModel) + " alone is read only when the probe model is the route's own model"
	}
	s.Cost = fmt.Sprintf("timing: about %d small paid probes a week for this allowance at a %s cadence, plus one as each named window (%s) nears its reset, never above probe.daily_cap (%d a day); on %s",
		s.ProbesPerWeek, shortDuration(t.ProbeEvery.Duration), strings.Join(t.Windows, ", "), s.DailyCap, model)
	if l.Adapter == "bb" {
		s.Warnings = append(s.Warnings, "timing: readings come from this computer's CLI sign-in, which may not be the account BB uses for this thread")
	}
	if fb := c.FallbackSet; fb != nil && fb.Probe.Disabled {
		s.Warnings = append(s.Warnings, "timing: scheduled probes are off in the fallbacks file (probe.scheduled_disabled), so no timing probe runs")
	}
	if c.RouteList.Unreadable {
		s.Warnings = append(s.Warnings, "timing preview unavailable: the standard route list could not be read")
	} else if c.TimingStepsUnavailable != "" {
		s.Warnings = append(s.Warnings, "timing preview unavailable: "+c.TimingStepsUnavailable)
	}
	return s
}

func orUnnamed(model string) string {
	if model == "" {
		return "the route's model"
	}
	return model
}

// shortDuration prints 12h rather than 12h0m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	s = strings.TrimSuffix(s, "0m")
	return s
}
