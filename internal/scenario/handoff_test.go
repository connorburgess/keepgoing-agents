package scenario

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	altThread    = "thr_example_alt"
	altCandidate = "claude-other-account"
	altModel     = "example-alt-model"
	probeModel   = "example-probe-model"
	newCandidate = "claude-new-conversation"
	newModel     = "example-new-model"
)

// existingFallback is a BB fallback on another account, in a thread that
// already exists.
const existingFallback = `  - id: claude-other-account
    adapter: bb
    provider: claude-code
    group: claude-account-b
    bb: {thread: thr_example_alt, cli: "%[1]s/bb", model: example-alt-model, reasoning: medium}
    probe: {model: example-probe-model, cli: "%[1]s/claude"}
`

// newThreadFallback asks BB for a new conversation on another account.
const newThreadFallback = `  - id: claude-new-conversation
    adapter: bb
    provider: claude-code
    group: claude-account-c
    bb: {new_thread: true, cli: "%[1]s/bb", model: example-new-model}
    probe: {model: example-probe-model, cli: "%[1]s/claude"}
`

func fallbacksFile(candidates ...string) string {
	return `version: 1
redundancy: {min_healthy: 1, span: group}
probe: {every: 4h, ttl: 6h, timeout: 10s}
candidates:
` + strings.Join(candidates, "")
}

// limitLoop sets up a BB primary on account A that hands off on a rate
// limit and switches back after the reset.
func limitLoop(w *World, candidates ...string) {
	w.Thread(altThread)
	w.Init("handoff", "switch_back", "await_user", "degraded", "recovered", "complete")
	w.Fallbacks(fallbacksFile(candidates...), func(l map[string]any) {
		l["group"] = "claude-account-a"
		errs, _ := l["errors"].(map[string]any)
		errs["failover_on"] = []string{"rate_limit"}
		l["switch_back"] = map[string]any{"enabled": true, "min_after": "1h", "retry_every": "30m"}
		l["probe"] = map[string]any{"cli": w.BinPath("claude")}
	})
}

// TestScenarioLimitHandoffSwitchBack: a limit that resets soon is waited
// out; a limit that resets in five hours moves the loop to a confirmed
// fallback on another account; after the reset, when the fallback finishes a
// turn, the loop returns to its primary.
func TestScenarioLimitHandoffSwitchBack(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	limitLoop(w, existingFallback)
	primary, alt := w.Thread(primaryThread), w.Thread(altThread)
	w.Control("enable")

	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first wake", all(
		action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}),
		probedModels(s, probeModel), noReports(s), targetIs(s, "primary", primaryThread),
	))

	// A limit that resets in twenty minutes: wait, spend nothing.
	w.Advance(9 * time.Minute)
	primary.RateLimited(w.Now.Add(20 * time.Minute))
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "near reset is waited out", all(
		action(s, "none"), nothingSent(s), probedModels(s), noReports(s), holds(s), targetIs(s, "primary", primaryThread),
	))
	w.Advance(10 * time.Minute)
	s = w.Tick()
	w.Check(s, "still waiting", all(action(s, "none"), nothingSent(s), holds(s)))

	// At the reset BB retries the turn itself; keepgoing sent nothing.
	w.Advance(9 * time.Minute)
	primary.StartTurn()
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "working after the reset", all(action(s, "none"), nothingSent(s), noReports(s), targetIs(s, "primary", primaryThread)))

	// A limit that resets in five hours: hand off, after an exact probe.
	w.Advance(20 * time.Minute)
	reset := w.Now.Add(5 * time.Hour)
	primary.RateLimited(reset)
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "handoff", all(
		action(s, "handoff"), probedModels(s, altModel),
		ranBB(s, "thread", "update", altThread, "--json", "--model", altModel, "--reasoning-level", "medium"),
		sentExactly(s, Send{Kind: "tell", Thread: altThread, Mode: "auto"}),
		sentContains(s, "rate_limit"), reported(s, "keepgoing.handoff"), holds(s),
		targetIs(s, altCandidate, altThread),
	))

	// The fallback works and commits; the primary is left alone and not
	// probed. With the only fallback in use, redundancy is degraded.
	for i := 0; i < 3; i++ {
		w.Advance(time.Hour)
		w.Commit(fmt.Sprintf("feat: fictional fallback step %d", i+1))
		s = w.Tick()
		want := []string{}
		if i == 0 {
			want = []string{"keepgoing.degraded"}
		}
		w.Check(s, "fallback working", all(action(s, "none"), nothingSent(s), reported(s, want...), notProbed(s, cliDefault), targetIs(s, altCandidate, altThread)))
	}

	// Before the reset, a finished fallback turn is woken, not switched.
	w.Commit("feat: fictional work on the fallback")
	alt.FinishTurn()
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "no switch-back before the reset", all(
		action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: altThread, Mode: "auto"}), notProbed(s, cliDefault), targetIs(s, altCandidate, altThread),
	))

	// After the reset, the next finished turn returns the loop.
	w.At(reset.Add(10 * time.Minute))
	w.Commit("feat: more fictional work on the fallback")
	alt.FinishTurn()
	primary.FinishTurn() // BB's own retry after the reset finished the old turn.
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "switch-back", all(
		// The exact probe uses the primary's own model, which this BB
		// primary leaves to the CLI.
		action(s, "switch_back"), probedModels(s, cliDefault),
		sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}),
		reported(s, "keepgoing.switch_back"), holds(s), targetIs(s, "primary", primaryThread),
	))
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "redundancy recovered", all(action(s, "none"), nothingSent(s), reported(s, "keepgoing.recovered"), probedModels(s, probeModel)))

	w.Advance(10 * time.Minute)
	w.Commit("feat: fictional final step")
	primary.FinishTurn()
	s = w.AgentRuns("complete", "--note", "Fictional task verified")
	w.Check(s, "complete", all(exit(s, 0), holds(s, "completed"), reported(s, "keepgoing.complete")))
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "completed", all(action(s, "none"), nothingSent(s), noCalls(s)))
	w.Check(s, "the fallback received only the brief and one wake", journeySends(w, altThread, "tell", "tell"))
}

// TestScenarioNewConversationHandoff hands off to a new BB conversation and
// checks that exactly one is created per handoff, whatever bb answers, and
// that an outcome keepgoing cannot settle holds the loop for a person
// instead of starting a second conversation or trying another fallback.
func TestScenarioNewConversationHandoff(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		outcome string
		check   func(w *World, s *Step)
	}{
		{"created", "ok", settledOnNewThread},
		{"answer lost then found by title", "lost", settledOnNewThread},
		{"refused so the next fallback", "refuse", func(w *World, s *Step) {
			w.Check(s, "refused creation moves to the next fallback", all(
				action(s, "handoff"), spawns(w, 0), sentExactly(s, Send{Kind: "tell", Thread: altThread, Mode: "auto"}),
				reported(s, "keepgoing.handoff"), holds(s), targetIs(s, altCandidate, altThread), newThreads(s, 0),
				attemptRecorded(s, Attempt{Target: newCandidate, Action: "handoff", Outcome: "adapter_error"}),
			))
		}},
		{"not found so held for a person", "unlisted", func(w *World, s *Step) {
			w.Check(s, "unknown outcome holds", all(
				spawns(w, 1), sentTo(s, altThread), holds(s, "awaitingUser"), reported(s, "keepgoing.await_user"),
				targetIs(s, "primary", primaryThread), newThreads(s, 1),
			))
			// Held checks ask BB nothing and create nothing.
			for i := 0; i < 2; i++ {
				w.Advance(time.Hour)
				s = w.Tick()
				w.Check(s, "held", all(action(s, "none"), nothingSent(s), noCalls(s), spawns(w, 1)))
			}
			// resume looks again: still not listed, so it holds again.
			s = w.Control("resume")
			w.Check(s, "resume without the thread", all(nothingSent(s), spawns(w, 1)))
			w.Advance(time.Minute)
			s = w.Tick()
			w.Check(s, "still unsettled", all(nothingSent(s), spawns(w, 1), holds(s, "awaitingUser"), sentTo(s, altThread)))
			// A person restores the thread's title; resume settles it.
			w.ListUnderTitle("thr_example_new1", w.spawnTitle())
			s = w.Control("resume")
			w.Advance(time.Minute)
			s = w.Tick()
			w.Check(s, "settled after resume", all(spawns(w, 1), holds(s), targetIs(s, newCandidate, "thr_example_new1"), sentTo(s, altThread)))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := NewWorld(t, scenarioStart)
			limitLoop(w, newThreadFallback, existingFallback)
			primary := w.Thread(primaryThread)
			w.Control("enable")
			w.Advance(time.Minute)
			s := w.Tick()
			w.Check(s, "first wake", all(action(s, "wake"), probedModels(s, probeModel, probeModel)))
			w.Advance(10 * time.Minute)
			primary.RateLimited(w.Now.Add(5 * time.Hour))
			w.SpawnOutcomes(c.outcome)
			w.Advance(time.Minute)
			s = w.Tick()
			c.check(w, s)
			// Later checks never create a second conversation.
			for i := 0; i < 3; i++ {
				w.Advance(70 * time.Minute)
				s = w.Tick()
				w.Check(s, "no second conversation", spawns(w, map[bool]int{true: 0, false: 1}[c.outcome == "refuse"]))
			}
		})
	}
}

func settledOnNewThread(w *World, s *Step) {
	w.Check(s, "handoff to a new conversation", all(
		action(s, "handoff"), spawns(w, 1), probedModels(s, newModel), sentTo(s, altThread),
		reported(s, "keepgoing.handoff"), holds(s), targetIs(s, newCandidate, "thr_example_new1"), newThreads(s, 1),
	))
	if len(s.Sent) == 1 {
		m := s.Sent[0]
		w.Check(s, "new conversation request", all(
			check(m.Kind == "spawn", "kind %s", m.Kind),
			check(m.Mode == "accept-edits", "permission mode %q", m.Mode),
			check(strings.HasPrefix(m.Title, "keepgoing: example-loop (continued, "), "title %q", m.Title),
			check(strings.Contains(m.Message, "Continue from @thread:"+primaryThread+"."), "first prompt does not continue the primary"),
		))
	}
	if r := s.Reports; len(r) == 1 {
		data, _ := r[0].Body["data"].(map[string]any)
		w.Check(s, "handoff report marks a new thread without its ID", all(
			check(data["newThread"] == true, "newThread %v", data["newThread"]),
			check(!strings.Contains(fmt.Sprint(r[0].Body), "thr_example_new1"), "report names the new thread"),
		))
	}
	// The new conversation is supervised like any other.
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "new conversation working", all(action(s, "none"), nothingSent(s), targetIs(s, newCandidate, "thr_example_new1")))
}

// spawnTitle is the title of the first created conversation.
func (w *World) spawnTitle() string {
	for _, st := range w.Journal {
		for _, m := range st.Sent {
			if m.Kind == "spawn" {
				return m.Title
			}
		}
	}
	w.t.Fatal("no conversation was created")
	return ""
}

func check(ok bool, format string, args ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf(format, args...)
}

// spawns counts conversations created over the whole journey.
func spawns(w *World, want int) error {
	n := 0
	for _, st := range w.Journal {
		for _, m := range st.Sent {
			if m.Kind == "spawn" {
				n++
			}
		}
	}
	return check(n == want, "%d conversations created, want %d", n, want)
}

func journeySends(w *World, thread string, kinds ...string) error {
	var got []string
	for _, st := range w.Journal {
		for _, m := range st.Sent {
			if m.Thread == thread {
				got = append(got, m.Kind)
			}
		}
	}
	return check(fmt.Sprint(got) == fmt.Sprint(kinds), "%s received %v, want %v", thread, got, kinds)
}

// cliDefault stands for a probe that names no model.
const cliDefault = "(cli default)"

// probedModels compares the models probed in the step, in order.
func probedModels(s *Step, models ...string) error {
	var got []string
	for _, p := range s.Probes {
		got = append(got, modelOf(p))
	}
	return check(fmt.Sprint(got) == fmt.Sprint(models), "probed %v, want %v", got, models)
}

func notProbed(s *Step, model string) error {
	for _, p := range s.Probes {
		if modelOf(p) == model {
			return fmt.Errorf("probed %s", model)
		}
	}
	return nil
}

func modelOf(probe []string) string {
	if m := flagOf(probe, "--model"); m != "" {
		return m
	}
	return cliDefault
}

func flagOf(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func ranBB(s *Step, args ...string) error {
	for _, c := range s.Calls {
		if fmt.Sprint(c) == fmt.Sprint(args) {
			return nil
		}
	}
	return fmt.Errorf("did not run bb %v", args)
}

func targetIs(s *Step, target, thread string) error {
	return check(s.Status.Target == target && s.Status.TargetAgent.BBThread == thread,
		"status target %s (%s), want %s (%s)", s.Status.Target, s.Status.TargetAgent.BBThread, target, thread)
}

func newThreads(s *Step, created int) error {
	return check(s.Status.NewThreads.Created == created, "status newThreads.created %d, want %d", s.Status.NewThreads.Created, created)
}

func attemptRecorded(s *Step, want Attempt) error {
	for _, l := range s.Runs.Loops {
		if l.Current == nil {
			continue
		}
		for _, a := range l.Current.Attempts.Latest {
			if a == want {
				return nil
			}
		}
	}
	return fmt.Errorf("runs does not record %+v", want)
}
