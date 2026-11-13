package supervise

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/handoff"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// childWork is what a check can see of a parent's children without any of
// them waiting on a person.
var childWork = map[string]func(*decide.Observation){
	"working": func(o *decide.Observation) {
		o.ActiveChildren, o.ActiveChildIDs = 2, []string{"thr_child_a", "thr_child_b"}
	},
	"one failed beside one working": func(o *decide.Observation) {
		o.ActiveChildren, o.ActiveChildIDs, o.ErroredChildren = 1, []string{"thr_child_a"}, children("thr_child_b")
	},
	"scan failed": func(o *decide.Observation) { o.ChildrenUnverified = true },
}

// wantBrief is what the handoff brief says for each childWork case.
var wantBrief = map[string]string{
	"working":                       "2 child conversations started by the previous conversation were still working",
	"one failed beside one working": "1 child conversation started by the previous conversation was still working",
	"scan failed":                   "could not list them, so their number is unknown",
}

// A candidate in the conversation being left continues it, so its brief says
// nothing about children; any other candidate is told.
func TestChildrenLeftAppliesOnlyToADifferentConversation(t *testing.T) {
	obs := decide.Observation{State: decide.Error, ActiveChildren: 3, ChildrenUnverified: true}
	from := config.Agent{Adapter: "bb", BB: &config.BB{Thread: "thr_primary"}}
	cand := func(a config.Agent) config.Candidate { return config.Candidate{ID: "example", Agent: a} }
	told := handoff.ChildrenLeft{Working: 3, Unverified: true}
	for name, tc := range map[string]struct {
		to   config.Agent
		want handoff.ChildrenLeft
	}{
		"same thread, another model": {config.Agent{Adapter: "bb", BB: &config.BB{Thread: "thr_primary", Model: "example-model"}}, handoff.ChildrenLeft{}},
		"another thread":             {config.Agent{Adapter: "bb", BB: &config.BB{Thread: "thr_other"}}, told},
		"a new thread":               {config.Agent{Adapter: "bb", BB: &config.BB{NewThread: true}}, told},
		"a headless agent":           {config.Agent{Adapter: "codex"}, told},
	} {
		if got := childrenLeft(obs, from, cand(tc.to)); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
	if got := childrenLeft(decide.Observation{State: decide.Error}, from, cand(config.Agent{Adapter: "codex"})); got.Line() != "" {
		t.Errorf("no children, complete listing: %q", got.Line())
	}
}

// The live failure: the supervised conversation reached its usage limit while
// conversations it had started were working. A confirmed fallback was
// configured and rate limits were set to hand off, yet every check recorded
// "child work running" and the loop stalled until a person acted.
func TestErroredParentIsRecoveredWhileChildrenWork(t *testing.T) {
	healthy := func(w *world) {
		reg := &health.Registry{Candidates: map[string]*health.Entry{}}
		for _, id := range []string{"claude-other", "codex-main"} {
			reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
		}
		if err := reg.Save(w.dir); err != nil {
			t.Fatal(err)
		}
		w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	}
	for name, kids := range childWork {
		t.Run("handoff/"+name, func(t *testing.T) {
			w := newWorld(t, true, true)
			obs := limited
			kids(&obs)
			p := w.primary(obs)
			healthy(w)
			res := runTick(t, w, t0)
			if res.Action != "handoff" || res.Target != "claude-other" || strings.Contains(res.Reason, "child") {
				t.Fatalf("no handoff with children %s: %+v", name, res)
			}
			st, err := state.Load(w.dir)
			if err != nil || st.Target() != "claude-other" {
				t.Fatalf("target not moved: %+v %v", st, err)
			}
			if r := lastIncident(t, w); r.Action != "handoff" || r.TargetKey == r.TargetKeyAfter {
				t.Fatalf("incident: %+v", r)
			}
			// The brief tells the fallback what was left working, from the
			// count alone: no child is named.
			brief := strings.Join(w.adapters["claude-other"].sent, "\n")
			if !strings.Contains(brief, wantBrief[name]) || !strings.Contains(brief, "not supervised by this loop") || strings.Contains(brief, "thr_child") {
				t.Fatalf("brief with children %s:\n%s", name, brief)
			}
			// The limited conversation is sent nothing, and nobody is asked to act.
			if len(p.sent) != 0 {
				t.Fatalf("the limited parent was sent %d messages", len(p.sent))
			}
			if ctl, err := control.Load(w.dir); err != nil || ctl.AwaitingUser || !ctl.Enabled {
				t.Fatalf("the loop was held: %+v %v", ctl, err)
			}
		})
		t.Run("waits while the network is down/"+name, func(t *testing.T) {
			// Recovery includes the network gate: nothing is probed, sent or
			// moved while offline, with or without children.
			w := newWorld(t, true, true)
			obs := limited
			kids(&obs)
			p := w.primary(obs)
			healthy(w)
			opts := w.opts(t0)
			opts.Online = func(context.Context) bool { return false }
			res, err := Tick(context.Background(), w.cfg, opts)
			if err != nil || res.Action != "none" || res.Reason != decide.NetworkDownReason || res.Target != state.Primary || len(p.sent) != 0 {
				t.Fatalf("children %s: %+v %v", name, res, err)
			}
			if fb := w.adapters["claude-other"]; fb != nil && len(fb.sent) != 0 {
				t.Fatalf("children %s: a brief was sent while offline", name)
			}
		})
		t.Run("limit wait is reported/"+name, func(t *testing.T) {
			w, p := limitWaitWorld(t, 72*time.Hour)
			kids(&p.obs)
			res := waitTick(t, w, t0)
			if strings.Contains(res.Reason, "child") || len(limitWaitReports(w)) != 1 || len(p.sent) != 0 {
				t.Fatalf("children %s: %+v, %d reports, %d sent", name, res, len(limitWaitReports(w)), len(p.sent))
			}
		})
		t.Run("re-prompts stay bounded/"+name, func(t *testing.T) {
			w := newWorld(t, true, false)
			obs := decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
			kids(&obs)
			p := w.primary(obs)
			var actions []string
			for i := 0; i < 5; i++ {
				actions = append(actions, runTick(t, w, t0.Add(time.Duration(i)*time.Hour)).Action)
			}
			// Two re-prompts (the configured cap), then the owner is asked once.
			if got := strings.Join(actions, " "); got != "reprompt reprompt await_user none none" || len(p.sent) != 2 {
				t.Fatalf("children %s: %s, %d sent", name, got, len(p.sent))
			}
		})
		t.Run("auth error asks the owner/"+name, func(t *testing.T) {
			w := newWorld(t, true, true)
			obs := decide.Observation{State: decide.Error, ErrorClass: decide.ErrAuth}
			kids(&obs)
			p := w.primary(obs)
			healthy(w)
			if res := runTick(t, w, t0); res.Action != "await_user" || res.Reason != "auth error" || len(p.sent) != 0 {
				t.Fatalf("children %s: %+v", name, res)
			}
		})
	}
}

// The other half of the rule is unchanged: a parent that is not in error is
// never moved or woken while its children work, whatever else is configured.
func TestChildWorkStillHoldsAHealthyParentWithFallbacksConfigured(t *testing.T) {
	for name, kids := range childWork {
		for _, parent := range []decide.State{decide.Idle, decide.Active} {
			w := newWorld(t, true, true)
			w.cfg.Loop.Probe = config.Probe{Kind: "command", Command: []string{"x"}}
			obs := decide.Observation{State: parent}
			kids(&obs)
			p := w.primary(obs)
			w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
			for i := 0; i < 4; i++ {
				res := runTick(t, w, t0.Add(time.Duration(i)*time.Hour))
				if res.Action == "handoff" || res.Action == "reprompt" || res.Target != state.Primary || len(p.sent) > 1 {
					t.Fatalf("%s parent, children %s, check %d: %+v (%d sent)", parent, name, i, res, len(p.sent))
				}
				// The only message a held parent may get is the single notice
				// that a child failed.
				if len(p.sent) == 1 && !res.ChildErrorNote && i == 0 {
					t.Fatalf("%s parent, children %s: woken: %+v", parent, name, res)
				}
			}
		}
	}
}
