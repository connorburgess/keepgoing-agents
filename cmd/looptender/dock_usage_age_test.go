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
