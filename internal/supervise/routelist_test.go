package supervise

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
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

const supervisedList = `version: 1
redundancy: {min_healthy: 1, span: none}
probe: {every: 4h, ttl: 6h, timeout: 1s}
routes:
  - {id: large, provider: claude-code, model: example-large, group: claude-large, enter: same_conversation}
  - {id: huge, provider: claude-code, model: example-huge, group: claude-huge, enter: same_conversation}
`

// standardWorld is a loop that follows a real standard list, resolved by the
// config package exactly as Load does. without names routes to leave out.
func standardWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, true, false)
	w.cfg.Loop.Routes = config.RoutesStandard
	setList(t, w)
	return w
}

func setList(t *testing.T, w *world, without ...string) {
	t.Helper()
	r, err := config.ParseRoutes([]byte(supervisedList))
	if err != nil {
		t.Fatal(err)
	}
	kept := r.Routes[:0]
	for _, rt := range r.Routes {
		if !slices.Contains(without, rt.ID) {
			kept = append(kept, rt)
		}
	}
	r.Routes = kept
	fb, skipped := r.Resolve(w.cfg.Loop)
	w.cfg.FallbackSet = fb
	w.cfg.RouteList = config.RouteStatus{Source: config.RoutesStandard, Skipped: skipped}
}

// healthy records a passing scheduled check for every route, so a handoff
// meets the redundancy rule.
func healthy(t *testing.T, w *world) {
	t.Helper()
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, c := range w.cfg.FallbackSet.Candidates {
		reg.Record(c.ID, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
		w.probeOK[c.ID] = true
	}
	if err := reg.Save(w.dir); err != nil {
		t.Fatal(err)
	}
}

// One check hands off; before the next, the standard list changes. The loop
// must still be on the route doing the work, with no check in between to
// save anything.
func TestHandoffThenListChangeKeepsTheRouteDoingTheWork(t *testing.T) {
	for _, change := range []string{"route_removed", "list_unreadable", "stop_requested"} {
		t.Run(change, func(t *testing.T) {
			w := standardWorld(t)
			healthy(t, w)
			p := w.primary(limited)
			res := runTick(t, w, t0)
			if res.Action != "handoff" || res.Target != "large" {
				t.Fatalf("handoff: %+v %+v", res, res.Handoff)
			}
			large := w.adapters["large"]
			large.obs = decide.Observation{State: decide.Active}
			sentBefore := len(large.sent)
			switch change {
			case "route_removed":
				setList(t, w, "large")
				healthy(t, w)
			case "list_unreadable":
				followStandard(w, true)
			case "stop_requested":
				setList(t, w, "large")
				if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
					t.Fatal(err)
				}
			}
			res = runTick(t, w, t0.Add(time.Minute))
			if res.Target != "large" || res.Handoff != nil || res.Action == "handoff" {
				t.Fatalf("left the route doing the work: %+v %+v", res, res.Handoff)
			}
			// A stop check acts first and does not describe the list.
			if change != "stop_requested" && (res.Routes == nil || !res.Routes.CurrentNotInList) {
				t.Fatalf("not marked: %+v", res.Routes)
			}
			if huge := w.adapters["huge"]; huge != nil && len(huge.sent) != 0 {
				t.Fatal("a second route was given the task")
			}
			if len(p.sent) != 0 || len(large.sent) != sentBefore {
				t.Fatalf("sent: primary %d, route %d (was %d)", len(p.sent), len(large.sent), sentBefore)
			}
			if change == "stop_requested" && (res.Action != "stop" || large.stopped != 1 || p.stopped != 0) {
				t.Fatalf("stop did not reach the route: %+v route=%d primary=%d", res, large.stopped, p.stopped)
			}
			if st, _ := state.Load(w.dir); st.Target() != "large" {
				t.Fatalf("state moved to %s", st.Target())
			}
		})
	}
}

// A route removed (or a list that cannot be read) while the loop has been on
// it for some checks: the loop stays, is still observed and can be stopped.
func TestRouteRemovedFromStandardListWhileInUseStaysAndIsMarked(t *testing.T) {
	for _, kind := range []string{"removed", "unreadable", "switched_off"} {
		t.Run(kind, func(t *testing.T) {
			w := standardWorld(t)
			w.primary(decide.Observation{State: decide.Idle})
			onRoute := &fakeAdapter{obs: decide.Observation{State: decide.Active}}
			w.adapters["large"] = onRoute
			if err := state.Save(w.dir, state.File{ActiveTarget: "large", LastWakeAt: t0}); err != nil {
				t.Fatal(err)
			}
			if res := runTick(t, w, t0); res.Target != "large" || res.Routes != nil {
				t.Fatalf("before: %+v", res)
			}
			if c, ok := LoadCurrentRoute(w.dir); !ok || c.ID != "large" || c.BB == nil || c.BB.Thread != "thr_primary" || c.BB.Model != "example-large" {
				t.Fatalf("saved record: %+v %t", c, ok)
			}
			switch kind {
			case "removed":
				setList(t, w, "large")
			case "switched_off":
				off := *w.cfg.FallbackSet
				off.Candidates = append([]config.Candidate{}, off.Candidates...)
				off.Candidates[0].Disabled = true
				w.cfg.FallbackSet = &off
			case "unreadable":
				followStandard(w, true)
			}
			for i := 1; i <= 3; i++ {
				res := runTick(t, w, t0.Add(time.Duration(i)*time.Minute))
				if res.Target != "large" || res.State != "active" || res.Routes == nil || !res.Routes.CurrentNotInList || res.Routes.Unreadable != (kind == "unreadable") {
					t.Fatalf("check %d left the route: %+v %+v", i, res, res.Routes)
				}
				if !strings.Contains(strings.Join(res.Warnings, "\n"), "no longer in this loop's route list") {
					t.Fatalf("warnings: %v", res.Warnings)
				}
			}
			if len(w.adapters[state.Primary].sent) != 0 || len(onRoute.sent) != 0 {
				t.Fatal("the change itself caused a send")
			}
			if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
				t.Fatal(err)
			}
			if res := runTick(t, w, t0.Add(10*time.Minute)); res.Action != "stop" || onRoute.stopped != 1 || res.Target != "large" {
				t.Fatalf("stop: %+v stopped=%d", res, onRoute.stopped)
			}
		})
	}
}

// Only a record the standard list could have produced for this loop, as it
// is configured now, is used. Anything else returns the loop to its first
// route, as for any target that is not in its list.
func TestSavedRouteRecordMustBeAShapeTheListCanProduce(t *testing.T) {
	w := standardWorld(t)
	real := w.cfg.FallbackSet.Candidates[0]
	for _, tc := range []struct {
		name string
		edit func(*config.Candidate)
	}{
		{"process_adapter", func(c *config.Candidate) {
			c.Adapter, c.BB = "process", nil
			c.Process = &config.Process{StatusCommand: []string{"/fictional/status"}, ResumeCommand: []string{"/fictional/resume"}}
		}},
		{"two_adapters", func(c *config.Candidate) { c.Claude = &config.Claude{Workdir: "/fictional/elsewhere"} }},
		{"another_thread", func(c *config.Candidate) { c.BB.Thread = "thr_somebody_else" }},
		{"another_bb_cli", func(c *config.Candidate) { c.BB.CLI = "/fictional/other-bb" }},
		{"command_probe", func(c *config.Candidate) { c.Probe.Kind, c.Probe.Command = "command", []string{"/fictional/probe"} }},
		{"probe_env", func(c *config.Candidate) { c.Probe.Env = map[string]string{"PATH": "/fictional"} }},
		{"bad_id", func(c *config.Candidate) { c.ID = "Not An ID" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := real
			bb := *real.BB
			c.BB = &bb
			tc.edit(&c)
			if err := saveCurrentRoute(w.dir, c); err != nil {
				t.Fatal(err)
			}
			if err := state.Save(w.dir, state.File{ActiveTarget: c.ID}); err != nil {
				t.Fatal(err)
			}
			setList(t, w, "large")
			if fb, outside := EffectiveRoutes(w.cfg, state.File{ActiveTarget: c.ID}); outside {
				t.Fatalf("record accepted: %+v", fb)
			}
			w.primary(decide.Observation{State: decide.Idle})
			if res := runTick(t, w, t0); res.Target != state.Primary {
				t.Fatalf("an untrusted record was used: %+v", res)
			}
		})
	}
	// The genuine record is used.
	if err := saveCurrentRoute(w.dir, real); err != nil {
		t.Fatal(err)
	}
	setList(t, w, "large")
	if _, outside := EffectiveRoutes(w.cfg, state.File{ActiveTarget: "large"}); !outside {
		t.Fatal("the genuine record was refused")
	}
	// A loop whose own configuration changed since (another thread) refuses it.
	w.cfg.Loop.BB.Thread = "thr_moved"
	if _, outside := EffectiveRoutes(w.cfg, state.File{ActiveTarget: "large"}); outside {
		t.Fatal("a record for another configuration was used")
	}
}

func TestCurrentRouteRecordIsOnlyUsedForItsOwnTargetAndStandardLoops(t *testing.T) {
	w := standardWorld(t)
	real := w.cfg.FallbackSet.Candidates[0]
	if err := saveCurrentRoute(w.dir, real); err != nil {
		t.Fatal(err)
	}
	setList(t, w, "large")
	w.primary(decide.Observation{State: decide.Idle})

	// A loop with a list of its own keeps today's rule: a removed target
	// returns to the primary.
	w.cfg.Loop.Routes, w.cfg.RouteList = "", config.RouteStatus{Source: "own"}
	if err := state.Save(w.dir, state.File{ActiveTarget: "large"}); err != nil {
		t.Fatal(err)
	}
	if res := runTick(t, w, t0); res.Target != state.Primary || res.Routes != nil {
		t.Fatalf("own list: %+v", res)
	}

	// A following loop on a different missing target does not borrow it.
	w.cfg.Loop.Routes = config.RoutesStandard
	setList(t, w, "large")
	if err := state.Save(w.dir, state.File{ActiveTarget: "another-route"}); err != nil {
		t.Fatal(err)
	}
	if res := runTick(t, w, t0.Add(time.Hour)); res.Target != state.Primary {
		t.Fatalf("unrelated record used: %+v", res)
	}

	for name, content := range map[string]string{"malformed": "version: [", "future": "version: 2\ncandidate: {id: large}\n", "primary": "version: 1\ncandidate: {id: primary}\n", "unknown_field": "version: 1\ncandidate: {id: large}\nextra: true\n", "oversized": "version: 1\n# " + strings.Repeat("x", currentRouteLimit) + "\n"} {
		if err := os.WriteFile(filepath.Join(w.dir, CurrentRouteName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := LoadCurrentRoute(w.dir); ok {
			t.Fatalf("%s record accepted", name)
		}
	}
}

// Retarget saves the record before the target, so a list change right after
// cannot strand the loop either.
func TestSaveCurrentRouteOnlyForFollowingLoopsAndKnownRoutes(t *testing.T) {
	w := standardWorld(t)
	if err := SaveCurrentRoute(w.cfg, w.cfg.FallbackSet, state.Primary); err != nil {
		t.Fatal(err)
	}
	if err := SaveCurrentRoute(w.cfg, w.cfg.FallbackSet, "not-a-route"); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadCurrentRoute(w.dir); ok {
		t.Fatal("a record was saved for the primary or an unknown route")
	}
	if err := SaveCurrentRoute(w.cfg, w.cfg.FallbackSet, "huge"); err != nil {
		t.Fatal(err)
	}
	if c, ok := LoadCurrentRoute(w.dir); !ok || c.ID != "huge" {
		t.Fatalf("%+v %t", c, ok)
	}
	own := newWorld(t, true, true)
	if err := SaveCurrentRoute(own.cfg, own.cfg.FallbackSet, "claude-other"); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadCurrentRoute(own.dir); ok {
		t.Fatal("a loop with its own list saved a record")
	}
}

// A report of an unreadable list recorded, then not settled because the
// check could not save state: the next check counts it as made, not as a
// send to the agent, and does not send it again.
func TestUnsettledRouteListReportIsNotRepeatedOrCountedAsASend(t *testing.T) {
	for _, key := range []string{"report:routes-unreadable", "report:routes-readable"} {
		t.Run(key, func(t *testing.T) {
			w := newWorld(t, true, true)
			followStandard(w, key == "report:routes-unreadable")
			w.primary(decide.Observation{State: decide.Active})
			if key == "report:routes-readable" {
				if err := state.Save(w.dir, state.File{RoutesUnreadable: true}); err != nil {
					t.Fatal(err)
				}
			}
			unsaved(t, w, t0, key, state.Primary)
			res := runTick(t, w, t0.Add(time.Minute))
			st, _ := state.Load(w.dir)
			if routeReports(w) != "" || !st.LastWakeAt.IsZero() || st.RoutesUnreadable != (key == "report:routes-unreadable") {
				t.Fatalf("%+v reports=%s state=%+v", res, w.reportTypes(), st)
			}
			if strings.Contains(strings.Join(res.Warnings, "\n"), "counted as sent") {
				t.Fatalf("counted as a send: %v", res.Warnings)
			}
		})
	}
}

// A loop that stops following the list forgets that it was unreadable, so it
// reports nothing stale if it follows the list again.
func TestStoppingFollowingClearsTheUnreadableMark(t *testing.T) {
	w := newWorld(t, true, true)
	followStandard(w, true)
	w.primary(decide.Observation{State: decide.Active})
	runTick(t, w, t0)
	if st, _ := state.Load(w.dir); !st.RoutesUnreadable {
		t.Fatal("not marked")
	}
	w.cfg.Loop.Routes, w.cfg.RouteList = "", config.RouteStatus{}
	runTick(t, w, t0.Add(time.Hour))
	if st, _ := state.Load(w.dir); st.RoutesUnreadable {
		t.Fatal("mark kept after the loop stopped following the list")
	}
}
