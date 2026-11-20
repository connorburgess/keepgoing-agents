package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/probe"
	"github.com/looptender/looptender/internal/registry"
	"github.com/looptender/looptender/internal/state"
)

// A usage reading is old when it is older than the panel's stale-after,
// whatever its windows say; that every window has reset since is a separate
// state. The report names the age of the oldest old reading it shows.
func TestDockMarksOldUsageReadingsApartFromEndedWindows(t *testing.T) {
	type reading struct {
		loop    string
		age     time.Duration
		resetIn time.Duration // from now; negative when the window has ended
	}
	build := func(staleAfter time.Duration, readings ...reading) dockReport {
		var inputs []dockInput
		for n, x := range readings {
			in := dockEvidence{controls: &clientControls{Enabled: true}}.input()
			in.Entry = registry.Entry{ID: fmt.Sprintf("loop_%032x", n+1), Loop: x.loop}
			c := config.Candidate{ID: state.Primary}
			// A group of its own per loop, so each reading is its own route.
			c.Adapter, c.Provider, c.Group = "bb", "example", "group-"+x.loop
			in.Chain = &dockChainInput{Primary: c}
			read, reset := dockNow.Add(-x.age), dockNow.Add(x.resetIn)
			w := usageWindowView{UsageWindow: probe.UsageWindow{Key: "seven_day", Used: 0.4, ResetsAt: &reset}, Stale: !reset.After(dockNow)}
			st := "available"
			if w.Stale {
				st = "stale"
			}
			in.Chain.PrimaryUse = &usageView{State: st, ObservedAt: &read, AgeSeconds: secondsSince(dockNow, read), Windows: []usageWindowView{w}, ProbesOnly: true}
			inputs = append(inputs, in)
		}
		return buildDockReport(inputs, dockNow, time.Minute, staleAfter, false, true, 0, true)
	}
	usage := func(r dockReport, loop string) dockUsageTarget {
		t.Helper()
		l := dockLoopNamed(t, r, loop)
		u := l.Chain.Entries[0].Usage
		// The route and the account line carry the same reading, marked the same way.
		for _, x := range r.Routes {
			if x.Key == l.Chain.Entries[0].Route && (x.Usage.Old != u.Old || x.Usage.Availability != u.Availability) {
				t.Errorf("%s: route usage %+v, loop usage %+v", loop, x.Usage, u)
			}
		}
		return u
	}

	r := build(defaultStaleAfter,
		reading{"example-fresh", 10 * time.Minute, 48 * time.Hour},
		reading{"example-old", 27 * time.Hour, 48 * time.Hour},
		reading{"example-ended", 10 * time.Minute, -5 * time.Minute},
	)
	if u := usage(r, "example-fresh"); u.Availability != "available" || u.Old {
		t.Errorf("fresh reading: %+v", u)
	}
	// 27 h old, its window still current: old, still available.
	if u := usage(r, "example-old"); u.Availability != "available" || !u.Old || u.Windows[0].Stale {
		t.Errorf("old reading: %+v", u)
	}
	// Recent, but its only window has ended: stale availability, not old.
	if u := usage(r, "example-ended"); u.Availability != "stale" || u.Old || !u.Windows[0].Stale {
		t.Errorf("ended reading: %+v", u)
	}
	for _, a := range r.Allowances {
		if a.Usage == nil {
			continue
		}
		if a.Usage.Old != (a.Usage.AgeSeconds != nil && *a.Usage.AgeSeconds > int64(defaultStaleAfter/time.Second)) {
			t.Errorf("allowance %q: %+v", a.Title, a.Usage)
		}
	}
	if r.OldestUsageAgeSeconds == nil || *r.OldestUsageAgeSeconds != int64(27*time.Hour/time.Second) {
		t.Errorf("oldest usage age %v", r.OldestUsageAgeSeconds)
	}

	// The threshold is the panel's stale-after, not a fixed one.
	if u := usage(build(5*time.Minute, reading{"example-fresh", 10 * time.Minute, 48 * time.Hour}), "example-fresh"); !u.Old {
		t.Errorf("10 min reading with a 5 min stale-after: %+v", u)
	}
	// At exactly stale-after a reading is not yet old, as for a check.
	if u := usage(build(time.Hour, reading{"example-edge", time.Hour, 48 * time.Hour}), "example-edge"); u.Old {
		t.Errorf("reading at stale-after: %+v", u)
	}

	// With no old reading, the report carries no age for the header, and the
	// field is absent from the data rather than zero.
	fresh := build(defaultStaleAfter, reading{"example-fresh", 10 * time.Minute, 48 * time.Hour})
	if fresh.OldestUsageAgeSeconds != nil {
		t.Errorf("oldest usage age %d with no old reading", *fresh.OldestUsageAgeSeconds)
	}
	body, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(body); strings.Contains(s, `"oldestUsageAgeSeconds"`) || strings.Contains(s, `"old":`) {
		t.Errorf("absent fields serialized: %s", s)
	}
	body, err = json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(body); !strings.Contains(s, `"oldestUsageAgeSeconds":97200`) || !strings.Contains(s, `"old":true`) {
		t.Errorf("old reading not in the data")
	}
}

// An old reading needs a person only when it leaves a question open: a
// current window at 70% to 99% when read, no current window used up, and the
// route or account it is shown on not already at a usage limit. Age alone, an
// ended window, a used-up window or a place at a limit never does.
func TestDockOldUsageNeedsPersonOnlyNearLimit(t *testing.T) {
	age := func(d time.Duration) *int64 { s := int64(d / time.Second); return &s }
	win := func(pct int, ended bool) dockUsageWindow {
		return dockUsageWindow{Name: "7 days", UsedPercent: pct, Stale: ended}
	}
	cases := []struct {
		name    string
		age     time.Duration
		windows []dockUsageWindow
		limit   bool // the route carries attention "limit"
		old     bool
		needs   bool
		ageNote string
		availab string
	}{
		{name: "recent and high is not old", age: 10 * time.Minute, windows: []dockUsageWindow{win(95, false)}, availab: "available"},
		{name: "old and low is age only", age: 32 * time.Hour, windows: []dockUsageWindow{win(40, false)}, old: true, availab: "available"},
		{name: "old just under 70", age: 32 * time.Hour, windows: []dockUsageWindow{win(69, false)}, old: true, availab: "available"},
		{name: "old at 70", age: 32 * time.Hour, windows: []dockUsageWindow{win(10, false), win(70, false)}, old: true, needs: true, availab: "available",
			ageNote: "This reading is 32 h old and was near its limit; it may have changed."},
		{name: "old at 99", age: 32 * time.Hour, windows: []dockUsageWindow{win(99, false)}, old: true, needs: true, availab: "available",
			ageNote: "This reading is 32 h old and was near its limit; it may have changed."},
		{name: "old and used up is age only", age: 3 * time.Hour, windows: []dockUsageWindow{win(100, false)}, old: true, availab: "available"},
		{name: "old over 100 is age only", age: 3 * time.Hour, windows: []dockUsageWindow{win(104, false)}, old: true, availab: "available"},
		{name: "old with one window used up and one at 80 is age only", age: 32 * time.Hour, windows: []dockUsageWindow{win(100, false), win(80, false)}, old: true, availab: "available"},
		{name: "old with a used-up window that has ended and one at 80", age: 32 * time.Hour, windows: []dockUsageWindow{win(100, true), win(80, false)}, old: true, needs: true, availab: "available",
			ageNote: "This reading is 32 h old and was near its limit; it may have changed."},
		{name: "old with a high window that has ended", age: 32 * time.Hour, windows: []dockUsageWindow{win(90, true), win(20, false)}, old: true, availab: "available"},
		{name: "windows ended stays neutral", age: 32 * time.Hour, windows: []dockUsageWindow{win(90, true)}, old: true, availab: "stale"},
		{name: "old at 50 on a route at a limit is age only", age: 50 * time.Hour, windows: []dockUsageWindow{win(50, false)}, limit: true, old: true, availab: "available"},
		{name: "old at 80 on a route at a limit is age only", age: 50 * time.Hour, windows: []dockUsageWindow{win(80, false)}, limit: true, old: true, availab: "available"},
		{name: "recent on a route at a limit is not about age", age: 5 * time.Minute, windows: []dockUsageWindow{win(5, false)}, limit: true, availab: "available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := dockUsageTarget{Availability: tc.availab, AgeSeconds: age(tc.age), Windows: tc.windows}
			route := dockRoute{Key: "route-example", Usage: u}
			allowance := u
			if tc.limit {
				route.Attention = "limit"
			}
			r := dockReport{
				Loops:      []dockLoop{{Chain: dockChain{Entries: []dockChainEntry{{Route: "route-example", Usage: u}}}}},
				Routes:     []dockRoute{route},
				Allowances: []dockAllowance{{Title: "example", Usage: &allowance}},
			}
			if tc.limit {
				r.Allowances[0].Notes = []dockNote{{Tone: "limit"}}
			}
			dockMarkOldUsage(&r, defaultStaleAfter)
			for where, got := range map[string]dockUsageTarget{"chain": r.Loops[0].Chain.Entries[0].Usage, "route": r.Routes[0].Usage, "allowance": *r.Allowances[0].Usage} {
				if got.Old != tc.old || got.NeedsPerson != tc.needs || got.AgeNote != tc.ageNote {
					t.Errorf("%s: old %v needsPerson %v note %q, want %v %v %q", where, got.Old, got.NeedsPerson, got.AgeNote, tc.old, tc.needs, tc.ageNote)
				}
			}
		})
	}
}
