package supervise

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/bb"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/prompt"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Creating a BB thread is the single exception to "keepgoing never creates a
// conversation". It happens only here, only for a handoff to a fallback
// candidate that opted in with bb.new_thread, at most once per handoff and at
// most loop.limits.max_new_threads times per loop. The conversation being
// left is never stopped, archived or messaged.

// ThreadCreator is the part of the BB adapter that creates and finds threads.
type ThreadCreator interface {
	PlaceOf(ctx context.Context, thread string) (bb.Place, error)
	FindByTitle(ctx context.Context, p bb.Place, title string) ([]string, error)
	Spawn(ctx context.Context, r bb.SpawnRequest) (string, error)
}

func bbThreads(cli string) ThreadCreator { return bb.New(config.BB{CLI: cli}) }

// newThreadTitle is what a created thread is called, so a person can tell
// keepgoing started it. The token makes the title unique, which is how a
// creation interrupted before it was recorded is found again.
func newThreadTitle(loop string) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("keepgoing: %s (continued, %s)", loop, hex.EncodeToString(b)), nil
}

// isNewThread reports whether a candidate starts a new thread at handoff.
func isNewThread(c config.Candidate) bool {
	return c.Adapter == "bb" && c.BB != nil && c.BB.NewThread
}

// saveNewThreadIntent writes the intent (or its removal) to state.json at
// once, ahead of the rest of the check's state.
func (t *tick) saveNewThreadIntent(in *state.NewThreadIntent) error {
	saved, err := state.Load(t.loop.StateDir)
	if err != nil {
		return err
	}
	saved.NewThreadIntent = in
	if err := state.Save(t.loop.StateDir, saved); err != nil {
		return err
	}
	t.st.NewThreadIntent = in
	return nil
}

// createThread creates the thread for a handoff to c, with the brief as its
// first prompt. The intent is on disk before bb is asked, so whatever happens
// next a later check can find the thread by its title instead of creating
// another. A bb.ErrSpawnUncertain error leaves the intent in place.
func (t *tick) createThread(from string, fromAgent config.Agent, c config.Candidate, brief prompt.Message, reason string) (string, error) {
	creator := t.opts.Threads(c.BB.CLI)
	place := bb.Place{Project: c.BB.Project, Environment: c.BB.Environment}
	continues := ""
	if fromAgent.Adapter == "bb" && fromAgent.BB != nil {
		continues = fromAgent.BB.Thread
	}
	if place.Project == "" || place.Environment == "" {
		if continues == "" {
			return "", errors.New("no BB conversation to continue: set bb.project and bb.environment on this candidate")
		}
		seen, err := creator.PlaceOf(t.ctx, continues)
		if err != nil {
			return "", errors.New("could not read the project and environment of the conversation being continued")
		}
		if place.Project == "" {
			place.Project = seen.Project
		}
		if place.Environment == "" {
			place.Environment = seen.Environment
		}
	}
	title, err := newThreadTitle(t.loop.Name)
	if err != nil {
		return "", errors.New("could not generate a title")
	}
	intent := &state.NewThreadIntent{Candidate: c.ID, Title: title, Project: place.Project, Environment: place.Environment,
		From: from, Continues: continues, Reason: reason, RequestedAt: t.now}
	if err := t.saveNewThreadIntent(intent); err != nil {
		return "", errors.New("could not record the intent to create a thread; nothing was created")
	}
	if err := state.ReserveSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
		t.saveNewThreadIntent(nil) // nothing was asked of bb
		return "", err
	}
	t.graphSessionOf(from) // the end of the move that is being left
	text := brief.Text
	if continues != "" {
		text += "\n\nThis conversation continues @thread:" + continues + ". keepgoing started it for this handoff; the earlier conversation was left as it was.\n"
	}
	attempt := t.graphBegin(c.ID, "handoff", string(adapter.ModeAuto), brief)
	thread, err := creator.Spawn(t.ctx, bb.SpawnRequest{Place: place, Provider: c.Provider, Model: c.BB.Model, Reasoning: c.BB.Reasoning, Title: title, Prompt: text})
	outcome := "adapter_returned"
	if err != nil {
		outcome = "adapter_error"
	}
	t.graphFinish(attempt, outcome)
	t.st.LastPromptSend = &state.PromptSend{Version: 1, CheckAt: t.now, Target: c.ID, Action: "handoff", Mode: string(adapter.ModeAuto), Outcome: outcome,
		GoalID: brief.GoalID, GoalRevision: brief.GoalRevision}
	switch {
	case errors.Is(err, bb.ErrSpawnUncertain):
		return "", err
	case err != nil:
		t.st.NewThreadIntent = nil // bb refused: nothing exists
		return "", err
	}
	t.adoptThread(intent, thread)
	return thread, nil
}

// adoptThread records a created thread as its candidate's thread.
func (t *tick) adoptThread(in *state.NewThreadIntent, thread string) {
	if t.st.NewThreads == nil {
		t.st.NewThreads = map[string]state.CreatedThread{}
	}
	t.st.NewThreads[in.Candidate] = state.CreatedThread{Thread: thread, Title: in.Title, CreatedAt: t.now, From: in.From, Continues: in.Continues}
	t.st.NewThreadsCreated++
	t.st.NewThreadIntent = nil
	t.incidentNewThread = true
}

// moveToCreated makes a created thread's candidate the active target, as a
// handoff to an existing thread would.
func (t *tick) moveToCreated(in *state.NewThreadIntent, thread string, note *HandoffNote) {
	t.st.ClearBriefs() // a new target, a new episode
	t.st.ClearChildErrors()
	note.To, note.NewThread, note.Continues = in.Candidate, thread, in.Continues
	t.st.ActiveTarget, t.st.LastHandoffAt, t.st.LastWakeAt = in.Candidate, t.now, t.now
	t.st.Handoffs++
	t.st.Reprompts, t.st.LastRepromptAt = 0, time.Time{}
	t.res.Target = in.Candidate
	t.report("handoff", fmt.Sprintf("%s: handed off from %s to %s, a new BB thread keepgoing created for this handoff, after a %s error", t.loop.Name, in.From, in.Candidate, in.Reason),
		map[string]any{"from": in.From, "to": in.Candidate, "reason": in.Reason, "tried": note.Tried, "newThread": true})
}

// reconcileNewThread settles a creation whose outcome was never recorded: the
// supervisor stopped between asking bb and saving the answer, or bb gave no
// clear answer. It looks for the intent's unique title. Exactly one match is
// the thread: the handoff is completed. No match means nothing was created.
// While the listing cannot be trusted the intent stays, no thread is created
// and no handoff is made, since a second conversation might be started on the
// same task. That wait is bounded: after errors.max_reprompts such checks the
// loop is held for a person. moved says the loop was moved to the thread;
// held says the hold was raised and the check should end.
func (t *tick) reconcileNewThread() (moved, held bool) {
	in := t.st.NewThreadIntent
	if in == nil || t.opts.DryRun {
		return false, false
	}
	cli := config.DefaultBBCLI
	c, configured := config.Candidate{}, false
	if t.fb != nil {
		c, configured = t.fb.Find(in.Candidate)
	}
	if configured && c.BB != nil {
		cli = c.BB.CLI
	}
	ids, err := t.opts.Threads(cli).FindByTitle(t.ctx, bb.Place{Project: in.Project, Environment: in.Environment}, in.Title)
	switch {
	case err != nil || len(ids) > 1:
		t.newThreadUnresolved = true
		in.Unresolved++
		why := "BB could not be asked whether it was created"
		if err == nil {
			why = "more than one thread has the title keepgoing generated"
		}
		if in.Unresolved < t.loop.Errors.MaxReprompts {
			t.warn("new thread for %s: %s; nothing is created or handed off until that is known", in.Candidate, why)
			return false, false
		}
		// Not a wait to sit in silently: ask a person.
		in.Unresolved = 0
		d := decide.Decision{Action: decide.AwaitUser, Reason: fmt.Sprintf("a new BB thread titled %q may have been created for a handoff, and %s; check BB, then resume, or retarget to drop the pending creation", in.Title, why)}
		t.res.Action, t.res.Reason = string(d.Action), d.Reason
		if err := t.act(nil, d); err != nil {
			t.warn("could not hold the loop: %v", err)
		}
		return false, true
	case len(ids) == 0:
		t.st.NewThreadIntent = nil // nothing was created
		return false, false
	}
	thread := ids[0]
	if !configured || c.Disabled || !isNewThread(c) || t.st.Target() != in.From {
		// The thread exists, but the handoff it was created for no longer
		// applies. Record it and leave supervision where it is.
		t.adoptThread(in, thread)
		t.warn("new thread for %s was created earlier but the loop has since moved or the candidate changed; supervision was not moved to it", in.Candidate)
		return false, false
	}
	note := &HandoffNote{From: in.From, Reason: in.Reason, Tried: []string{in.Candidate}, Reconciled: true}
	t.res.Handoff = note
	t.adoptThread(in, thread)
	t.moveToCreated(in, thread, note)
	return true, false
}

// newThreadGate says why a new-thread candidate cannot be offered a handoff
// right now, or "".
func (t *tick) newThreadGate(c config.Candidate) string {
	switch {
	case !isNewThread(c):
		return ""
	case t.st.NewThreadIntent != nil:
		return "new_thread_pending" // an earlier creation is still unsettled
	case !decide.NewThreadAllowed(t.st.NewThreadsCreated, t.loop.Limits.MaxNewThreads):
		t.newThreadCapped = true
		return "new_thread_cap"
	}
	return ""
}
