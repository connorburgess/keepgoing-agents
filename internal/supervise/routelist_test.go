package supervise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// followStandard turns the fixture loop into one that follows the standard
// list, as config.Load would have resolved it.
func followStandard(w *world, unreadable bool) {
	w.cfg.Loop.Routes = config.RoutesStandard
	w.cfg.RouteList = config.RouteStatus{Source: config.RoutesStandard, Unreadable: unreadable}
	if unreadable {
		w.cfg.FallbackSet = nil
	}
}

func routeReports(w *world) string {
	var out []string
	for _, e := range w.reports {
		if e.Data["reason"] == "standard_route_list_unreadable" {
			out = append(out, e.Type)
		}
	}
	return strings.Join(out, ",")
}

// A following loop whose standard list cannot be read is still supervised on
// its current route, never hands off, and reports the condition once when it
// begins and once when it ends.
func TestUnreadableStandardListKeepsSupervisingWithoutHandoff(t *testing.T) {
	w := newWorld(t, true, true)
	list := w.cfg.FallbackSet
	followStandard(w, true)
	p := w.primary(decide.Observation{State: decide.Idle})

	res := runTick(t, w, t0)
	if res.Action != "wake" || len(p.sent) != 1 || res.Routes == nil || !res.Routes.Unreadable || res.Routes.Source != "standard" {
		t.Fatalf("first check: %+v %+v", res, res.Routes)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), config.StandardRoutesUnreadable) || routeReports(w) != "degraded" {
		t.Fatalf("warnings %v reports %s", res.Warnings, w.reportTypes())
	}

	// A usage limit that would otherwise hand off: nothing to hand off to, no
	// probe, and no second report of any kind about routes or redundancy.
	p.obs = limited
	for i := 1; i <= 3; i++ {
		res = runTick(t, w, t0.Add(time.Duration(i)*time.Hour))
		if res.Action == "handoff" || res.Handoff != nil || res.Target != state.Primary || len(w.probes) != 0 || res.Routes == nil || !res.Routes.Unreadable {
			t.Fatalf("check %d handed off or probed: %+v", i, res)
		}
	}
	if routeReports(w) != "degraded" || strings.Count(w.reportTypes(), "degraded") != 1 {
		t.Fatalf("reports: %s", w.reportTypes())
	}

	// The loop reached its ordinary hold for the persistent error. The person
	// resumes it, and the list is readable again: reported once, routes back.
	if ctl, _ := control.Load(w.dir); !ctl.AwaitingUser {
		t.Fatalf("expected the ordinary error hold, got %+v", ctl)
	}
	if _, err := control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false }); err != nil {
		t.Fatal(err)
	}
	p.obs = decide.Observation{State: decide.Idle}
	w.cfg.FallbackSet = list
	w.cfg.RouteList.Unreadable = false
	w.probeOK["claude-other"], w.probeOK["codex-main"] = true, true
	res = runTick(t, w, t0.Add(5*time.Hour))
	if res.Routes != nil || routeReports(w) != "degraded,recovered" {
		t.Fatalf("after repair: %+v reports %s", res.Routes, w.reportTypes())
	}
	runTick(t, w, t0.Add(6*time.Hour))
	if routeReports(w) != "degraded,recovered" {
		t.Fatalf("repeated: %s", w.reportTypes())
	}
}

func TestUnreadableStandardListDryRunAndHoldsRecordNothing(t *testing.T) {
	w := newWorld(t, true, true)
	followStandard(w, true)
	w.primary(decide.Observation{State: decide.Idle})
	opts := w.opts(t0)
	opts.DryRun = true
	res, err := Tick(t.Context(), w.cfg, opts)
	if err != nil || res.Routes == nil || !res.Routes.Unreadable || len(w.reports) != 0 {
		t.Fatalf("dry run: %+v %v reports=%s", res, err, w.reportTypes())
	}
	if st, _ := state.Load(w.dir); st.RoutesUnreadable {
		t.Fatal("dry run recorded the condition")
	}
	// A held loop is not observed, so it neither reports nor forgets.
	if _, err := control.Update(w.dir, func(f *control.File) { f.Paused = true }); err != nil {
		t.Fatal(err)
	}
	if res := runTick(t, w, t0.Add(time.Hour)); res.Reason != "paused" || len(w.reports) != 0 {
		t.Fatalf("held: %+v reports=%s", res, w.reportTypes())
	}
	if _, err := control.Update(w.dir, func(f *control.File) { f.Paused = false }); err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0.Add(2*time.Hour))
	if routeReports(w) != "degraded" {
		t.Fatalf("after the hold: %s", w.reportTypes())
	}
}

// A route removed from the list (or a list that cannot be read) while the
// loop is on that route: the loop stays there, is still observed and can be
// stopped, and is never returned to the primary by the change itself.
func TestRouteRemovedFromStandardListWhileInUseStaysAndIsMarked(t *testing.T) {
	for _, kind := range []string{"removed", "unreadable", "switched_off"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, true, true)
			followStandard(w, false)
			full := w.cfg.FallbackSet
			w.primary(decide.Observation{State: decide.Idle})
			onRoute := &fakeAdapter{obs: decide.Observation{State: decide.Active}}
			w.adapters["claude-other"] = onRoute
			if err := state.Save(w.dir, state.File{ActiveTarget: "claude-other", LastWakeAt: t0}); err != nil {
				t.Fatal(err)
			}
			// While the route is in the list, each check saves its record.
			if res := runTick(t, w, t0); res.Target != "claude-other" || res.Routes != nil {
				t.Fatalf("before: %+v", res)
			}
			if c, ok := LoadCurrentRoute(w.dir); !ok || c.ID != "claude-other" || c.BB == nil || c.BB.Thread != "thr_claude-other" {
				t.Fatalf("saved record: %+v %t", c, ok)
			}
			switch kind {
			case "removed":
				without := *full
				without.Candidates = []config.Candidate{full.Candidates[0], full.Candidates[2]}
				w.cfg.FallbackSet = &without
			case "switched_off":
				off := *full
				off.Candidates = append([]config.Candidate{}, full.Candidates...)
				off.Candidates[1].Disabled = true
				w.cfg.FallbackSet = &off
			case "unreadable":
				followStandard(w, true)
			}
			for i := 1; i <= 3; i++ {
				res := runTick(t, w, t0.Add(time.Duration(i)*time.Minute))
				if res.Target != "claude-other" || res.State != "active" || res.Routes == nil || !res.Routes.CurrentNotInList || res.Routes.Unreadable != (kind == "unreadable") {
					t.Fatalf("check %d left the route: %+v %+v", i, res, res.Routes)
				}
				if !strings.Contains(strings.Join(res.Warnings, "\n"), "no longer in this loop's route list") {
					t.Fatalf("warnings: %v", res.Warnings)
				}
			}
			if st, _ := state.Load(w.dir); st.Target() != "claude-other" {
				t.Fatalf("state moved to %s", st.Target())
			}
			if len(w.adapters[state.Primary].sent) != 0 || len(onRoute.sent) != 0 {
				t.Fatal("the change itself caused a send")
			}
			// Stop still reaches the route the loop is on.
			if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
				t.Fatal(err)
			}
			if res := runTick(t, w, t0.Add(10*time.Minute)); res.Action != "stop" || onRoute.stopped != 1 || res.Target != "claude-other" {
				t.Fatalf("stop: %+v stopped=%d", res, onRoute.stopped)
			}
		})
	}
}

func TestCurrentRouteRecordIsOnlyUsedForItsOwnTargetAndStandardLoops(t *testing.T) {
	w := newWorld(t, true, true)
	full := w.cfg.FallbackSet
	if err := saveCurrentRoute(w.dir, full.Candidates[1]); err != nil {
		t.Fatal(err)
	}
	without := *full
	without.Candidates = []config.Candidate{full.Candidates[0], full.Candidates[2]}
	w.cfg.FallbackSet = &without
	w.primary(decide.Observation{State: decide.Idle})

	// A loop with a list of its own keeps today's rule: a removed target
	// returns to the primary.
	if err := state.Save(w.dir, state.File{ActiveTarget: "claude-other"}); err != nil {
		t.Fatal(err)
	}
	if res := runTick(t, w, t0); res.Target != state.Primary || res.Routes != nil {
		t.Fatalf("own list: %+v", res)
	}

	// A following loop on a different missing target does not borrow the
	// record of another route.
	followStandard(w, false)
	w.cfg.FallbackSet = &without
	if err := state.Save(w.dir, state.File{ActiveTarget: "another-route"}); err != nil {
		t.Fatal(err)
	}
	if res := runTick(t, w, t0.Add(time.Hour)); res.Target != state.Primary {
		t.Fatalf("unrelated record used: %+v", res)
	}

	// Unusable records are no record.
	for name, content := range map[string]string{"malformed": "version: [", "future": "version: 2\ncandidate: {id: claude-other}\n", "primary": "version: 1\ncandidate: {id: primary}\n", "unknown_field": "version: 1\ncandidate: {id: claude-other}\nextra: true\n", "oversized": "version: 1\n# " + strings.Repeat("x", currentRouteLimit) + "\n"} {
		if err := os.WriteFile(filepath.Join(w.dir, CurrentRouteName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := LoadCurrentRoute(w.dir); ok {
			t.Fatalf("%s record accepted", name)
		}
	}
}
