package supervise

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/state"
)

// idleFacts is a digest of the structured facts the idle spacing rule
// watches: the Git progress fingerprint (as the time it last changed), the
// latest child outcome notice, the turn counts, the goal revision, the
// control record (any control change, including resume), and the target with
// what last set it, the last handoff or switch-back and the last timing step
// or retarget. It returns the digest as the facts are now, and as they will
// be once an idle wake sent now has been requested and has started its one
// turn. A session error is handled apart: it clears the record. Nothing here
// reads transcript or message text, and the digest holds no identifier.
func (t *tick) idleFacts(obs decide.Observation, ctl control.File) (now, afterWake string) {
	ts := func(v time.Time) string { return strconv.FormatInt(v.UnixNano(), 10) }
	goal := "none"
	if g, err := control.LoadGoal(t.loop.StateDir); err != nil {
		goal = "unavailable"
	} else if g != nil {
		s := g.Summary()
		goal = s.ID + "/" + strconv.FormatUint(s.Revision, 10)
	}
	progress := "untracked"
	if len(t.loop.Progress.Repositories) > 0 {
		progress = ts(t.st.ProgressChangedAt)
	}
	var step, retarget time.Time
	if tm := t.st.Timing; tm != nil {
		if tm.LastMove != nil {
			step = tm.LastMove.At
		}
		if tm.Retarget != nil {
			retarget = tm.Retarget.At
		}
	}
	parts := []string{
		"progress=" + progress,
		"child_outcome=" + strconv.FormatInt(obs.ChildOutcome, 10),
		"goal=" + goal,
		"control=" + ts(ctl.UpdatedAt),
		"target=" + t.st.Target() + "/" + t.st.TargetSetBy,
		"handoff=" + ts(t.st.LastHandoffAt),
		"timing=" + ts(step) + "/" + ts(retarget),
	}
	digest := func(turns string) string {
		sum := sha256.Sum256([]byte(strings.Join(append(parts, "turns="+turns), "\n")))
		return hex.EncodeToString(sum[:12])
	}
	// A wake adds one request and, once it runs, one turn start. Matching
	// the record therefore means exactly that happened since the wake: its
	// turn started, nobody else asked for a turn, and, since the rule acts
	// only on an idle session, the turn has ended. Until then, and after
	// anyone else's message, the facts differ.
	c := obs.Turns
	if c == nil {
		return digest("unknown"), digest("unknown")
	}
	count := func(r, s int64) string { return strconv.FormatInt(r, 10) + "/" + strconv.FormatInt(s, 10) }
	return digest(count(c.Requests, c.Starts)), digest(count(c.Requests+1, c.Starts+1))
}

// idleSpacingInput returns the rule's input for this check. With the key off
// it forgets the record and returns the zero input, which never spaces. A
// session seen in error ends the run of unchanged wakes: the error and the
// recovery that follows are changes. So does a direct lane in error, from the
// child listing's status: while one is, idle wakes are not spaced.
func (t *tick) idleSpacingInput(obs decide.Observation, ctl control.File) decide.IdleSpacing {
	first, max := t.loop.Progress.IdleSpacingWaits()
	t.idleFactsAfterWake = ""
	if max <= 0 || obs.State == decide.Error || len(obs.ErroredChildren) > 0 {
		t.st.IdleSpacing = nil
		return decide.IdleSpacing{}
	}
	var now string
	now, t.idleFactsAfterWake = t.idleFacts(obs, ctl)
	in := decide.IdleSpacing{First: first, Max: max}
	if r := t.st.IdleSpacing; r != nil && !r.LastWakeAt.IsZero() && r.Facts == now {
		in.Unchanged = r.Unchanged + 1
		in.SinceIdleWake = t.now.Sub(r.LastWakeAt)
	}
	return in
}

// recordIdleWake runs once the adapter accepted an ordinary idle wake. A
// failed send records nothing: it started no turn, so it is not a wake that
// changed nothing. A wake that carried a child-error notice starts the count
// over: the notice is a change.
func (t *tick) recordIdleWake(in decide.Input, childNotice bool) {
	if t.idleFactsAfterWake == "" {
		return
	}
	n := in.IdleSpacing.Unchanged
	if childNotice {
		n = 0
	}
	t.st.IdleSpacing = &state.IdleSpacing{LastWakeAt: t.now, Facts: t.idleFactsAfterWake, Unchanged: n}
}

// noteIdleSpacing records the final decision of a check: when a spaced idle
// wake is due and why, for status, and the count of spaced wakes per day.
// Neither is ever read by a decision.
func (t *tick) noteIdleSpacing(in decide.Input, d decide.Decision) {
	r := t.st.IdleSpacing
	if r == nil || t.opts.DryRun {
		return
	}
	if d.IdleWait == "" {
		r.WaitUntil, r.Wait = time.Time{}, ""
		return
	}
	s := in.IdleSpacing
	r.WaitUntil = r.LastWakeAt.Add(decide.IdleSpacingWait(s.First, s.Max, s.Unchanged))
	r.Wait = string(d.IdleWait)
	t.st.CountIdleSpaced(string(d.IdleWait), t.now)
}
