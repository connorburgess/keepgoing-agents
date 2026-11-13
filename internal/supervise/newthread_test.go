package supervise

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/bb"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// fakeThreads stands in for BB's thread creation and listing. Every call is
// counted, so a test can show that nothing was asked of BB at all.
type fakeThreads struct {
	t        *testing.T
	place    bb.Place
	placeErr error
	listErr  error
	spawnErr error
	// createdAnyway makes a failing spawn still create the thread, as when bb
	// is killed after BB accepted the request.
	createdAnyway bool
	// crash makes Spawn panic after the thread exists, standing in for the
	// supervisor process ending before it could record anything.
	crash   bool
	titles  map[string][]string
	spawned []bb.SpawnRequest
	places  []string
	lists   int
}

func (f *fakeThreads) PlaceOf(_ context.Context, thread string) (bb.Place, error) {
	f.places = append(f.places, thread)
	return f.place, f.placeErr
}

func (f *fakeThreads) FindByTitle(_ context.Context, p bb.Place, title string) ([]string, error) {
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.titles[title], nil
}

func (f *fakeThreads) Spawn(_ context.Context, r bb.SpawnRequest) (string, error) {
	f.spawned = append(f.spawned, r)
	id := fmt.Sprintf("thr_created_%d", len(f.spawned))
	if f.spawnErr == nil || f.createdAnyway {
		f.titles[r.Title] = append(f.titles[r.Title], id)
	}
	if f.crash {
		panic("simulated process exit after bb created the thread")
	}
	if f.spawnErr != nil {
		return "", f.spawnErr
	}
	return id, nil
}

// newThreadWorld is a rate-limited BB primary whose first eligible fallback,
// claude-other, starts a new thread at handoff. codex-main is an ordinary
// fallback on an existing thread.
func newThreadWorld(t *testing.T) (*world, *fakeAdapter, *fakeThreads) {
	t.Helper()
	w := newWorld(t, true, true)
	w.cfg.Loop.Limits.MaxNewThreads = 3
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	for i := range w.cfg.FallbackSet.Candidates {
		if c := &w.cfg.FallbackSet.Candidates[i]; c.ID == "claude-other" {
			c.BB = &config.BB{NewThread: true, Model: "example-model", Reasoning: "high"}
		}
	}
	return w, w.primary(limited), &fakeThreads{t: t, place: bb.Place{Project: "proj_example", Environment: "env_example"}, titles: map[string][]string{}}
}

// sentTo counts messages sent to a target, zero when it was never built.
func sentTo(w *world, target string) int {
	if a := w.adapters[target]; a != nil {
		return len(a.sent)
	}
	return 0
}

// threadTick runs one check with the fake thread creator and records which
// agent each adapter was built for.
func threadTick(t *testing.T, w *world, f *fakeThreads, at time.Time, built map[string]string) (Result, error) {
	t.Helper()
	opts := w.opts(at)
	opts.Threads = func(string) ThreadCreator { return f }
	build := opts.NewAdapter
	opts.NewAdapter = func(a config.Agent, dir, target string) (adapter.Adapter, error) {
		if built != nil && a.BB != nil {
			built[target] = a.BB.Thread
		}
		return build(a, dir, target)
	}
	return Tick(context.Background(), w.cfg, opts)
}

func mustThreadTick(t *testing.T, w *world, f *fakeThreads, at time.Time) Result {
	t.Helper()
	res, err := threadTick(t, w, f, at, nil)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return res
}

// One handoff creates one thread: with the brief as its first prompt, in the
// place of the conversation it continues, titled so a person can tell
// keepgoing started it. Later checks supervise it and create nothing more,
// and the conversation that was left is not touched.
func TestHandoffToANewThreadCreatesItOnce(t *testing.T) {
	w, p, f := newThreadWorld(t)
	built := map[string]string{}
	res, err := threadTick(t, w, f, t0, built)
	if err != nil || res.Action != "handoff" || res.Target != "claude-other" || res.Handoff.NewThread != "thr_created_1" || res.Handoff.Continues != "thr_primary" || res.Handoff.Reconciled {
		t.Fatalf("%+v handoff=%+v err=%v", res, res.Handoff, err)
	}
	if len(f.spawned) != 1 || len(f.places) != 1 || f.places[0] != "thr_primary" {
		t.Fatalf("spawns=%d place lookups=%v", len(f.spawned), f.places)
	}
	r := f.spawned[0]
	if r.Project != "proj_example" || r.Environment != "env_example" || r.Provider != "claude-code" || r.Model != "example-model" || r.Reasoning != "high" ||
		!strings.HasPrefix(r.Title, "keepgoing: example-loop (continued, ") || !strings.HasSuffix(r.Title, ")") {
		t.Fatalf("request %+v", r)
	}
	if !strings.Contains(r.Prompt, `You are taking over the "example-loop" loop`) || !strings.Contains(r.Prompt, "This conversation continues @thread:thr_primary.") || !strings.Contains(r.Prompt, "keep going") {
		t.Fatalf("first prompt: %q", r.Prompt)
	}
	st, _ := state.Load(w.dir)
	created := st.NewThreads["claude-other"]
	if st.Target() != "claude-other" || created.Thread != "thr_created_1" || created.Title != r.Title || created.From != state.Primary || created.Continues != "thr_primary" ||
		st.NewThreadsCreated != 1 || st.NewThreadIntent != nil || st.Handoffs != 1 {
		t.Fatalf("state %+v", st)
	}
	// The conversation that was left: nothing sent, nothing stopped.
	if len(p.sent) != 0 || p.stopped != 0 {
		t.Fatalf("the old conversation was touched: sent=%d stopped=%d", len(p.sent), p.stopped)
	}
	var handoffs int
	for _, e := range w.reports {
		if e.Type == "handoff" {
			handoffs++
			if e.Data["newThread"] != true || e.Data["to"] != "claude-other" || strings.Contains(e.Text, "thr_created") {
				t.Fatalf("report %+v", e)
			}
		}
	}
	if handoffs != 1 {
		t.Fatalf("%d handoff reports", handoffs)
	}
	// Later checks supervise the created thread and create nothing.
	for i := 1; i <= 5; i++ {
		if res, err := threadTick(t, w, f, t0.Add(time.Duration(i)*time.Minute), built); err != nil || res.Target != "claude-other" || res.Action != "wake" {
			t.Fatalf("check %d: %+v %v", i, res, err)
		}
	}
	if len(f.spawned) != 1 || f.lists != 0 || built["claude-other"] != "thr_created_1" || sentTo(w, "claude-other") != 5 {
		t.Fatalf("spawns=%d listings=%d adapter thread=%q sends=%d", len(f.spawned), f.lists, built["claude-other"], sentTo(w, "claude-other"))
	}
}

// The supervisor ends after BB created the thread and before anything was
// recorded. The intent was on disk first, so the next check finds the thread
// by its title, completes the handoff, and creates nothing.
func TestCrashAfterCreationIsReconciledWithoutASecondThread(t *testing.T) {
	w, _, f := newThreadWorld(t)
	f.crash = true
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the simulated exit")
			}
		}()
		threadTick(t, w, f, t0, nil)
	}()
	st, _ := state.Load(w.dir)
	if st.NewThreadIntent == nil || st.NewThreadIntent.Candidate != "claude-other" || st.NewThreadIntent.Title != f.spawned[0].Title || st.Target() != state.Primary || len(st.NewThreads) != 0 {
		t.Fatalf("after the crash: %+v", st)
	}
	f.crash = false
	res := mustThreadTick(t, w, f, t0.Add(time.Minute))
	st, _ = state.Load(w.dir)
	if len(f.spawned) != 1 || f.lists != 1 || res.Target != "claude-other" || res.Handoff == nil || !res.Handoff.Reconciled || res.Handoff.NewThread != "thr_created_1" ||
		st.Target() != "claude-other" || st.NewThreads["claude-other"].Thread != "thr_created_1" || st.NewThreadsCreated != 1 || st.NewThreadIntent != nil {
		t.Fatalf("spawns=%d listings=%d result=%+v state=%+v", len(f.spawned), f.lists, res, st)
	}
	// Restarts after that create nothing either.
	for i := 2; i < 6; i++ {
		mustThreadTick(t, w, f, t0.Add(time.Duration(i)*time.Minute))
	}
	if len(f.spawned) != 1 {
		t.Fatalf("%d threads created", len(f.spawned))
	}
}

// bb gives no clear answer. Nothing else is tried until the outcome is known:
// creating again, or handing the loop to another fallback, could put two
// conversations on the task.
func TestUncertainCreationIsSettledBeforeAnythingElseIsTried(t *testing.T) {
	t.Run("the thread was created", func(t *testing.T) {
		w, p, f := newThreadWorld(t)
		f.spawnErr, f.createdAnyway = bb.ErrSpawnUncertain, true
		res := mustThreadTick(t, w, f, t0)
		st, _ := state.Load(w.dir)
		if res.Action != "none" || !strings.Contains(res.Reason, "a new thread may have been created") || st.NewThreadIntent == nil || st.Target() != state.Primary ||
			sentTo(w, "codex-main") != 0 || len(p.sent) != 0 || exactProbes(w, "codex-main") != 0 {
			t.Fatalf("%+v state=%+v", res, st)
		}
		res = mustThreadTick(t, w, f, t0.Add(time.Minute))
		if len(f.spawned) != 1 || res.Target != "claude-other" || !res.Handoff.Reconciled {
			t.Fatalf("spawns=%d %+v", len(f.spawned), res)
		}
	})
	t.Run("nothing was created", func(t *testing.T) {
		w, _, f := newThreadWorld(t)
		f.spawnErr = bb.ErrSpawnUncertain
		mustThreadTick(t, w, f, t0)
		f.spawnErr = nil
		// The listing shows no such thread: the intent is dropped and the
		// handoff goes on. The candidate is backing off after its failed
		// attempt, so the next fallback takes the loop.
		res := mustThreadTick(t, w, f, t0.Add(time.Minute))
		st, _ := state.Load(w.dir)
		if st.NewThreadIntent != nil || res.Action != "handoff" || res.Target != "codex-main" || len(f.spawned) != 1 || st.NewThreadsCreated != 0 {
			t.Fatalf("%+v state=%+v spawns=%d", res, st, len(f.spawned))
		}
	})
	t.Run("BB cannot be asked", func(t *testing.T) {
		w, p, f := newThreadWorld(t) // max_reprompts 2
		f.spawnErr, f.createdAnyway = bb.ErrSpawnUncertain, true
		mustThreadTick(t, w, f, t0)
		f.listErr = errors.New("listing unavailable")
		res := mustThreadTick(t, w, f, t0.Add(time.Minute))
		if res.Action != "none" || sentTo(w, "codex-main") != 0 || len(p.sent) != 0 {
			t.Fatalf("something was tried while the creation was unsettled: %+v", res)
		}
		// Not a wait to sit in silently: the second such check asks a person.
		res = mustThreadTick(t, w, f, t0.Add(2*time.Minute))
		ctl, _ := control.Load(w.dir)
		if res.Action != "await_user" || !ctl.AwaitingUser || !strings.Contains(ctl.Note, f.spawned[0].Title) || strings.Count(w.reportTypes(), "await_user") != 1 {
			t.Fatalf("%+v hold=%+v", res, ctl)
		}
		if len(f.spawned) != 1 || sentTo(w, "codex-main") != 0 {
			t.Fatalf("spawns=%d", len(f.spawned))
		}
		// BB answers again and the person resumes: the thread is found.
		f.listErr = nil
		control.Update(w.dir, func(c *control.File) { c.AwaitingUser = false })
		if res := mustThreadTick(t, w, f, t0.Add(3*time.Minute)); res.Target != "claude-other" || len(f.spawned) != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("two threads carry the title", func(t *testing.T) {
		w, _, f := newThreadWorld(t)
		f.spawnErr, f.createdAnyway = bb.ErrSpawnUncertain, true
		mustThreadTick(t, w, f, t0)
		f.titles[f.spawned[0].Title] = append(f.titles[f.spawned[0].Title], "thr_unrelated_copy")
		mustThreadTick(t, w, f, t0.Add(time.Minute))
		res := mustThreadTick(t, w, f, t0.Add(2*time.Minute))
		st, _ := state.Load(w.dir)
		if res.Action != "await_user" || st.Target() != state.Primary || len(st.NewThreads) != 0 || len(f.spawned) != 1 {
			t.Fatalf("%+v state=%+v", res, st)
		}
	})
}

// bb refuses to create the thread. That counts as a rejected brief for that
// fallback, under its own cap and spacing, and the next fallback is tried.
func TestFailedCreationCountsAsARejectedBriefAndFallsThrough(t *testing.T) {
	w, p, f := newThreadWorld(t)
	f.spawnErr = bb.ErrSpawnRefused
	res := mustThreadTick(t, w, f, t0)
	st, _ := state.Load(w.dir)
	if res.Action != "handoff" || res.Target != "codex-main" || res.Handoff.NewThread != "" || st.HandoffBriefs != nil || st.NewThreadsCreated != 0 || st.NewThreadIntent != nil || len(f.spawned) != 1 {
		t.Fatalf("%+v state=%+v", res, st)
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), "new thread not created") || sentTo(w, "codex-main") != 1 || len(p.sent) != 0 {
		t.Fatalf("warnings %v", res.Warnings)
	}

	// With no other fallback: two attempts a backoff apart, then that fallback
	// is capped and the loop falls through as it does for rejected briefs.
	w, p, f = newThreadWorld(t)
	f.spawnErr = bb.ErrSpawnRefused
	delete(w.probeOK, "codex-main")
	w.cfg.FallbackSet.Redundancy.MinHealthy = 1
	obs := limited
	obs.ResetsAt = t0.Add(5 * time.Hour)
	p.obs = obs
	var last Result
	for minute := 0; minute < 30; minute++ {
		last = mustThreadTick(t, w, f, t0.Add(time.Duration(minute)*time.Minute))
	}
	st, _ = state.Load(w.dir)
	if len(f.spawned) != 2 || st.HandoffBriefs["claude-other"].Failed != 2 || exactProbes(w, "claude-other") != 2 || !strings.Contains(last.Reason, "every confirmed fallback is capped") || st.NewThreadsCreated != 0 {
		t.Fatalf("spawns=%d briefs=%+v last=%+v", len(f.spawned), st.HandoffBriefs, last)
	}

	// The place of the conversation being continued cannot be read.
	w, _, f = newThreadWorld(t)
	f.placeErr = errors.New("unavailable")
	if res := mustThreadTick(t, w, f, t0); res.Target != "codex-main" || len(f.spawned) != 0 {
		t.Fatalf("%+v", res)
	}
	// A configured project and environment are used as they are.
	w, _, f = newThreadWorld(t)
	for i := range w.cfg.FallbackSet.Candidates {
		if c := &w.cfg.FallbackSet.Candidates[i]; c.ID == "claude-other" {
			c.BB.Project, c.BB.Environment = "proj_configured", "env_configured"
		}
	}
	if res := mustThreadTick(t, w, f, t0); res.Target != "claude-other" || len(f.places) != 0 || f.spawned[0].Project != "proj_configured" || f.spawned[0].Environment != "env_configured" {
		t.Fatalf("%+v places=%v", res, f.places)
	}
}

// A loop creates at most loop.limits.max_new_threads threads. At the cap a
// new-thread fallback is skipped unprobed; another fallback may still take
// the loop; with none, a person is asked.
func TestNewThreadCap(t *testing.T) {
	t.Run("another fallback takes over", func(t *testing.T) {
		w, _, f := newThreadWorld(t)
		w.cfg.Loop.Limits.MaxNewThreads = 1
		state.Save(w.dir, state.File{NewThreadsCreated: 1})
		res := mustThreadTick(t, w, f, t0)
		if res.Target != "codex-main" || len(f.spawned) != 0 || exactProbes(w, "claude-other") != 0 || strings.Join(res.Handoff.Skipped, ",") != "claude-other: new_thread_cap" {
			t.Fatalf("%+v handoff=%+v", res, res.Handoff)
		}
		if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
			t.Fatal("held although another fallback took the loop")
		}
	})
	t.Run("no other fallback: a person is asked", func(t *testing.T) {
		w, p, f := newThreadWorld(t)
		w.cfg.Loop.Limits.MaxNewThreads = 1
		delete(w.probeOK, "codex-main")
		w.cfg.FallbackSet.Redundancy.MinHealthy = 1
		state.Save(w.dir, state.File{NewThreadsCreated: 1})
		res := mustThreadTick(t, w, f, t0)
		ctl, _ := control.Load(w.dir)
		if res.Action != "await_user" || res.Reason != decide.NewThreadCapReason || !ctl.AwaitingUser || len(f.spawned) != 0 || len(p.sent) != 0 || exactProbes(w, "claude-other") != 0 {
			t.Fatalf("%+v hold=%+v", res, ctl)
		}
		// Resume alone holds again; raising the cap and resuming creates the thread.
		control.Update(w.dir, func(c *control.File) { c.AwaitingUser = false })
		if res := mustThreadTick(t, w, f, t0.Add(time.Minute)); res.Action != "await_user" || len(f.spawned) != 0 {
			t.Fatalf("resume alone: %+v", res)
		}
		w.cfg.Loop.Limits.MaxNewThreads = 2
		control.Update(w.dir, func(c *control.File) { c.AwaitingUser = false })
		if res := mustThreadTick(t, w, f, t0.Add(2*time.Minute)); res.Target != "claude-other" || len(f.spawned) != 1 {
			t.Fatalf("after raising the cap: %+v", res)
		}
		if st, _ := state.Load(w.dir); st.NewThreadsCreated != 2 {
			t.Fatalf("count %d", st.NewThreadsCreated)
		}
	})
}

// Stop and pause win. A held loop asks BB nothing, creates nothing and does
// not settle a pending creation; stop still reaches a running turn.
func TestStopAndPauseWinOverThreadCreation(t *testing.T) {
	for _, hold := range []string{"pause", "stop", "disabled", "awaiting user", "completed"} {
		for _, pending := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s pending=%t", hold, pending), func(t *testing.T) {
				w, p, f := newThreadWorld(t)
				if pending {
					state.Save(w.dir, state.File{NewThreadIntent: &state.NewThreadIntent{Candidate: "claude-other", Title: "keepgoing: example-loop (continued, 00000000)", Project: "proj_example", Environment: "env_example", From: state.Primary, RequestedAt: t0}})
					f.titles["keepgoing: example-loop (continued, 00000000)"] = []string{"thr_created_earlier"}
				}
				control.Update(w.dir, func(c *control.File) {
					c.Paused, c.Stop, c.AwaitingUser, c.Completed = hold == "pause", hold == "stop", hold == "awaiting user", hold == "completed"
					c.Enabled = hold != "disabled"
				})
				for i := 0; i < 3; i++ {
					if res := mustThreadTick(t, w, f, t0.Add(time.Duration(i)*time.Minute)); res.Action != "none" || res.Target != state.Primary {
						t.Fatalf("%+v", res)
					}
				}
				st, _ := state.Load(w.dir)
				if len(f.spawned) != 0 || f.lists != 0 || len(f.places) != 0 || len(st.NewThreads) != 0 || (st.NewThreadIntent != nil) != pending || len(w.probes) != 0 {
					t.Fatalf("BB was asked under %s: spawns=%d listings=%d places=%d probes=%d state=%+v", hold, len(f.spawned), f.lists, len(f.places), len(w.probes), st)
				}
				if hold == "stop" {
					p.obs = decide.Observation{State: decide.Active}
					if res := mustThreadTick(t, w, f, t0.Add(time.Hour)); res.Action != "stop" || p.stopped != 1 || len(f.spawned) != 0 {
						t.Fatalf("%+v", res)
					}
				}
			})
		}
	}
}

// Switch-back to the primary works from a created thread, and a later handoff
// creates another thread for the same candidate, counted against the cap.
func TestSwitchBackFromACreatedThreadAndASecondHandoff(t *testing.T) {
	w, p, f := newThreadWorld(t)
	w.cfg.Loop.SwitchBack = config.SwitchBack{Enabled: true, MinAfter: config.Duration{Duration: time.Hour}, RetryEvery: config.Duration{Duration: 30 * time.Minute}}
	w.cfg.Loop.Probe = config.Probe{Kind: "command", Command: []string{"x"}}
	obs := limited
	obs.ResetsAt = t0.Add(2 * time.Hour)
	p.obs = obs
	if res := mustThreadTick(t, w, f, t0); res.Target != "claude-other" {
		t.Fatalf("%+v", res)
	}
	// The handoff built no adapter for the new thread: the brief was its first prompt.
	created := &fakeAdapter{obs: decide.Observation{State: decide.Idle}}
	w.adapters["claude-other"] = created
	if res := mustThreadTick(t, w, f, t0.Add(time.Hour)); res.Action != "wake" || res.Target != "claude-other" {
		t.Fatalf("before the reset: %+v", res)
	}
	w.probeOK["primary"] = true
	p.obs = decide.Observation{State: decide.Idle}
	res := mustThreadTick(t, w, f, t0.Add(2*time.Hour+time.Minute))
	st, _ := state.Load(w.dir)
	if res.Action != "switch_back" || res.Target != state.Primary || st.Target() != state.Primary || len(p.sent) != 1 || created.stopped != 0 {
		t.Fatalf("%+v state=%+v", res, st)
	}
	// The created thread is left as it was and stays on record.
	if st.NewThreads["claude-other"].Thread != "thr_created_1" || len(f.spawned) != 1 {
		t.Fatalf("%+v", st.NewThreads)
	}
	// The primary hits its limit again: a second thread for the same candidate.
	p.obs = limited
	res = mustThreadTick(t, w, f, t0.Add(5*time.Hour))
	st, _ = state.Load(w.dir)
	if res.Target != "claude-other" || res.Handoff.NewThread != "thr_created_2" || st.NewThreads["claude-other"].Thread != "thr_created_2" || st.NewThreadsCreated != 2 || f.spawned[0].Title == f.spawned[1].Title {
		t.Fatalf("%+v state=%+v", res, st)
	}
}

// The move is recorded with both ends: the run graph links the run to the
// conversation that was left and to the created thread, marked as created,
// and the event log's check record says a thread was created.
func TestNewThreadIsRecordedInTheRunGraphAndEventLog(t *testing.T) {
	w, _, f := newThreadWorld(t)
	mustThreadTick(t, w, f, t0)
	mustThreadTick(t, w, f, t0.Add(time.Minute))
	g := loadGraph(t, w.dir)
	sessions := map[string]rungraph.Source{}
	for _, l := range g.Links {
		if l.Kind == rd.SessionLink && l.Run == g.CurrentRun {
			sessions[l.Target+" "+l.Session] = l.Source
		}
	}
	if sessions["primary thr_primary"] != rungraph.SourceConfigured || sessions["claude-other thr_created_1"] != rungraph.SourceCreated || len(sessions) != 2 {
		t.Fatalf("session links: %v", sessions)
	}
	j, err := incident.Load(w.dir)
	if err != nil || len(j.Records) < 2 {
		t.Fatalf("%v %d", err, len(j.Records))
	}
	move := j.Records[0]
	if move.Action != "handoff" || !move.NewThread || move.TargetKey == move.TargetKeyAfter || j.Records[1].NewThread {
		t.Fatalf("event log: %+v then %+v", move, j.Records[1])
	}
}

// Nothing changes for a configuration that does not opt in: BB is never asked
// to create or list a thread, and the handoff goes to the existing thread.
func TestNoThreadIsCreatedWithoutOptIn(t *testing.T) {
	w := newWorld(t, true, true)
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	w.primary(limited)
	f := &fakeThreads{t: t, titles: map[string][]string{}}
	for i := 0; i < 5; i++ {
		mustThreadTick(t, w, f, t0.Add(time.Duration(i)*time.Minute))
	}
	st, _ := state.Load(w.dir)
	if len(f.spawned) != 0 || f.lists != 0 || len(f.places) != 0 || st.Target() != "claude-other" || len(st.NewThreads) != 0 || st.NewThreadsCreated != 0 || sentTo(w, "claude-other") == 0 {
		t.Fatalf("spawns=%d listings=%d places=%d state=%+v", len(f.spawned), f.lists, len(f.places), st)
	}
}

// The creation is one send against loop.limits.max_sends, a dry run creates
// and records nothing, and a new-thread fallback with no thread on record is
// not treated as a target.
func TestNewThreadSendAllowanceDryRunAndMissingRecord(t *testing.T) {
	w, _, f := newThreadWorld(t)
	w.cfg.Loop.Limits.MaxSends = 5
	// A dry run runs no probes, so the fallbacks' health is recorded first.
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	opts := w.opts(t0)
	opts.DryRun, opts.Threads = true, func(string) ThreadCreator { return f }
	res, err := Tick(context.Background(), w.cfg, opts)
	st, _ := state.Load(w.dir)
	if err != nil || res.Action != "handoff" || res.Handoff.To != "claude-other" || !res.Handoff.WouldCreateThread || len(f.spawned) != 0 || len(f.places) != 0 || st.NewThreadIntent != nil {
		t.Fatalf("dry run: %+v handoff=%+v err=%v", res, res.Handoff, err)
	}
	mustThreadTick(t, w, f, t0)
	sendsUsed(t, w, 1)
	// The send limit is reached before creation: nothing is asked of BB.
	w, _, f = newThreadWorld(t)
	w.cfg.Loop.Limits.MaxSends = 1
	if err := state.ReserveSend(w.dir, 1); err != nil {
		t.Fatal(err)
	}
	if res := mustThreadTick(t, w, f, t0); res.Action != "await_user" || len(f.spawned) != 0 {
		t.Fatalf("%+v", res)
	}
	// The active target is a new-thread fallback but no thread is on record
	// (state was lost): the loop returns to its primary and says so.
	w, _, f = newThreadWorld(t)
	state.Save(w.dir, state.File{ActiveTarget: "claude-other"})
	w.primary(decide.Observation{State: decide.Idle})
	res = mustThreadTick(t, w, f, t0)
	if res.Target != state.Primary || !strings.Contains(strings.Join(res.Warnings, ";"), "no created thread on record") {
		t.Fatalf("%+v", res)
	}
}
