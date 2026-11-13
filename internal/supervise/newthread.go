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

// Time for creating a thread. It takes the place of the model switch and send
// of an ordinary handoff, so its three bb commands together stay within the
// time a check reserves for those (reserveSend). The lookup made for an
// unsettled creation at the start of a check comes out of the observation's
// time instead.
const (
	newThreadPlaceTime = 10 * time.Second // read the project and environment
	newThreadSpawnTime = 30 * time.Second // bb thread spawn
	newThreadFindTime  = 20 * time.Second // look the thread up by its title
)

// isNewThread reports whether a candidate starts a new thread at handoff.
func isNewThread(c config.Candidate) bool {
	return c.Adapter == "bb" && c.BB != nil && c.BB.NewThread
}

// errNewThreadUnsettled means a thread may exist and could not be found: the
// loop must be held for a person, and nothing else may be tried.
var errNewThreadUnsettled = errors.New("a new thread may have been created and could not be found")

// beginNewThread saves the intent to create a thread, and counts the attempt,
// in one write to state.json ahead of the rest of the check's state. From
// here on the attempt counts against the cap unless it is proven never to
// have reached BB.
func (t *tick) beginNewThread(in *state.NewThreadIntent) error {
	saved, err := state.Load(t.loop.StateDir)
	if err != nil {
		return err
	}
	saved.NewThreadIntent, saved.NewThreadsCreated, saved.LastNewThreadAt = in, saved.NewThreadsCreated+1, t.now
	if err := state.Save(t.loop.StateDir, saved); err != nil {
		return err
	}
	t.st.NewThreadIntent, t.st.NewThreadsCreated, t.st.LastNewThreadAt = in, saved.NewThreadsCreated, t.now
	return nil
}

// refundNewThread drops an attempt that provably never reached BB.
func (t *tick) refundNewThread(previous time.Time) {
	t.st.NewThreadIntent, t.st.LastNewThreadAt = nil, previous
	if t.st.NewThreadsCreated > 0 {
		t.st.NewThreadsCreated--
	}
	if saved, err := state.Load(t.loop.StateDir); err == nil {
		saved.NewThreadIntent, saved.NewThreadsCreated, saved.LastNewThreadAt = nil, t.st.NewThreadsCreated, previous
		state.Save(t.loop.StateDir, saved)
	}
}

// ownershipNote is fixed wording appended to the first prompt of a created
// thread. The handoff does not change who owns the loop.
func ownershipNote(loop string) string {
	return "\n\nLoop ownership: this conversation does not own the \"" + loop + "\" loop. Its owner is unchanged by this handoff. " +
		"From here you may run keepgoing pause, stop, await-user and complete for this loop. " +
		"Do not run keepgoing resume or enable, and do not use --take-over, unless a person asks you to.\n"
}

// createThread creates the thread for a handoff to c, with the brief as its
// first prompt. The intent is on disk, and counted, before bb is asked. Only
// an answer that proves the request never reached BB is treated as "nothing
// was created". After any other failure the thread is looked for by its
// title: found, it is the thread; not found, errNewThreadUnsettled.
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
		ctx, cancel := context.WithTimeout(t.ctx, newThreadPlaceTime)
		seen, err := creator.PlaceOf(ctx, continues)
		cancel()
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
	previous := t.st.LastNewThreadAt
	intent := &state.NewThreadIntent{Candidate: c.ID, Title: title, Project: place.Project, Environment: place.Environment,
		From: from, Continues: continues, Reason: reason, RequestedAt: t.now}
	if err := t.beginNewThread(intent); err != nil {
		return "", errors.New("could not record the intent to create a thread; nothing was created")
	}
	if err := state.ReserveSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
		t.refundNewThread(previous) // nothing was asked of bb
		return "", err
	}
	t.graphSessionOf(from) // the end of the move that is being left
	text := brief.Text
	if continues != "" {
		// BB's documented way to reference the source of a handoff.
		text += "\n\nContinue from @thread:" + continues + ". keepgoing started this conversation for the handoff; the earlier one was left as it was.\n"
	}
	text += ownershipNote(t.loop.Name)
	attempt := t.graphBegin(c.ID, "handoff", string(adapter.ModeAuto), brief)
	ctx, cancel := context.WithTimeout(t.ctx, newThreadSpawnTime)
	thread, err := creator.Spawn(ctx, bb.SpawnRequest{Place: place, Provider: c.Provider, Model: c.BB.Model, Reasoning: c.BB.Reasoning,
		Permission: c.BB.NewThreadPermission(), Title: title, Prompt: text})
	cancel()
	outcome := "adapter_returned"
	if err != nil {
		outcome = "adapter_error"
	}
	t.graphFinish(attempt, outcome)
	t.st.LastPromptSend = &state.PromptSend{Version: 1, CheckAt: t.now, Target: c.ID, Action: "handoff", Mode: string(adapter.ModeAuto), Outcome: outcome,
		GoalID: brief.GoalID, GoalRevision: brief.GoalRevision}
	switch {
	case err == nil:
		t.adoptThread(intent, thread)
		return thread, nil
	case errors.Is(err, bb.ErrSpawnRefused):
		t.refundNewThread(previous)
		return "", err
	}
	// No proof either way: BB may have created the thread and failed after.
	ctx, cancel = context.WithTimeout(t.ctx, newThreadFindTime)
	ids, findErr := creator.FindByTitle(ctx, place, title)
	cancel()
	if findErr == nil && len(ids) == 1 {
		t.adoptThread(intent, ids[0]) // created, whatever bb went on to report
		return ids[0], nil
	}
	return "", errNewThreadUnsettled
}

// adoptThread records a created thread as its candidate's thread. The attempt
// was counted when it was begun.
func (t *tick) adoptThread(in *state.NewThreadIntent, thread string) {
	if t.st.NewThreads == nil {
		t.st.NewThreads = map[string]state.CreatedThread{}
	}
	t.st.NewThreads[in.Candidate] = state.CreatedThread{Thread: thread, Title: in.Title, CreatedAt: t.now, From: in.From, Continues: in.Continues}
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

// unsettledReason is the hold raised when a thread may exist and cannot be
// found. It names the title so a person can look for it.
func unsettledReason(in *state.NewThreadIntent) string {
	return fmt.Sprintf("keepgoing asked BB to create a thread titled %q for a handoff and cannot tell whether it exists (it may have been renamed or archived, or BB could not be asked). "+
		"Nothing else was created or handed off. Look for it in BB, then choose where the loop continues with retarget, and resume", in.Title)
}

// holdForNewThread asks a person. The pending creation stays on record and
// stays counted; a later check, or retarget, looks for the thread again.
func (t *tick) holdForNewThread() {
	d := decide.Decision{Action: decide.AwaitUser, Reason: unsettledReason(t.st.NewThreadIntent)}
	t.res.Action, t.res.Reason = string(d.Action), d.Reason
	if err := t.act(nil, d); err != nil {
		t.warn("could not hold the loop: %v", err)
	}
}

// reconcileNewThread settles a creation whose outcome was never recorded: the
// supervisor stopped between asking bb and saving the answer, or a person
// resumed a loop held for it. It looks for the intent's unique title. Exactly
// one match is the thread, and the handoff is completed. Anything else is not
// proof that nothing was created (the thread may have been renamed, archived
// or not listed yet), so the loop is held again: no spawn is retried and the
// loop is handed to nothing else. moved says the loop was moved to the
// thread; held says the hold was raised and the check should end.
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
	// The lookup's time is taken from the observation that follows, so the
	// check's hard limit is unchanged.
	started := t.opts.Clock()
	ctx, cancel := context.WithTimeout(t.ctx, newThreadFindTime)
	ids, err := t.opts.Threads(cli).FindByTitle(ctx, bb.Place{Project: in.Project, Environment: in.Environment}, in.Title)
	cancel()
	t.observeSpent = min(max(t.opts.Clock().Sub(started), 0), newThreadFindTime)
	if err != nil || len(ids) != 1 {
		t.holdForNewThread()
		return false, true
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
	if !isNewThread(c) {
		return ""
	}
	if t.st.NewThreadIntent != nil {
		return "new_thread_pending" // an earlier creation is still unsettled
	}
	v := decide.NewThreadGate(t.st.NewThreadsCreated, t.loop.Limits.MaxNewThreads, t.now.Sub(t.st.LastNewThreadAt), config.MinNewThreadInterval, !t.st.LastNewThreadAt.IsZero())
	switch v {
	case decide.NewThreadAllowed:
		return ""
	case decide.NewThreadCapped:
		t.newThreadCapped = true
	}
	return string(v)
}
