package supervise

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// lastCheckBeforeAGap records a loop that has shown its usual interval (ten
// minutes) and was last checked at `at`, by a check that was then killed
// with a send recorded but not settled: the case a gap most often follows,
// because a check killed mid-send is what makes a scheduler give up.
func lastCheckBeforeAGap(t *testing.T, w *world, at time.Time, startedBy *state.Routine) {
	t.Helper()
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	st.CheckedAt, st.ScheduleGaps, st.StartedBy = at, []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}, startedBy
	if err := state.Save(w.dir, st); err != nil {
		t.Fatal(err)
	}
}

func subscribe(w *world, subscribed bool, events ...string) {
	if subscribed {
		events = append(events, "check_gap")
	}
	w.cfg.Loop.Report = &config.Report{Events: events}
}

// After a four-hour gap, a send recorded before the gap is handled exactly
// as main handles it, whether check_gap is subscribed or not, and check_gap
// is sent once, after the check that handled it was saved.
func TestCheckGapLeavesRecordedSendsToTheRules(t *testing.T) {
	gap := 4 * time.Hour
	type outcome struct {
		action, reason              string
		reprompts, unconfirmed      int
		held                        bool
		briefHold                   int
		primaryStops, fallbackStops int
		briefsSent                  int
	}
	cases := []struct {
		name string
		run  func(t *testing.T, subscribed bool) (outcome, *world)
		want func(o outcome) error
	}{
		{"a pending re-prompt is counted", func(t *testing.T, subscribed bool) (outcome, *world) {
			w := newWorld(t, true, false)
			subscribe(w, subscribed, "error")
			w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
			lastCheckBeforeAGap(t, w, t0, nil)
			unsaved(t, w, t0, "reprompt", state.Primary)
			r := runTick(t, w, t0.Add(gap))
			st, _ := state.Load(w.dir)
			return outcome{action: r.Action, reason: r.Reason, reprompts: st.Reprompts, unconfirmed: st.UnconfirmedReprompts}, w
		}, func(o outcome) error {
			if o.unconfirmed != 1 || o.reprompts < 1 {
				return fmt.Errorf("the recorded re-prompt was not counted: %+v", o)
			}
			return nil
		}},
		{"a pending handoff brief holds for a person", func(t *testing.T, subscribed bool) (outcome, *world) {
			w, _, fallbacks := rejectingWorld(t, false)
			subscribe(w, subscribed, "await_user")
			lastCheckBeforeAGap(t, w, t0, nil)
			unsaved(t, w, t0, "handoff", "claude-other")
			r := runTick(t, w, t0.Add(gap))
			ctl, _ := control.Load(w.dir)
			return outcome{action: r.Action, reason: r.Reason, held: ctl.AwaitingUser, briefHold: len(ctl.BriefHold), briefsSent: len(fallbacks["claude-other"].sent) + len(fallbacks["codex-main"].sent)}, w
		}, func(o outcome) error {
			if o.action != "await_user" || !o.held || o.briefHold != 1 || o.briefsSent != 0 || !strings.HasPrefix(o.reason, "a handoff brief to claude-other may have been delivered") {
				return fmt.Errorf("not held for the brief: %+v", o)
			}
			return nil
		}},
		{"a requested stop stops both conversations", func(t *testing.T, subscribed bool) (outcome, *world) {
			w, p, fallbacks := rejectingWorld(t, false)
			subscribe(w, subscribed, "stop")
			lastCheckBeforeAGap(t, w, t0, nil)
			unsaved(t, w, t0, "handoff", "claude-other")
			control.Update(w.dir, func(f *control.File) { f.Stop = true })
			r := runTick(t, w, t0.Add(gap))
			return outcome{action: r.Action, reason: r.Reason, primaryStops: p.stopped, fallbackStops: fallbacks["claude-other"].stopped}, w
		}, func(o outcome) error {
			if o.primaryStops != 1 || o.fallbackStops != 1 {
				return fmt.Errorf("not both stopped: %+v", o)
			}
			return nil
		}},
		{"a paused loop keeps the record", func(t *testing.T, subscribed bool) (outcome, *world) {
			w, _, fallbacks := rejectingWorld(t, false)
			subscribe(w, subscribed, "await_user")
			lastCheckBeforeAGap(t, w, t0, nil)
			unsaved(t, w, t0, "handoff", "claude-other")
			control.Update(w.dir, func(f *control.File) { f.Paused = true })
			runTick(t, w, t0.Add(gap)) // held: does not read the records
			control.Update(w.dir, func(f *control.File) { f.Paused = false })
			r := runTick(t, w, t0.Add(gap+time.Minute))
			ctl, _ := control.Load(w.dir)
			return outcome{action: r.Action, reason: r.Reason, held: ctl.AwaitingUser, briefHold: len(ctl.BriefHold), briefsSent: len(fallbacks["claude-other"].sent) + len(fallbacks["codex-main"].sent)}, w
		}, func(o outcome) error {
			if o.action != "await_user" || !o.held || o.briefHold != 1 || o.briefsSent != 0 {
				return fmt.Errorf("the record was dropped: %+v", o)
			}
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			without, _ := tc.run(t, false)
			with, w := tc.run(t, true)
			if err := tc.want(without); err != nil {
				t.Fatalf("without check_gap: %v", err)
			}
			if with != without {
				t.Fatalf("check_gap changed the outcome:\nwith    %+v\nwithout %+v", with, without)
			}
			// A paused loop is not judged, so its gap is not reported.
			want := 1
			if strings.Contains(tc.name, "paused") {
				want = 0
			}
			if n := strings.Count(w.reportTypes(), "check_gap"); n != want {
				t.Fatalf("%d check_gap reports, want %d: %s", n, want, w.reportTypes())
			}
		})
	}
}

// savedAtDelivery reports into the world and keeps state.json as it was on
// disk when check_gap was delivered: what a check killed at that moment
// leaves.
type savedAtDelivery struct {
	w     *world
	saved *state.File
}

func (r savedAtDelivery) Report(ctx context.Context, e report.Event) error {
	if e.Type == "check_gap" {
		if st, err := state.Load(r.w.dir); err == nil {
			*r.saved = st
		}
	}
	return r.w.Report(ctx, e)
}

// check_gap is sent after the check that marks the gap reported is saved.
// A check killed while delivering it has already saved the mark and its own
// time, so no later check sends it again, however many are killed.
func TestCheckGapIsNotRepeatedAfterAKillAtDelivery(t *testing.T) {
	w := newWorld(t, true, false)
	subscribe(w, true)
	w.primary(decide.Observation{State: decide.Active})
	lastCheckBeforeAGap(t, w, t0, nil)
	var saved state.File
	at := t0.Add(4 * time.Hour)
	opts := w.opts(at)
	opts.Reporter = savedAtDelivery{w: w, saved: &saved}
	if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
		t.Fatal(err)
	}
	if !saved.CheckedAt.Equal(at) {
		t.Fatalf("delivered before this check was saved: checked %v", saved.CheckedAt)
	}
	// Killed there: the next checks start from that saved state.
	if err := state.Save(w.dir, saved); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		runTick(t, w, at.Add(time.Duration(i)*10*time.Minute))
	}
	if n := strings.Count(w.reportTypes(), "check_gap"); n != 1 {
		t.Fatalf("%d check_gap reports: %s", n, w.reportTypes())
	}
	if l, _ := state.LoadLedger(w.dir); l.Seq != 0 {
		t.Fatalf("check_gap was recorded in sends.json: %+v", l)
	}
}

// A dry run reports nothing and keeps the gap for the next real check, with
// the identity of the check before the gap; the next real check reports it.
func TestCheckGapAfterADryRun(t *testing.T) {
	w := newWorld(t, true, false)
	subscribe(w, true)
	w.primary(decide.Observation{State: decide.Active})
	auto := &state.Routine{Source: state.RoutineBBAutomation, Routine: "auto_example1"}
	lastCheckBeforeAGap(t, w, t0, auto)
	opts := w.opts(t0.Add(4 * time.Hour))
	opts.DryRun = true
	if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load(w.dir)
	if strings.Contains(w.reportTypes(), "check_gap") || st.CheckGapPending == nil || st.StartedBy == nil || *st.StartedBy != *auto {
		t.Fatalf("dry run: reports %s, pending %+v, startedBy %+v", w.reportTypes(), st.CheckGapPending, st.StartedBy)
	}
	runTick(t, w, t0.Add(4*time.Hour+10*time.Minute))
	if n := strings.Count(w.reportTypes(), "check_gap"); n != 1 {
		t.Fatalf("%d check_gap reports after the dry run", n)
	}
	e := w.reports[len(w.reports)-1]
	for _, r := range w.reports {
		if r.Type == "check_gap" {
			e = r
		}
	}
	if !strings.Contains(e.Text, "auto_example1") || !strings.Contains(e.Text, t0.Format(time.RFC3339)) {
		t.Fatalf("%q", e.Text)
	}
	runTick(t, w, t0.Add(4*time.Hour+20*time.Minute))
	if n := strings.Count(w.reportTypes(), "check_gap"); n != 1 {
		t.Fatalf("reported again: %s", w.reportTypes())
	}
}

// A loop held by a person, paused or declared complete may have had its
// scheduler stopped on purpose: resuming it sends no check_gap. A requested
// stop is not a hold.
func TestCheckGapIsNotReportedForAHeldLoop(t *testing.T) {
	for _, hold := range []string{"paused", "awaiting", "completed"} {
		w := newWorld(t, true, false)
		subscribe(w, true)
		w.primary(decide.Observation{State: decide.Active})
		lastCheckBeforeAGap(t, w, t0, nil)
		control.Update(w.dir, func(f *control.File) {
			f.Paused, f.AwaitingUser, f.Completed = hold == "paused", hold == "awaiting", hold == "completed"
		})
		runTick(t, w, t0.Add(72*time.Hour))
		if strings.Contains(w.reportTypes(), "check_gap") {
			t.Fatalf("%s: %s", hold, w.reportTypes())
		}
	}
}

// check_gap is an essential report: it may take the time reserved for those
// when the check's own budget is spent.
func TestCheckGapIsAnEssentialReport(t *testing.T) {
	if !essentialReports[checkGapEvent] {
		t.Fatal("check_gap is not essential")
	}
}

// A daemon that ran the last check and is still running was running through
// the gap: the computer slept, and no check_gap is sent.
func TestCheckGapIsNotReportedWhenTheSameDaemonSlept(t *testing.T) {
	w := newWorld(t, true, false)
	subscribe(w, true)
	w.primary(decide.Observation{State: decide.Active})
	lastCheckBeforeAGap(t, w, t0, nil)
	opts := w.opts(t0.Add(4 * time.Hour))
	opts.DaemonStartedAt = t0.Add(-time.Hour)
	if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.reportTypes(), "check_gap") {
		t.Fatalf("%s", w.reportTypes())
	}
}

// Checks turned away by an earlier check that still holds the lock: the
// report says a check may have hung, not that the scheduler stopped.
func TestCheckGapAfterTurnedAwayChecksBlamesTheHungCheck(t *testing.T) {
	w := newWorld(t, true, false)
	subscribe(w, true)
	w.primary(decide.Observation{State: decide.Active})
	lastCheckBeforeAGap(t, w, t0, nil)
	if err := state.NoteTurnedAway(w.dir, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0.Add(4*time.Hour))
	var text string
	for _, r := range w.reports {
		if r.Type == "check_gap" {
			text = r.Text
		}
	}
	if !strings.Contains(text, "each found an earlier check still running") || strings.Contains(text, "scheduler") {
		t.Fatalf("%q", text)
	}
	if _, err := os.Stat(filepath.Join(w.dir, state.TurnedAwayName)); err != nil {
		t.Fatal(err)
	}
}
