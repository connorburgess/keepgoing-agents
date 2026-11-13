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
	crash bool
	// unlisted makes a created thread missing from the title listing, as when
	// it was renamed, retitled by BB, archived or is not visible yet.
	unlisted bool
	// extra, when set, is a second thread reported under every listed title.
	extra   string
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
	ids := f.titles[title]
	if f.extra != "" && len(ids) > 0 {
		ids = append(append([]string(nil), ids...), f.extra)
	}
	return ids, nil
}

func (f *fakeThreads) Spawn(_ context.Context, r bb.SpawnRequest) (string, error) {
	f.spawned = append(f.spawned, r)
	id := fmt.Sprintf("thr_created_%d", len(f.spawned))
	if (f.spawnErr == nil || f.createdAnyway) && !f.unlisted {
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
	if r.Project != "proj_example" || r.Environment != "env_example" || r.Provider != "claude-code" || r.Model != "example-model" || r.Reasoning != "high" || r.Permission != "accept-edits" ||
		!strings.HasPrefix(r.Title, "keepgoing: example-loop (continued, ") || !strings.HasSuffix(r.Title, ")") {
		t.Fatalf("request %+v", r)
	}
	if !strings.Contains(r.Prompt, `You are taking over the "example-loop" loop`) || !strings.Contains(r.Prompt, "Continue from @thread:thr_primary.") || !strings.Contains(r.Prompt, "keep going") ||
		!strings.Contains(r.Prompt, `Loop ownership: this conversation does not own the "example-loop" loop. Its owner is unchanged by this handoff. From here you may run keepgoing pause, stop, await-user and complete for this loop. Do not run keepgoing resume or enable, and do not use --take-over, unless a person asks you to.`) {
		t.Fatalf("first prompt: %q", r.Prompt)
	}
	st, _ := state.Load(w.dir)
	created := st.NewThreads["claude-other"]
	if st.Target() != "claude-other" || created.Thread != "thr_created_1" || created.Title != r.Title || created.From != state.Primary || created.Continues != "thr_primary" ||
		st.NewThreadsCreated != 1 || !st.LastNewThreadAt.Equal(t0) || st.NewThreadIntent != nil || st.Handoffs != 1 {
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
	// The attempt was counted with the intent, before bb was asked.
	if st.NewThreadIntent == nil || st.NewThreadIntent.Candidate != "claude-other" || st.NewThreadIntent.Title != f.spawned[0].Title || st.Target() != state.Primary || len(st.NewThreads) != 0 || st.NewThreadsCreated != 1 {
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

// bb gives no proof that nothing was created: it was killed, printed nothing
// usable, or returned a server error. The thread is looked for by its title
// straight away. Found, the handoff completes. Not found is not proof either
// (it may have been renamed, archived or not listed yet): the attempt stays
// counted, the loop is held for a person, the spawn is never retried and the
// loop is handed to no other fallback.
func TestUnprovenCreationIsFoundOrHeldNeverRetried(t *testing.T) {
	t.Run("the thread was created and is listed", func(t *testing.T) {
		w, _, f := newThreadWorld(t)
		f.spawnErr, f.createdAnyway = bb.ErrSpawnUncertain, true
		res := mustThreadTick(t, w, f, t0)
		st, _ := state.Load(w.dir)
		if res.Action != "handoff" || res.Target != "claude-other" || res.Handoff.NewThread != "thr_created_1" || len(f.spawned) != 1 || f.lists != 1 || st.NewThreadsCreated != 1 || st.NewThreadIntent != nil {
			t.Fatalf("%+v state=%+v", res, st)
		}
	})
	for name, setup := range map[string]func(*fakeThreads){
		"created but missing from the listing": func(f *fakeThreads) { f.createdAnyway, f.unlisted = true, true },
		"not created at all":                   func(f *fakeThreads) {},
		"BB cannot be asked":                   func(f *fakeThreads) { f.createdAnyway, f.listErr = true, errors.New("listing unavailable") },
	} {
		t.Run(name, func(t *testing.T) {
			w, p, f := newThreadWorld(t)
			w.cfg.Loop.Limits.MaxNewThreads = 1
			f.spawnErr = bb.ErrSpawnUncertain
			setup(f)
			res := mustThreadTick(t, w, f, t0)
			ctl, _ := control.Load(w.dir)
			st, _ := state.Load(w.dir)
			if res.Action != "await_user" || !ctl.AwaitingUser || !strings.Contains(ctl.Note, f.spawned[0].Title) || !strings.Contains(ctl.Note, "cannot tell whether it exists") ||
				strings.Count(w.reportTypes(), "await_user") != 1 {
				t.Fatalf("%+v hold=%+v reports=%s", res, ctl, w.reportTypes())
			}
			// Counted, still pending, and nothing else was tried.
			if st.NewThreadsCreated != 1 || st.NewThreadIntent == nil || st.Target() != state.Primary || sentTo(w, "codex-main") != 0 || len(p.sent) != 0 || exactProbes(w, "codex-main") != 0 {
				t.Fatalf("state=%+v codex sends=%d primary sends=%d", st, sentTo(w, "codex-main"), len(p.sent))
			}
			// A person resumes, again and again over hours: the thread is looked
			// for each time, never created again, and nothing is handed off.
			f.spawnErr = nil
			for i := 1; i <= 6; i++ {
				control.Update(w.dir, func(c *control.File) { c.AwaitingUser = false })
				if res := mustThreadTick(t, w, f, t0.Add(time.Duration(i)*2*time.Hour)); res.Action != "await_user" {
					t.Fatalf("resume %d: %+v", i, res)
				}
			}
			st, _ = state.Load(w.dir)
			if len(f.spawned) != 1 || st.NewThreadsCreated != 1 || sentTo(w, "codex-main") != 0 || st.Target() != state.Primary {
				t.Fatalf("spawns=%d count=%d codex sends=%d", len(f.spawned), st.NewThreadsCreated, sentTo(w, "codex-main"))
			}
		})
	}
	t.Run("held, then the thread shows up in the listing", func(t *testing.T) {
		w, _, f := newThreadWorld(t)
		f.spawnErr, f.createdAnyway, f.unlisted = bb.ErrSpawnUncertain, true, true
		mustThreadTick(t, w, f, t0)
		f.titles[f.spawned[0].Title] = []string{"thr_created_1"} // now visible
		control.Update(w.dir, func(c *control.File) { c.AwaitingUser = false })
		res := mustThreadTick(t, w, f, t0.Add(time.Minute))
		if res.Target != "claude-other" || !res.Handoff.Reconciled || res.Handoff.NewThread != "thr_created_1" || len(f.spawned) != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("two threads carry the title", func(t *testing.T) {
		w, _, f := newThreadWorld(t)
		f.spawnErr, f.createdAnyway = bb.ErrSpawnUncertain, true
		f.titles = map[string][]string{}
		// A second thread appears under the same title before the lookup.
		creator := f
		opts := w.opts(t0)
		opts.Threads = func(string) ThreadCreator { return doubledTitle{creator} }
		res, err := Tick(context.Background(), w.cfg, opts)
		st, _ := state.Load(w.dir)
		if err != nil || res.Action != "await_user" || st.Target() != state.Primary || len(st.NewThreads) != 0 || len(f.spawned) != 1 || st.NewThreadsCreated != 1 {
			t.Fatalf("%+v state=%+v err=%v", res, st, err)
		}
	})
	t.Run("the supervisor ended and the thread is not listed", func(t *testing.T) {
		w, _, f := newThreadWorld(t)
		f.crash, f.unlisted = true, true
		func() {
			defer func() { recover() }()
			threadTick(t, w, f, t0, nil)
		}()
		f.crash = false
		res := mustThreadTick(t, w, f, t0.Add(time.Minute))
		st, _ := state.Load(w.dir)
		if res.Action != "await_user" || len(f.spawned) != 1 || st.NewThreadsCreated != 1 || st.NewThreadIntent == nil || sentTo(w, "codex-main") != 0 {
			t.Fatalf("%+v state=%+v", res, st)
		}
	})
}

// doubledTitle reports one more thread under every title than exist.
type doubledTitle struct{ *fakeThreads }

func (d doubledTitle) FindByTitle(ctx context.Context, p bb.Place, title string) ([]string, error) {
	ids, err := d.fakeThreads.FindByTitle(ctx, p, title)
	return append(ids, "thr_unrelated_copy"), err
}

// The bb CLI reported rejecting the invocation and no such thread is listed.
// Together that is "nothing was created": it counts as a rejected brief for that
// fallback, under its own cap and spacing, the attempt is not counted against
// the thread cap, and the next fallback is tried.
func TestFailedCreationCountsAsARejectedBriefAndFallsThrough(t *testing.T) {
	w, p, f := newThreadWorld(t)
	f.spawnErr = bb.ErrSpawnRefused
	res := mustThreadTick(t, w, f, t0)
	st, _ := state.Load(w.dir)
	if res.Action != "handoff" || res.Target != "codex-main" || res.Handoff.NewThread != "" || st.HandoffBriefs != nil || st.NewThreadsCreated != 0 || !st.LastNewThreadAt.IsZero() || st.NewThreadIntent != nil || len(f.spawned) != 1 || f.lists != 1 {
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
	// The primary hits its limit again half an hour after the first thread was
	// created: too soon for another, so the ordinary fallback takes the loop.
	// (Here the first was created at t0; this check is well past the hour, so
	// spacing is exercised separately below.)
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
	if err := spendSends(w.dir, 1); err != nil {
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

// Two threads are never created less than an hour apart. Inside the hour the
// new-thread fallback is skipped unprobed and another fallback may take the
// loop; no hold is raised for spacing alone.
func TestNewThreadsAreSpacedAnHourApart(t *testing.T) {
	w, _, f := newThreadWorld(t)
	state.Save(w.dir, state.File{NewThreadsCreated: 1, LastNewThreadAt: t0.Add(-59 * time.Minute)})
	res := mustThreadTick(t, w, f, t0)
	if res.Target != "codex-main" || len(f.spawned) != 0 || exactProbes(w, "claude-other") != 0 || strings.Join(res.Handoff.Skipped, ",") != "claude-other: new_thread_spacing" {
		t.Fatalf("%+v handoff=%+v", res, res.Handoff)
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("spacing raised a hold")
	}
	w, _, f = newThreadWorld(t)
	state.Save(w.dir, state.File{NewThreadsCreated: 1, LastNewThreadAt: t0.Add(-time.Hour)})
	if res := mustThreadTick(t, w, f, t0); res.Target != "claude-other" || len(f.spawned) != 1 {
		t.Fatalf("an hour later: %+v", res)
	}
	// With no other fallback the loop falls through to its ordinary rules.
	w, p, f := newThreadWorld(t)
	delete(w.probeOK, "codex-main")
	w.cfg.FallbackSet.Redundancy.MinHealthy = 1
	state.Save(w.dir, state.File{NewThreadsCreated: 1, LastNewThreadAt: t0.Add(-time.Minute)})
	if res := mustThreadTick(t, w, f, t0); res.Action != "reprompt" || len(f.spawned) != 0 || len(p.sent) != 1 {
		t.Fatalf("no other fallback: %+v", res)
	}
}

// hungThreads is a bb whose thread commands each run until they are stopped.
// Each charges the fake wall clock the time its context allows.
type hungThreads struct {
	*fakeThreads
	o     *outage
	spent map[string]time.Duration
}

func (h hungThreads) hang(ctx context.Context, step string) {
	limit := 10 * time.Minute // a command with no limit would hold the check open
	if d, ok := ctx.Deadline(); ok {
		limit = time.Until(d).Round(time.Second) // the deadline is on the real clock; its length matters
	}
	h.o.clock = h.o.clock.Add(limit)
	h.spent[step] += limit
}

func (h hungThreads) PlaceOf(ctx context.Context, thread string) (bb.Place, error) {
	h.hang(ctx, "place")
	return h.fakeThreads.PlaceOf(ctx, thread)
}

func (h hungThreads) Spawn(ctx context.Context, r bb.SpawnRequest) (string, error) {
	h.hang(ctx, "spawn")
	return h.fakeThreads.Spawn(ctx, r)
}

func (h hungThreads) FindByTitle(ctx context.Context, p bb.Place, title string) ([]string, error) {
	h.hang(ctx, "find")
	return h.fakeThreads.FindByTitle(ctx, p, title)
}

// Creating a thread, and looking for one whose creation is unsettled, fit
// inside the check's hard limit with every other step at its maximum too:
// reading the place, the spawn and the lookup take the time reserved for a
// model switch and send, and the lookup at the start of a later check comes
// out of the observation's time.
func TestNewThreadStepsFitTheCheckTimeLimit(t *testing.T) {
	for _, found := range []bool{true, false} {
		t.Run(fmt.Sprintf("found=%t", found), func(t *testing.T) {
			o := newOutage(t, 2)
			o.cfg.Loop.TickTimeout = config.Duration{Duration: config.MinTickTimeout}
			o.cfg.FallbackSet.Probe.Timeout = config.Duration{Duration: 30 * time.Second}
			o.cfg.FallbackSet.Redundancy.MinHealthy = 1
			o.cfg.Loop.Progress.Repositories = []string{"/example/project"}
			o.cfg.Loop.Yield = &config.Yield{BusyCommand: []string{"x"}}
			o.cfg.FallbackSet.Candidates[0].BB = &config.BB{NewThread: true}
			o.cfg.Loop.Limits.MaxNewThreads = config.DefaultMaxNewThreads // as config.Load sets it
			o.probeUp["fallback-1"], o.probeUp["fallback-2"] = true, true
			f := &fakeThreads{t: t, place: bb.Place{Project: "proj_example", Environment: "env_example"}, titles: map[string][]string{},
				spawnErr: bb.ErrSpawnUncertain, createdAnyway: true, unlisted: !found}
			h := hungThreads{f, o, map[string]time.Duration{}}
			o.threads = h
			p := o.primary(decide.Observation{State: decide.Idle})
			limit := config.MinTickTimeout + config.TickTail
			// A quick first check confirms both fallbacks with scheduled probes.
			if r, _ := o.next(t, 1); r.Redundancy == nil || !r.Redundancy.Meets {
				t.Fatalf("first check: %+v", r)
			}
			// From here every step is slow. The observation leaves just enough
			// free time for the exact probe to start.
			p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
			o.yieldTime, o.observeTime, o.fingerprintTime = 10*time.Second, observeTimeout-minProbeTime, state.FingerprintTimeout
			r, took := o.next(t, 2)
			if len(f.spawned) != 1 || h.spent["place"] != newThreadPlaceTime || h.spent["spawn"] != newThreadSpawnTime || h.spent["find"] != newThreadFindTime ||
				newThreadPlaceTime+newThreadSpawnTime+newThreadFindTime > reserveSend {
				t.Fatalf("spawns=%d spent=%v result=%+v handoff=%+v took=%s", len(f.spawned), h.spent, r, r.Handoff, took)
			}
			if want := map[bool]string{true: "handoff", false: "await_user"}[found]; r.Action != want || took > limit {
				t.Fatalf("creating: %+v took %s of %s", r, took, limit)
			}
			if found {
				return
			}
			// Resumed while the thread still cannot be found: the lookup alone,
			// then the hold and its report.
			control.Update(o.dir, func(c *control.File) { c.AwaitingUser = false })
			if r, took := o.next(t, 3); r.Action != "await_user" || took > limit || len(f.spawned) != 1 {
				t.Fatalf("held again: %+v took %s", r, took)
			}
			// The thread shows up: the lookup, then a full observation of it
			// and a wake, all slow.
			f.titles[f.spawned[0].Title] = []string{"thr_created_1"}
			o.sendTime, o.activateTime, o.observeTime = 30*time.Second, 30*time.Second, observeTimeout-newThreadFindTime // the most it is now allowed
			control.Update(o.dir, func(c *control.File) { c.AwaitingUser = false })
			h.spent["find"] = 0
			r, took = o.next(t, 4)
			// Yield, lookup and observation together take what yield and
			// observation alone may: the lookup is not extra time.
			if r.Handoff == nil || !r.Handoff.Reconciled || took > limit || h.spent["find"] != newThreadFindTime || o.observeLimit != observeTimeout-newThreadFindTime {
				t.Fatalf("reconciled: %+v took %s of %s (find %s, observation %s)", r, took, limit, h.spent["find"], o.observeLimit)
			}
		})
	}
}

// A refusal is taken at its word only when the listing agrees. A thread that
// exists despite it is adopted and counted; a listing that cannot be trusted
// leaves the outcome unknown, so the attempt stays counted and the loop is
// held. Nothing is refunded in either case.
func TestRefusalIsCheckedAgainstTheListingBeforeTheRefund(t *testing.T) {
	w, _, f := newThreadWorld(t)
	f.spawnErr, f.createdAnyway = bb.ErrSpawnRefused, true
	res := mustThreadTick(t, w, f, t0)
	st, _ := state.Load(w.dir)
	if res.Action != "handoff" || res.Target != "claude-other" || res.Handoff.NewThread != "thr_created_1" || st.NewThreadsCreated != 1 || !st.LastNewThreadAt.Equal(t0) || sentTo(w, "codex-main") != 0 {
		t.Fatalf("created despite the refusal: %+v state=%+v", res, st)
	}
	for name, setup := range map[string]func(*fakeThreads){
		"BB cannot be asked": func(f *fakeThreads) { f.listErr = errors.New("listing unavailable") },
		"two threads carry the title": func(f *fakeThreads) {
			f.createdAnyway = true
			f.extra = "thr_unrelated_copy"
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, _, f := newThreadWorld(t)
			f.spawnErr = bb.ErrSpawnRefused
			setup(f)
			res := mustThreadTick(t, w, f, t0)
			st, _ := state.Load(w.dir)
			if res.Action != "await_user" || st.NewThreadsCreated != 1 || st.NewThreadIntent == nil || len(f.spawned) != 1 || sentTo(w, "codex-main") != 0 || st.Target() != state.Primary {
				t.Fatalf("%+v state=%+v", res, st)
			}
		})
	}
}

// A bb CLI that could not be started is held like any unknown outcome, and
// the hold says what happened instead of that a thread may exist.
func TestMissingBBHoldsWithItsOwnMessage(t *testing.T) {
	w, _, f := newThreadWorld(t)
	f.spawnErr, f.listErr = bb.ErrSpawnNotStarted, errors.New("bb could not be started")
	res := mustThreadTick(t, w, f, t0)
	st, _ := state.Load(w.dir)
	ctl, _ := control.Load(w.dir)
	if res.Action != "await_user" || st.NewThreadsCreated != 1 || st.NewThreadIntent == nil || !st.NewThreadIntent.NotStarted || sentTo(w, "codex-main") != 0 {
		t.Fatalf("%+v state=%+v", res, st)
	}
	for _, want := range []string{"could not start the bb CLI", "most likely no thread was created", "bb.cli", "--discard-pending-thread", f.spawned[0].Title} {
		if !strings.Contains(ctl.Note, want) {
			t.Errorf("hold note missing %q: %s", want, ctl.Note)
		}
	}
	if strings.Contains(ctl.Note, "cannot tell whether it exists") {
		t.Errorf("hold note: %s", ctl.Note)
	}
	// bb is repaired and lists no such thread: still no proof, still held,
	// with the same explanation, and nothing is created.
	f.spawnErr, f.listErr = nil, nil
	control.Update(w.dir, func(c *control.File) { c.AwaitingUser = false })
	res = mustThreadTick(t, w, f, t0.Add(2*time.Hour))
	if ctl, _ := control.Load(w.dir); res.Action != "await_user" || len(f.spawned) != 1 || !strings.Contains(ctl.Note, "could not start the bb CLI") {
		t.Fatalf("%+v note=%s", res, ctl.Note)
	}
}
