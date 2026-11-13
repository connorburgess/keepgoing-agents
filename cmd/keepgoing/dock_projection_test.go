package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

// Each projection is worded on the page as status words it, without the
// window's name, which the meter beside it already shows, and with each time
// as a part the page shows in its own time zone.
func TestDockProjectionPhrases(t *testing.T) {
	h := func(n int) time.Time { return dockNow.Add(time.Duration(n) * time.Hour) }
	ptr := func(t time.Time) *time.Time { return &t }
	used := func(f float64) *float64 { return &f }
	resets := h(36)
	base := projectionView{State: "projected", Readings: 3, SpanSeconds: 6 * 3600, MinReadings: 3, MinSpanSeconds: 6 * 3600, MaxAgeSeconds: 6 * 3600, now: dockNow}
	with := func(f func(*projectionView)) projectionView { p := base; f(&p); return p }
	for _, c := range []struct {
		p          projectionView
		page, cli  string
		needsReads bool
	}{
		{with(func(p *projectionView) { p.UsedAtReset = used(0.6) }),
			"60% at reset (3 readings over 6h 0m)", "w 60% at reset (3 readings over 6h 0m)", false},
		{with(func(p *projectionView) { p.UsedAtReset, p.RunsOutAt = used(2), ptr(h(12)) }),
			"Runs out about 24h 0m before it resets, at Tue 5 Mar, 00:00 (3 readings over 6h 0m)",
			"w runs out about 24h 0m before it resets, at 2030-03-05 00:00Z (3 readings over 6h 0m)", false},
		{with(func(p *projectionView) { p.UsedAtReset, p.RunsOutAt = used(2.4), ptr(h(-2)) }),
			"Projected to have run out at Mon 4 Mar, 10:00, 38h 0m before it resets (3 readings over 6h 0m)",
			"w projected to have run out at 2030-03-04 10:00Z, 38h 0m before it resets (3 readings over 6h 0m)", false},
		{with(func(p *projectionView) { p.UsedAtReset, p.UsedUpSince = used(1), ptr(h(-6)) }),
			"Used up since Mon 4 Mar, 06:00", "w used up since 2030-03-04 06:00Z", false},
		{with(func(p *projectionView) { p.UsedAtReset, p.WithinRounding = used(1.05), true }),
			"May run out before it resets, within the readings' rounding (3 readings over 6h 0m)",
			"w may run out before it resets, within the readings' rounding (3 readings over 6h 0m)", false},
		{with(func(p *projectionView) { p.State, p.Reason, p.Readings = "unavailable", "too_few_readings", 2 }),
			"Can't project: 2 of the 3 readings needed in this window", "w can't project: 2 of the 3 readings needed in this window", true},
		{with(func(p *projectionView) { p.State, p.Reason, p.ResetsInSeconds = "unavailable", "too_early", 162*3600 }),
			"Can't project: too early to project: 6h 0m of readings for a window that resets in 6d 18h",
			"w can't project: too early to project: 6h 0m of readings for a window that resets in 6d 18h", true},
		{with(func(p *projectionView) { p.State, p.Reason = "unavailable", "no_history" }),
			"Can't project: no earlier reading of this window is recorded", "w can't project: no earlier reading of this window is recorded", true},
		{with(func(p *projectionView) { p.State, p.Reason = "unavailable", "newest_too_old" }),
			"Can't project: the newest reading is older than 6h 0m", "w can't project: the newest reading is older than 6h 0m", true},
		{with(func(p *projectionView) { p.State, p.Reason = "unavailable", "no_reset_time" }),
			"Can't project: no reset time was reported", "w can't project: no reset time was reported", true},
		{with(func(p *projectionView) { p.State, p.Reason = "unavailable", "window_reset" }),
			"Can't project: the window has reset", "w can't project: the window has reset", false},
	} {
		if got := say(dockProjection(c.p, &resets), dockNow); got != c.page {
			t.Errorf("page: got  %q\n      want %q", got, c.page)
		}
		if got := c.p.text("w", &resets); got != c.cli {
			t.Errorf("status: got  %q\n        want %q", got, c.cli)
		}
		if c.p.needsReadings() != c.needsReads {
			t.Errorf("%q: needs readings %v", c.page, !c.needsReads)
		}
	}
	if dockProjection(projectionView{}, &resets) != nil {
		t.Error("a window with no projection record was worded")
	}
}

// The note about where readings come from is said once per route, only when
// a window could not be projected for want of readings, and a window that has
// reset gets no projection on the page: its reset line already says it ended.
func TestDockReadingsNoteOncePerRoute(t *testing.T) {
	read, resets, ended := dockNow.Add(-time.Minute), dockNow.Add(48*time.Hour), dockNow.Add(-time.Hour)
	projected := projectionView{State: "projected", UsedAtReset: func() *float64 { f := 0.5; return &f }(), Readings: 3, SpanSeconds: 43200, now: dockNow}
	missing := projectionView{State: "unavailable", Reason: "no_history", now: dockNow}
	reset := projectionView{State: "unavailable", Reason: "window_reset", now: dockNow}
	view := func(ws ...usageWindowView) usageView {
		return usageView{State: "available", ObservedAt: &read, AgeSeconds: func() *int64 { n := int64(60); return &n }(), ProbesOnly: true, Windows: ws}
	}
	win := func(key string, stale bool, at *time.Time, p projectionView) usageWindowView {
		return usageWindowView{UsageWindow: probe.UsageWindow{Key: key, Used: 0.4, ResetsAt: at}, Stale: stale, Projection: p}
	}
	for name, c := range map[string]struct {
		v    usageView
		note bool
	}{
		"two windows missing, said once": {view(win("five_hour", false, &resets, missing), win("seven_day", false, &resets, missing)), true},
		"one projected, one missing":     {view(win("five_hour", false, &resets, missing), win("seven_day", false, &resets, projected)), true},
		"all projected":                  {view(win("seven_day", false, &resets, projected)), false},
		"only a window that has reset":   {view(win("five_hour", true, &ended, reset), win("seven_day", false, &resets, projected)), false},
	} {
		u := dockUsageOf(c.v, dockNow)
		if (u.ReadingsNote == readingsNote) != c.note || (u.ReadingsNote != "" && u.ReadingsNote != readingsNote) {
			t.Errorf("%s: note %q", name, u.ReadingsNote)
		}
		for _, w := range u.Windows {
			if w.Stale != (len(w.Projection) == 0) {
				t.Errorf("%s: window %s stale %v projection %q", name, w.Name, w.Stale, say(w.Projection, dockNow))
			}
		}
		// status says it once too, under the same rule.
		if strings.Contains(c.v.projectionText(), readingsNote) != c.note || strings.Count(c.v.projectionText(), readingsNote) > 1 {
			t.Errorf("%s: status %q", name, c.v.projectionText())
		}
	}
}

// From recorded readings to the page: a loop's route view and the
// allowances strip show the same projection and note as status.
func TestDockShowsProjectionInRouteViewAndAllowances(t *testing.T) {
	d, _ := dockFixtureReader(t)
	r := d.read(context.Background(), false)
	first := dockLoopNamed(t, r, "docs-site-refresh").Chain.Entries[0]
	byName := map[string]dockUsageWindow{}
	for _, w := range first.Usage.Windows {
		byName[w.Name] = w
	}
	week, short := byName["7-day"], byName["5-hour"]
	wantWeek := "Runs out about 2d 9h before it resets, at Wed 6 Mar, 02:48 (3 readings over 25h 0m)"
	if say(week.Projection, dockNow) != wantWeek || say(short.Projection, dockNow) != "Can't project: no earlier reading of this window is recorded" ||
		first.Usage.ReadingsNote != readingsNote {
		t.Errorf("route view: %q %q %q", say(week.Projection, dockNow), say(short.Projection, dockNow), first.Usage.ReadingsNote)
	}
	found := false
	for _, a := range r.Allowances {
		for _, w := range a.Usage.Windows {
			if w.Name == "7-day" && say(w.Projection, dockNow) == wantWeek {
				found = found || a.Usage.ReadingsNote == accountReadingsNote
			}
		}
	}
	if !found {
		t.Errorf("no allowance line shows the projection and the note: %+v", r.Allowances)
	}
}

// A window at 100% with no recorded history is said to be used up since the
// latest reading, not "no earlier reading": that reading alone shows it.
func TestUsedUpWithoutHistory(t *testing.T) {
	read, resets := dockNow.Add(-7*time.Minute), dockNow.Add(2*time.Hour)
	p := projectionOf(nil, probe.UsageWindow{Key: "five_hour", Used: 1, ResetsAt: &resets}, read, dockNow, projectionRules(nil))
	if p.UsedUpSince == nil || !p.UsedUpSince.Equal(read) || p.needsReadings() ||
		say(dockProjection(p, &resets), dockNow) != "Used up since Mon 4 Mar, 11:53" || p.text("five_hour", &resets) != "five_hour used up since 2030-03-04 11:53Z" {
		t.Errorf("%+v %q", p, say(dockProjection(p, &resets), dockNow))
	}
	// Below 100% with no history it still says so.
	if p := projectionOf(nil, probe.UsageWindow{Key: "five_hour", Used: 0.99, ResetsAt: &resets}, read, dockNow, projectionRules(nil)); p.Reason != "no_history" {
		t.Errorf("%+v", p)
	}
}
