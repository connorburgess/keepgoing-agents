package supervise

import (
	"fmt"
	"strconv"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
)

// refusalStreakEvent is the report sent when a streak of refused turns
// reaches 3, and again at 10 and every 10 after. It is a report only: the
// loop keeps continuing, unless refusal.ask_after holds it for a person.
const refusalStreakEvent = "refusal_streak"

// gaugeRefusals keeps the refusal gauge. A refused turn is counted once, on
// the first check that sees it; the continue sent for it makes the next
// refused turn a new one. A turn that ends without a refusal ends the streak,
// and is counted as recovered when a continue came before it. A streak that
// raised a decision starts over once the decision has been answered.
func (t *tick) gaugeRefusals(obs decide.Observation) {
	t.refusalMark, t.fallbackMark = "", false
	if t.opts.DryRun {
		return
	}
	g := &t.st.Refusals
	// A model fallback after a refusal is noted once and changes nothing. The
	// first sight of a target's log sets the baseline without a note, so an
	// old fallback is not reported as new.
	if obs.ModelFallback > 0 {
		if g.FallbackTarget == t.res.Target && obs.ModelFallback > g.FallbackSeq {
			t.fallbackMark = true
		}
		if g.FallbackTarget != t.res.Target || obs.ModelFallback > g.FallbackSeq {
			g.FallbackSeq, g.FallbackTarget = obs.ModelFallback, t.res.Target
		}
	} else if g.FallbackTarget != t.res.Target {
		g.FallbackSeq, g.FallbackTarget = 0, t.res.Target
	}
	if g.Asked {
		g.Asked, g.Streak, g.StreakSince, g.Seen, g.Continued = false, 0, time.Time{}, false, false
	}
	refused := obs.State == decide.Error && obs.ErrorClass == decide.ErrRefused
	switch {
	case refused && !g.Seen:
		streak := g.Refused(t.now)
		t.refusalMark = "refused"
		if streak == 3 || (streak >= 10 && streak%10 == 0) {
			t.reportRefusalStreak(streak)
		}
	case !refused && g.Streak > 0 && turnEnded(obs):
		if g.Ended(t.now, obs.State == decide.Idle) {
			t.refusalMark = "recovered"
		}
	}
}

// turnEnded: the session's latest turn has finished, one way or another.
func turnEnded(obs decide.Observation) bool {
	return !obs.Gone && (obs.State == decide.Idle || obs.State == decide.Error)
}

func (t *tick) reportRefusalStreak(streak int) {
	if !t.loop.Report.ReportsEvent(refusalStreakEvent) || !t.reserveReport(fmt.Sprintf("%s:%d:%d", refusalStreakEvent, t.st.Refusals.StreakSince.Unix(), streak)) {
		return
	}
	g := t.st.Refusals
	day, recovered := g.InLastDay(t.now)
	ask := ""
	if n := t.loop.Refusal.AskAfterCount(); n > 0 && streak < n {
		ask = fmt.Sprintf(" At %d in a row it stops and asks you how to proceed.", n)
	}
	t.report(refusalStreakEvent, fmt.Sprintf("%s: the model or provider has declined %d turns in a row on %s. keepgoing keeps sending a plain continue in the same session, spaced up to 10 minutes apart, and never moves the work to another model, account or conversation. False flags are common and a continue usually clears them: in the last 24 hours %d turns were declined and %d recovered after a continue.%s To stop the continues, run keepgoing pause or keepgoing stop for this loop.",
		t.loop.Name, streak, t.res.Target, day, recovered, ask),
		map[string]any{"target": t.res.Target, "streak": streak, "longestStreak": g.LongestStreak, "total": g.Total, "recovered": g.Recovered, "lastDay": day, "recoveredLastDay": recovered})
}

// questionEvidence is what a question of kind records as its facts: counts,
// times, the route and the error class. Never text from the session.
func (t *tick) questionEvidence(kind string) []control.Evidence {
	ts := func(at time.Time) string { return at.UTC().Format(time.RFC3339) }
	route := control.Evidence{Key: "route", Type: "route", Value: t.res.Target}
	if kind == control.QuestionRestricted {
		return []control.Evidence{route, {Key: "error_class", Type: "class", Value: string(decide.ErrRestricted)}, {Key: "last_at", Type: "time", Value: ts(t.now)}}
	}
	g := t.st.Refusals
	_, recovered := g.InLastDay(t.now)
	first := g.StreakSince
	if first.IsZero() {
		first = g.LastAt
	}
	return []control.Evidence{
		{Key: "declined_turns", Type: "count", Value: strconv.Itoa(g.Streak)},
		{Key: "first_at", Type: "time", Value: ts(first)},
		{Key: "last_at", Type: "time", Value: ts(g.LastAt)},
		route,
		{Key: "error_class", Type: "class", Value: string(decide.ErrRefused)},
		{Key: "current_streak", Type: "count", Value: strconv.Itoa(g.Streak)},
		{Key: "recovered_by_continue_24h", Type: "count", Value: strconv.Itoa(recovered)},
	}
}

// withdrawQuestion removes the open question with this ID, if it is still the
// open one, and records that it was withdrawn.
func (t *tick) withdrawQuestion(id string) {
	removed := false
	if _, err := control.Update(t.loop.StateDir, func(f *control.File) {
		if f.Question != nil && f.Question.ID == id {
			f.Question, removed = nil, true
		}
	}); err != nil {
		t.warn("an open question could not be withdrawn: %v", err)
		return
	}
	if removed {
		t.questionRecord("question_withdrawn", id)
	}
}

// questionRecord adds a control record that carries the question's ID only.
func (t *tick) questionRecord(operation, id string) {
	r := incident.Record{Kind: "control", CheckStartedAt: t.now, Build: t.opts.Version, ControlChange: &incident.ControlChange{Operation: operation, QuestionID: id, SaveObservedAt: time.Now().UTC(), Caller: "unavailable"}}
	if c, err := control.Load(t.loop.StateDir); err == nil {
		r.ControlBefore, r.ControlAfter = incident.Gate(c.Gate()), incident.Gate(c.Gate())
	}
	if err := incident.Append(t.loop.StateDir, r); err != nil {
		t.warn("question history not recorded: %v", err)
	}
}
