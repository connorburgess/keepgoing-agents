package supervise

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

const graphWarning = "run reference record unavailable; discovery evidence is incomplete"

func loadGraph(t *testing.T, dir string) rungraph.Graph {
	t.Helper()
	g, availability := rungraph.Load(dir)
	if availability != rungraph.Available {
		t.Fatalf("graph is %s", availability)
	}
	return g
}

// watchingAdapter reads the graph from inside Send, as a crash at that
// moment would leave it.
type watchingAdapter struct {
	*fakeAdapter
	dir    string
	during []rungraph.Attempt
}

func (a *watchingAdapter) Send(ctx context.Context, p string, m adapter.Mode) error {
	g, _ := rungraph.Load(a.dir)
	a.during = append([]rungraph.Attempt{}, g.Attempts...)
	return a.fakeAdapter.Send(ctx, p, m)
}

func TestTickRecordsRunIntentBeforeSendSessionAndGoal(t *testing.T) {
	w := newWorld(t, true, false) // enabled with no graph, as an older build leaves it
	w.cfg.Loop.PromptGoal = true
	goal, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", control.GoalSpec{Title: "fictional goal", ObjectiveRefs: []string{"objective"}, AcceptanceRefs: []string{"acceptance"}}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	watch := &watchingAdapter{fakeAdapter: w.primary(decide.Observation{State: decide.Idle}), dir: w.dir}
	o := w.opts(t0)
	o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return watch, nil }
	if res, err := Tick(context.Background(), w.cfg, o); err != nil || res.Action != "wake" || len(res.Warnings) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(watch.during) != 1 || watch.during[0].Outcome != rd.Intent || !watch.during[0].ReturnedAt.IsZero() {
		t.Fatalf("the intent was not on disk before the send: %#v", watch.during)
	}
	g := loadGraph(t, w.dir)
	run, status := g.Current()
	if status != rungraph.Found || run.Run.Origin != rd.Adopted || run.Source != rungraph.SourceReconciled || !run.Run.ClosedAt.IsZero() {
		t.Fatalf("%s %#v", status, run.Run)
	}
	a := g.Attempts[0]
	if a.ID != watch.during[0].ID || a.Run != run.Ref || a.Outcome != rd.AdapterReturned || a.Target != state.Primary || a.Action != "wake" || a.Mode != "auto" || a.GoalID != goal.ID || a.GoalRevision != 1 {
		t.Fatalf("%#v", a)
	}
	sessions := g.Follow(run, rungraph.EdgeSession)
	if len(sessions) != 1 || sessions[0].Ref != rungraph.SessionRef("bb", "thr_primary") || sessions[0].Source != rungraph.SourceConfigured {
		t.Fatalf("%#v", sessions)
	}
	if goals := g.Follow(run, rungraph.EdgeGoalRevision); len(goals) != 1 || goals[0].Ref != rungraph.GoalRevisionRef(goal.ID, 1) || goals[0].Source != rungraph.SourceGoal {
		t.Fatalf("%#v", goals)
	}
	// A later failed send is a second attempt in the same run. A revised goal
	// adds a revision link; the unchanged chat adds nothing.
	if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", control.GoalSpec{Title: "fictional goal", ObjectiveRefs: []string{"objective"}, AcceptanceRefs: []string{"acceptance"}}, goal.ID, 1); err != nil {
		t.Fatal(err)
	}
	watch.sendErr = errors.New("fictional-private-send-error")
	o.Now = t0.Add(2 * time.Hour)
	if _, err := Tick(context.Background(), w.cfg, o); err == nil {
		t.Fatal("send error was swallowed")
	}
	g = loadGraph(t, w.dir)
	if g.CurrentRun != run.Ref || len(g.Runs) != 1 || len(g.Attempts) != 2 || g.Attempts[1].ID == a.ID || g.Attempts[1].Outcome != rd.AdapterError || g.Attempts[1].GoalRevision != 2 || len(g.Links) != 3 {
		t.Fatalf("%#v", g)
	}
	b, _ := os.ReadFile(filepath.Join(w.dir, rungraph.FileName))
	if strings.Contains(string(b), "fictional") || strings.Contains(string(b), "keep going") {
		t.Fatal("task text, goal text or an adapter error reached the graph")
	}
}

func TestHandoffKeepsTheRunAndBothSessions(t *testing.T) {
	w := newWorld(t, true, true)
	w.primary(decide.Observation{State: decide.Idle})
	runTick(t, w, t0)
	w.primary(limited)
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"same-account", "claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0, Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	w.probeOK = map[string]bool{"codex-main": true}
	if res := runTick(t, w, t0.Add(time.Hour)); res.Action != "handoff" || res.Target != "codex-main" {
		t.Fatalf("%+v", res)
	}
	g := loadGraph(t, w.dir)
	run, _ := g.Current()
	var targets []string
	for _, e := range run.Edges {
		if e.Type == rungraph.EdgeSession {
			targets = append(targets, e.Target+"="+e.To)
		}
	}
	if len(g.Runs) != 1 || strings.Join(targets, " ") != "primary=session:bb/thr_primary codex-main=session:bb/thr_codex-main" {
		t.Fatalf("runs=%d sessions=%v", len(g.Runs), targets)
	}
	last := g.Attempts[len(g.Attempts)-1]
	if last.Action != "handoff" || last.Target != "codex-main" || last.Run != run.Ref {
		t.Fatalf("%#v", last)
	}
}

func TestDryRunsAndIdleLoopsRecordNothing(t *testing.T) {
	w := newWorld(t, true, false)
	w.primary(decide.Observation{State: decide.Idle})
	o := w.opts(t0)
	o.DryRun = true
	if _, err := Tick(context.Background(), w.cfg, o); err != nil {
		t.Fatal(err)
	}
	disabled := newWorld(t, false, false)
	runTick(t, disabled, t0)
	held := newWorld(t, true, false)
	if _, err := control.Update(held.dir, func(f *control.File) { f.Paused = true }); err != nil {
		t.Fatal(err)
	}
	runTick(t, held, t0)
	for _, dir := range []string{w.dir, disabled.dir, held.dir} {
		if _, availability := rungraph.Load(dir); availability != rungraph.Missing {
			t.Fatalf("graph is %s", availability)
		}
	}
}

// An unusable or unwritable graph must leave every tick outcome identical
// apart from one warning, and must never be rewritten.
func TestUnusableGraphNeverChangesATick(t *testing.T) {
	type outcome struct {
		action, reason, state, target, control, check string
		sent                                          int
		failed                                        bool
	}
	var lastRunLog string
	run := func(t *testing.T, breakGraph func(dir string), setup func(*world, *fakeAdapter)) (outcome, []string) {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Idle})
		setup(w, p)
		breakGraph(w.dir)
		res, err := Tick(context.Background(), w.cfg, w.opts(t0))
		st, loadErr := state.Load(w.dir)
		if loadErr != nil || st.LastCheck == nil {
			t.Fatalf("%v %+v", loadErr, st)
		}
		ctl, _ := control.Load(w.dir)
		b, _ := os.ReadFile(filepath.Join(w.dir, state.LogName))
		lastRunLog = string(b)
		return outcome{res.Action, res.Reason, res.State, res.Target, ctl.Summary(), st.LastCheck.Action + "/" + st.LastCheck.Outcome, len(p.sent), err != nil}, res.Warnings
	}
	breaks := map[string]func(dir string){
		"directory in its place": func(dir string) { os.Mkdir(filepath.Join(dir, rungraph.FileName), 0o700) },
		"corrupt":                func(dir string) { os.WriteFile(filepath.Join(dir, rungraph.FileName), []byte("{"), 0o600) },
		"newer schema": func(dir string) {
			os.WriteFile(filepath.Join(dir, rungraph.FileName), []byte(`{"schemaVersion":99}`), 0o600)
		},
	}
	setups := map[string]func(*world, *fakeAdapter){
		"wake":       func(*world, *fakeAdapter) {},
		"send error": func(_ *world, p *fakeAdapter) { p.sendErr = errors.New("fictional send error") },
		"active":     func(_ *world, p *fakeAdapter) { p.obs = decide.Observation{State: decide.Active} },
		"completed":  func(w *world, _ *fakeAdapter) { control.Update(w.dir, func(f *control.File) { f.Completed = true }) },
		"stop":       func(w *world, _ *fakeAdapter) { control.Update(w.dir, func(f *control.File) { f.Stop = true }) },
		"await user": func(_ *world, p *fakeAdapter) { p.obs = decide.Observation{State: decide.Idle, AwaitingInput: true} },
	}
	for setupName, setup := range setups {
		want, warnings := run(t, func(string) {}, setup)
		if strings.Contains(strings.Join(warnings, ";"), graphWarning) {
			t.Fatalf("%s: healthy graph warned: %v", setupName, warnings)
		}
		for breakName, breakGraph := range breaks {
			t.Run(setupName+"/"+breakName, func(t *testing.T) {
				got, warnings := run(t, breakGraph, setup)
				if got != want {
					t.Fatalf("got %+v, want %+v", got, want)
				}
				if n := strings.Count(strings.Join(warnings, ";"), graphWarning); n != 1 {
					t.Fatalf("want one graph warning, got %d: %v", n, warnings)
				}
				if log := lastRunLog; strings.Count(log, graphWarning) != 1 {
					t.Fatalf("the warning is missing from runs.jsonl: %s", log)
				}
			})
		}
	}
	// The unusable record itself is left byte for byte.
	w := newWorld(t, true, false)
	w.primary(decide.Observation{State: decide.Idle})
	path := filepath.Join(w.dir, rungraph.FileName)
	os.WriteFile(path, []byte(`{"schemaVersion":99}`), 0o600)
	runTick(t, w, t0)
	if b, _ := os.ReadFile(path); string(b) != `{"schemaVersion":99}` {
		t.Fatalf("unsupported graph rewritten: %s", b)
	}
}

// A completion that is saved and recorded after the tick starts, but before
// the tick updates the graph, must not be read as a reopen.
func TestTickNeverAppliesControlOlderThanTheGraph(t *testing.T) {
	w := newWorld(t, true, false)
	w.primary(decide.Observation{State: decide.Idle})
	runTick(t, w, t0)
	run := loadGraph(t, w.dir).CurrentRun
	graphSyncHook = func() {
		graphSyncHook = nil
		f, err := control.Update(w.dir, func(f *control.File) { f.Completed = true })
		if err != nil {
			t.Error(err)
		}
		if err := rungraph.Update(w.dir, t0, func(g *rungraph.Graph) (bool, error) {
			return g.Sync(rungraph.Boundary{Gate: rungraph.GateOf(f), Operation: "complete", At: t0, NewRunID: "run_00000000000000000000000000000009"})
		}); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { graphSyncHook = nil })
	runTick(t, w, t0.Add(time.Hour))
	runTick(t, w, t0.Add(2*time.Hour))
	g := loadGraph(t, w.dir)
	if len(g.Runs) != 1 || g.CurrentRun != run || !g.Completed || g.Runs[0].ClosedSource != rungraph.SourceControl {
		t.Fatalf("a run was fabricated from stale control state: %#v", g.Runs)
	}
}

// A chat identifier the contract cannot store is a gap in the evidence, said
// once per run. It is not a failed write and must not warn on every check.
func TestUnrecordableSessionIdentifierIsNotedOnce(t *testing.T) {
	for _, thread := range []string{"team/thread-1", "_thread", "two words"} {
		t.Run(thread, func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.Agent.BB.Thread = thread
			p := w.primary(decide.Observation{State: decide.Idle})
			first := runTick(t, w, t0)
			if n := strings.Count(strings.Join(first.Warnings, ";"), "cannot be recorded"); n != 1 || strings.Contains(strings.Join(first.Warnings, ";"), graphWarning) {
				t.Fatalf("%v", first.Warnings)
			}
			for i := 1; i <= 3; i++ {
				if res := runTick(t, w, t0.Add(time.Duration(i)*2*time.Hour)); len(res.Warnings) != 0 {
					t.Fatalf("check %d warned again: %v", i, res.Warnings)
				}
			}
			g := loadGraph(t, w.dir)
			run, _ := g.Current()
			if len(g.Follow(run, rungraph.EdgeSession)) != 0 || len(run.Unrecorded) != 1 || run.Unrecorded[0].Target != state.Primary || run.Unrecorded[0].Reason != rungraph.ReasonSessionID {
				t.Fatalf("%#v", run)
			}
			b, _ := os.ReadFile(filepath.Join(w.dir, rungraph.FileName))
			if strings.Contains(string(b), thread) || len(p.sent) == 0 || len(g.Attempts) != len(p.sent) {
				t.Fatalf("rejected identifier stored, or sends not recorded: %s", b)
			}
		})
	}
}

// A headless runner has no configured chat: its reference is the resume ID it
// saved, read from disk without running the provider.
func TestHeadlessTargetRecordsItsSavedSession(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Agent = config.Agent{Adapter: "claude", Provider: "claude-code"}
	w.primary(decide.Observation{State: decide.Idle})
	runTick(t, w, t0)
	g := loadGraph(t, w.dir)
	run, _ := g.Current()
	if len(g.Follow(run, rungraph.EdgeSession)) != 0 || len(run.Unrecorded) != 0 {
		t.Fatalf("a session was recorded before the runner saved one: %#v", run)
	}
	const id = "11111111-2222-3333-4444-555555555555"
	if err := runner.Dir(filepath.Join(w.dir, "targets", state.Primary)).SaveSession(id); err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0.Add(2*time.Hour))
	g = loadGraph(t, w.dir)
	run, _ = g.Current()
	sessions := g.Follow(run, rungraph.EdgeSession)
	if len(sessions) != 1 || sessions[0].Ref != rungraph.SessionRef("claude", id) || sessions[0].Source != rungraph.SourceRunnerFile {
		t.Fatalf("%#v", sessions)
	}
}

// A scheduler identity supplied with a check is recorded as a trigger of the
// open run and changes nothing the check does.
func TestSuppliedRoutineIsRecordedAndNeverChangesTheCheck(t *testing.T) {
	routine := rd.RoutineRef{Source: "example-scheduler", Routine: "nightly", Invocation: "inv-1"}
	type outcome struct {
		action, reason, state string
		sent                  int
	}
	run := func(supplied rd.RoutineRef, enabled bool) (outcome, *world) {
		w := newWorld(t, enabled, false)
		p := w.primary(decide.Observation{State: decide.Idle})
		o := w.opts(t0)
		o.Routine = supplied
		res, err := Tick(context.Background(), w.cfg, o)
		if err != nil || len(res.Warnings) != 0 {
			t.Fatalf("%+v %v", res, err)
		}
		return outcome{res.Action, res.Reason, res.State, len(p.sent)}, w
	}
	plain, _ := run(rd.RoutineRef{}, true)
	with, w := run(routine, true)
	if plain != with {
		t.Fatalf("%+v vs %+v", plain, with)
	}
	g := loadGraph(t, w.dir)
	current, _ := g.Current()
	triggers := g.Follow(current, rungraph.EdgeInvocation)
	// The run was adopted by the check, not opened by the trigger.
	if current.Run.Origin != rd.Adopted || current.Run.Routine != nil || len(triggers) != 1 || triggers[0].Ref != rungraph.InvocationRef(routine) || triggers[0].Source != rungraph.SourceExternal {
		t.Fatalf("%#v %#v", current.Run, triggers)
	}
	// A trigger for a disabled loop opens nothing and records nothing.
	off, disabled := run(routine, false)
	if _, availability := rungraph.Load(disabled.dir); availability != rungraph.Missing || off.sent != 0 {
		t.Fatalf("%s %+v", availability, off)
	}
}
