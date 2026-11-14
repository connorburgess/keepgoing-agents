package supervise

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

// timingWorld is a loop running on its first route, with fallbacks.
func timingWorld(t *testing.T, timing *config.Timing) *world {
	t.Helper()
	w := newWorld(t, true, true)
	w.cfg.Loop.Probe = config.Probe{Kind: "claude", Model: "example-small"}
	w.cfg.Loop.Timing = timing
	w.primary(decide.Observation{State: decide.Active})
	for _, id := range []string{"claude-other", "codex-main", "same-account"} {
		w.probeOK[id] = true
	}
	return w
}

func timingOn(every, near time.Duration) *config.Timing {
	return &config.Timing{Enabled: true, Windows: []string{"seven_day"}, NearReset: config.Duration{Duration: near}, ProbeEvery: config.Duration{Duration: every}}
}

// tickTimes runs a check at each offset from t0 and returns the probes made,
// with the first route's probes carrying a reading of both windows.
func tickTimes(t *testing.T, w *world, weekResets, shortResets time.Time, offsets ...time.Duration) []string {
	t.Helper()
	w.probes = nil
	for _, d := range offsets {
		now := t0.Add(d)
		opts := w.opts(now)
		inner := opts.Probe
		opts.Probe = func(ctx context.Context, c config.Candidate, tier probe.Tier, timeout time.Duration) probe.Result {
			r := inner(ctx, c, tier, timeout)
			if c.ID == "primary" {
				id := probe.UsageIdentity(c)
				r = probe.Result{OK: true, UsageIdentity: id, Usage: &probe.Usage{At: now, Source: probe.UsageSourceClaude, Identity: id, Windows: []probe.UsageWindow{
					{Key: "five_hour", Used: 0.2, ResetsAt: &shortResets}, {Key: "seven_day", Used: 0.4, ResetsAt: &weekResets}}}}
			}
			return r
		}
		if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
			t.Fatalf("check at %s: %v", d, err)
		}
	}
	return w.probes
}

func primaryProbes(probes []string) []string {
	out := []string{}
	for _, p := range probes {
		if strings.HasPrefix(p, "primary:") {
			out = append(out, p)
		}
	}
	return out
}

// With timing off, or present but not turned on, the probes are exactly
// today's: the route in use is never probed on a schedule.
func TestTimingOffProbesExactlyAsToday(t *testing.T) {
	week, short := t0.Add(80*time.Hour), t0.Add(3*time.Hour)
	offsets := []time.Duration{0, time.Hour, 5 * time.Hour, 13 * time.Hour, 25 * time.Hour}
	var want []string
	for i, timing := range []*config.Timing{nil, {Enabled: false, Windows: []string{"seven_day"}, NearReset: config.Duration{Duration: 6 * time.Hour}, ProbeEvery: config.Duration{Duration: time.Hour}}} {
		w := timingWorld(t, timing)
		got := tickTimes(t, w, week, short, offsets...)
		if len(primaryProbes(got)) != 0 {
			t.Fatalf("case %d: the route in use was probed: %v", i, got)
		}
		if i == 0 {
			want = got
		} else if !slices.Equal(got, want) {
			t.Fatalf("timing present but off changed the probes:\n%v\n%v", got, want)
		}
	}
	if len(want) == 0 {
		t.Fatal("no scheduled probes at all; the comparison proves nothing")
	}
}

// With timing on, the first route is probed on its own cadence while the loop
// runs on it, its readings build the history, and the day's cap holds.
func TestTimingProbesTheRouteInUseOnItsCadenceWithinTheDailyCap(t *testing.T) {
	week, short := t0.Add(80*time.Hour), t0.Add(3*time.Hour)
	w := timingWorld(t, timingOn(2*time.Hour, 6*time.Hour))
	got := primaryProbes(tickTimes(t, w, week, short, 0, time.Hour, 2*time.Hour, 4*time.Hour))
	if !slices.Equal(got, []string{"primary:scheduled", "primary:scheduled", "primary:scheduled"}) {
		t.Fatalf("every 2 h from 12:00 to 16:00: %v", got)
	}
	reg, err := health.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	e := reg.Candidates["primary"]
	if e == nil || e.ProbesToday != 3 || e.UsageHistory == nil || len(e.UsageHistory.Find("seven_day").Samples) != 3 {
		t.Fatalf("recorded: %+v", e)
	}
	// The day's cap: six scheduled probes of this route a day, then none.
	w.cfg.Loop.Timing.ProbeEvery = config.Duration{Duration: time.Hour}
	got = primaryProbes(tickTimes(t, w, week, short, 5*time.Hour, 6*time.Hour, 7*time.Hour, 8*time.Hour, 9*time.Hour))
	if len(got) != 3 {
		t.Fatalf("over the cap of 6: %d more probes", len(got))
	}
	reg, _ = health.Load(w.dir)
	if n := reg.Candidates["primary"].ProbesToday; n != 6 {
		t.Fatalf("probes today %d", n)
	}
}

// One probe is made as a named window's reported reset comes within
// near_reset, once per window instance; a window that is not named never
// causes one.
func TestTimingProbesOnceAsANamedWindowNearsItsReset(t *testing.T) {
	week, short := t0.Add(20*time.Hour), t0.Add(2*time.Hour)
	w := timingWorld(t, timingOn(24*time.Hour, 6*time.Hour))
	// 12:00 the cadence probe; 13:00 the five-hour window is near its reset
	// but not named; 02:00 the next day the weekly reset is 6 h away; 03:00
	// that instance has had its probe.
	got := primaryProbes(tickTimes(t, w, week, short, 0, time.Hour, 14*time.Hour, 15*time.Hour, 16*time.Hour))
	if len(got) != 2 {
		t.Fatalf("want the cadence probe and one near the reset: %v", got)
	}
	reg, _ := health.Load(w.dir)
	e := reg.Candidates["primary"]
	if len(e.NearReset) != 1 || e.NearReset[0].Key != "seven_day" || !e.NearReset[0].ResetsAt.Equal(week) {
		t.Fatalf("marks: %+v", e.NearReset)
	}
	if s := e.UsageHistory.Find("seven_day"); s == nil || len(s.Samples) != 2 || !s.Samples[1].At.Equal(t0.Add(14*time.Hour)) {
		t.Fatalf("history: %+v", e.UsageHistory)
	}
}

// A dry run and a check outside the loop's window make no timing probe.
func TestTimingProbeOnlyWhenScheduledProbesRun(t *testing.T) {
	week, short := t0.Add(80*time.Hour), t0.Add(3*time.Hour)
	w := timingWorld(t, timingOn(time.Hour, 6*time.Hour))
	opts := w.opts(t0)
	opts.DryRun = true
	if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
		t.Fatal(err)
	}
	if got := primaryProbes(w.probes); len(got) != 0 {
		t.Fatalf("dry run probed: %v", got)
	}
	w.cfg.Loop.Window = &config.Window{TZ: "UTC", Days: map[string][]string{"Sun": {"09:00", "10:00"}}}
	if got := primaryProbes(tickTimes(t, w, week, short, 0)); len(got) != 0 {
		t.Fatalf("outside the window: %v", got)
	}
}
