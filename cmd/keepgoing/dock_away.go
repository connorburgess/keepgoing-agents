package main

import (
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Why a loop left a route is said only from the record of that route's own
// departure: a check that moved the loop (with the error class it observed
// on the route it left), or a retarget command. The newest record wins, and
// nothing is inferred from configuration. The reset time a provider reported
// is kept by state for the first route only, and is attached only to the
// handoff that left it.

// Departure kinds.
const (
	dockLeftLimit     = "usage_limit"
	dockLeftSignIn    = "sign_in"
	dockLeftOverload  = "overloaded"
	dockLeftNetwork   = "network"
	dockLeftError     = "other_error"
	dockLeftCommand   = "command"
	dockLeftTiming    = "timing"       // usage-aware timing stepped off it
	dockLeftUnknown   = "not_recorded" // no record of why
	dockLeftNotKept   = "not_retained" // the history no longer reaches it
	dockReturnOn      = "on"
	dockReturnOff     = "off"
	dockReturnStopped = "stopped"
	// dockReturnNoHandoff: returning is on, but it follows only a handoff and
	// state records none (a retarget does not set one), so it will not come.
	dockReturnNoHandoff = "no_handoff"
	// dockReturnTiming: usage-aware timing returns at its window's reset.
	dockReturnTiming = "timing"
)

// dockDeparture is one route the loop left on its way to its current route.
type dockDeparture struct {
	Position int        `json:"position"`
	Route    string     `json:"route"` // the route's key
	How      string     `json:"how"`
	At       *time.Time `json:"at,omitempty"`
	// ResetsAt is the reset the provider reported when the loop left its
	// first route on a usage limit; only the first route has one.
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
	ResetPassed bool       `json:"resetPassed"`
	// ResetFar: the provider reported a reset more than a year from the
	// read; it is not shown.
	ResetFar bool `json:"resetFar,omitempty"`
	// Text is the sentence on the loop's page; Others is the same fact on the
	// routes view, starting with the loop's name.
	Text   dockText `json:"text"`
	Others dockText `json:"others"`
	loop   string
	key    string
}

// atLimit is a usage limit whose reported reset has not passed, or was not
// reported.
func (d dockDeparture) atLimit() bool { return d.How == dockLeftLimit && !d.ResetPassed }

// dockAway is what is recorded about a loop that is not on its first route.
type dockAway struct {
	// Choice is the current route's place in the order, 2 for the second.
	Choice int        `json:"choice"`
	Since  *time.Time `json:"since,omitempty"`
	// How is why the first route was left, as in dockDeparture.How.
	How           string          `json:"how"`
	Return        string          `json:"return"` // on, off, stopped or no_handoff
	LastReturnTry *time.Time      `json:"lastReturnTry,omitempty"`
	Departures    []dockDeparture `json:"departures"`
	// Short and Why are the two lines of the Route card; Picker is what the
	// loop's row adds ("on its second choice").
	Short  dockText `json:"short"`
	Why    dockText `json:"why"`
	Picker string   `json:"picker"`
	// timing is the move usage-aware timing made to the current route.
	timing *state.TimingMove
	// timingLeft: that route is no longer a timing step.
	timingLeft bool
}

// dockMove is one recorded move of the loop from one route to another.
type dockMove struct {
	at         time.Time
	from, to   string // hashed route aliases, as the history keeps them
	how        string
	fromFirst  bool
	isHandoff  bool
	errorClass string
}

// dockMoves reads every move the retained history records, oldest first: a
// check that moved the loop (a handoff or a return) and a retarget command.
func dockMoves(tl *timelineReport) []dockMove {
	var out []dockMove
	if tl == nil {
		return out
	}
	for _, e := range tl.Events {
		r := e.Incident
		if r == nil || r.DryRun || r.Outcome == "dry_run" {
			continue
		}
		from, to := r.TargetKey, r.TargetKeyAfter
		if from == "" || to == "" || from == "unavailable" || to == "unavailable" || from == to {
			continue
		}
		m := dockMove{at: e.At, from: from, to: to}
		switch {
		case r.Kind == "retarget":
			m.how = dockLeftCommand
		case r.Kind == "tick" && (r.Cause == incident.TimingStepUp || r.Cause == incident.TimingStepDown || r.Cause == incident.TimingReturn):
			m.how = dockLeftTiming
		case r.Kind == "tick" && (r.Action == "handoff" || r.Action == "switch_back") && r.Outcome == "succeeded":
			m.isHandoff = r.Action == "handoff"
			m.errorClass = r.Observation.ErrorClass
			switch r.Observation.ErrorClass {
			case "rate_limit":
				m.how = dockLeftLimit
			case "auth":
				m.how = dockLeftSignIn
			case "overloaded":
				m.how = dockLeftOverload
			case "network":
				m.how = dockLeftNetwork
			default:
				m.how = dockLeftError
			}
		default:
			continue
		}
		out = append(out, m)
	}
	return out
}

// dockAwayOf follows the recorded moves back from the loop's current route to
// its first, attributing each route it left to the record of that departure.
func dockAwayOf(st *statusOut, chain *dockChainInput, entries []dockChainEntry, tl *timelineReport, now time.Time) *dockAway {
	if st == nil || !st.TargetConfigured || st.Target == state.Primary || chain == nil {
		return nil
	}
	a := &dockAway{Return: dockReturnOff, Departures: []dockDeparture{}}
	if tr := st.State.Timing; st.State.TargetSetBy == state.SetByTiming && tr != nil && tr.LastMove != nil && tr.LastMove.To == st.Target {
		return dockTimingAway(a, st, chain, entries, now)
	}
	position := map[string]int{incident.TargetKey(state.Primary): 0}
	for i, cand := range chain.Candidates {
		position[incident.TargetKey(cand.ID)] = i + 1
		if cand.ID == st.Target {
			a.Choice = i + 2
		}
	}
	routeKey := func(pos int) string {
		for _, e := range entries {
			if e.Position == pos {
				return e.Route
			}
		}
		return ""
	}
	switch {
	case !chain.SwitchBack:
	case st.TakeoverBriefs.SwitchBackStopped:
		a.Return = dockReturnStopped
	case st.State.LastHandoffAt.IsZero():
		// switchBackReady returns false without a recorded handoff.
		a.Return = dockReturnNoHandoff
	default:
		a.Return = dockReturnOn
	}
	if t := st.State.LastSwitchBackTry; !t.IsZero() {
		a.LastReturnTry = &t
	}

	moves := dockMoves(tl)
	firstKey := incident.TargetKey(state.Primary)
	newestFromFirst := -1
	for i, m := range moves {
		if m.from == firstKey && m.isHandoff {
			newestFromFirst = i
		}
	}
	cur := incident.TargetKey(st.Target)
	found := map[int]bool{}
	for i := len(moves) - 1; i >= 0 && cur != firstKey; i-- {
		m := moves[i]
		if m.to != cur {
			continue
		}
		pos, known := position[m.from]
		if !known || found[pos] {
			break // a route no longer in the configuration, or a loop in the records
		}
		at := m.at
		if a.Since == nil {
			a.Since = &at
		}
		d := dockDeparture{Position: pos, Route: routeKey(pos), How: m.how, At: &at}
		if pos == 0 && m.isHandoff && m.how == dockLeftLimit && i == newestFromFirst {
			// state keeps the reset reported at the newest handoff off the first
			// route; it belongs to this departure and no other.
			if r := st.State.PrimaryResetsAt; !r.IsZero() && !st.State.LastHandoffAt.IsZero() && !at.After(st.State.LastHandoffAt) {
				if dockSaneReset(now, &r) {
					d.ResetsAt, d.ResetPassed = &r, !now.Before(r)
				} else {
					d.ResetFar = true
				}
			}
		}
		found[pos] = true
		a.Departures = append(a.Departures, d)
		cur = m.from
	}
	if cur != firstKey {
		// The records stop before reaching the first route.
		how := dockLeftUnknown
		if tl != nil && (len(moves) > 0 || tl.Filter.ReachesPrunedHistory || timelineDropped(tl)) {
			how = dockLeftNotKept
		}
		a.Departures = append(a.Departures, dockDeparture{Position: 0, Route: routeKey(0), How: how})
	}
	for i := range a.Departures {
		if a.Departures[i].Position == 0 {
			a.How = a.Departures[i].How
		}
	}
	a.finish(now)
	return a
}

func timelineDropped(tl *timelineReport) bool {
	for _, s := range tl.Sources {
		if s.Source == incident.FileName && s.Dropped > 0 {
			return true
		}
	}
	return false
}

// finish writes the away sentences.
func (a *dockAway) finish(now time.Time) {
	a.Picker = "on its " + dockOrdinal(a.Choice) + " choice"
	a.Short = txt("On its " + dockOrdinal(a.Choice) + " choice")
	if a.Since != nil {
		a.Short = txt(a.Short, " since ", tm(*a.Since, "date"))
	}
	var first dockDeparture
	for _, d := range a.Departures {
		if d.Position == 0 {
			first = d
		}
	}
	switch first.How {
	case dockLeftLimit:
		switch {
		case first.ResetFar:
			a.Why = txt("First choice hit a usage limit; the reported reset is more than a year away")
		case first.ResetsAt == nil:
			a.Why = txt("First choice hit a usage limit; no reset time was reported")
		case first.ResetPassed:
			a.Why = txt("First choice hit a usage limit; its reported reset, ", tm(*first.ResetsAt, "date"), ", has passed")
		default:
			a.Why = txt("First choice at a usage limit until ", tm(*first.ResetsAt, "date"))
		}
	case dockLeftSignIn:
		a.Why = txt("First choice left after its sign-in failed")
	case dockLeftOverload:
		a.Why = txt("First choice left while its provider was overloaded")
	case dockLeftNetwork:
		a.Why = txt("First choice left when it could not be reached")
	case dockLeftError:
		a.Why = txt("First choice left after an error")
	case dockLeftCommand:
		a.Why = txt("Moved off its first choice by a command")
	case dockLeftTiming:
		a.Why = a.timingWhy()
	case dockLeftNotKept:
		a.Why = txt("The record of why it left its first choice is no longer retained")
	default:
		a.Why = txt("Why it left its first choice is not recorded")
	}
	for i := range a.Departures {
		a.Departures[i].Text = a.departureText(a.Departures[i], "", now)
	}
}

// departureText is the sentence about one route the loop left. With a
// subject, it starts with the loop's name for the routes view.
func (a *dockAway) departureText(d dockDeparture, subject string, now time.Time) dockText {
	who, moved := "The loop", "the loop moved on"
	if subject != "" {
		who, moved = subject, "moved on"
	}
	on := func() dockText {
		if d.At == nil {
			return txt()
		}
		return txt(" on ", tm(*d.At, "date"))
	}
	var s dockText
	switch d.How {
	case dockLeftLimit:
		if subject != "" {
			s = txt(subject+" hit a usage limit here", on(), ", and moved on.")
		} else {
			s = txt("Hit a usage limit here", on(), ", and the loop moved on.")
		}
	case dockLeftSignIn:
		s = txt("Sign-in failed here", on(), ", and "+moved+".")
		if subject != "" {
			s = txt(subject+": sign-in failed here", on(), ", and it moved on.")
		}
	case dockLeftOverload:
		s = txt("The provider was overloaded here", on(), ", and "+moved+".")
		if subject != "" {
			s = txt(subject+": the provider was overloaded here", on(), ", and it moved on.")
		}
	case dockLeftNetwork:
		s = txt("Could not be reached here", on(), ", and "+moved+".")
		if subject != "" {
			s = txt(subject+": could not be reached here", on(), ", and it moved on.")
		}
	case dockLeftError:
		s = txt("Failed here", on(), ", and "+moved+".")
		if subject != "" {
			s = txt(subject+": failed here", on(), ", and it moved on.")
		}
	case dockLeftCommand:
		s = txt(who+" was pointed at another route by a command", on(), ".")
	case dockLeftTiming:
		return a.timingDeparture(d, who, on())
	case dockLeftNotKept:
		s = txt("The record of why " + lowerFirst(who) + " left this route is no longer retained.")
	default:
		s = txt("Why " + lowerFirst(who) + " left this route is not recorded.")
	}
	if d.Position != 0 {
		return s
	}
	// Only the first route has a reported reset and a return.
	if d.How == dockLeftLimit {
		switch {
		case d.ResetFar:
			s = txt(s, " The provider reported a reset more than a year away; it is not shown.")
		case d.ResetsAt == nil:
			s = txt(s, " The provider reported no reset time.")
		case d.ResetPassed:
			s = txt(s, " The reset the provider reported, ", tm(*d.ResetsAt, "datetime"), ", has passed.")
		default:
			s = txt(s, " The provider reported a reset for ", tm(*d.ResetsAt, "datetime"), " (in "+dockSpan(d.ResetsAt.Sub(now))+").")
		}
	}
	forWhom := "this loop"
	if subject != "" {
		forWhom = subject
	}
	switch a.Return {
	case dockReturnOff:
		s = txt(s, " keepgoing will not return by itself: returning is switched off for "+forWhom+".")
	case dockReturnStopped:
		s = txt(s, " keepgoing has stopped trying to return, after its attempts were refused.")
	case dockReturnNoHandoff:
		if d.How == dockLeftCommand {
			s = txt(s, " keepgoing will not return by itself: the loop was moved by a command.")
		} else {
			s = txt(s, " keepgoing will not return by itself: its state records no handoff to return from.")
		}
	default:
		switch {
		case d.How == dockLeftLimit && d.ResetsAt != nil && !d.ResetPassed:
			s = txt(s, " keepgoing returns after that, once a test passes.")
		case a.LastReturnTry != nil:
			s = txt(s, " A return was last tried "+dockAgo(now, *a.LastReturnTry)+".")
		default:
			s = txt(s, " No return has been tried yet.")
		}
	}
	return s
}

func lowerFirst(s string) string {
	if s == "The loop" {
		return "the loop"
	}
	return s
}

// dockTimingAway describes a loop on a step usage-aware timing chose, from the
// move on record: when, which window it was for and when that resets.
func dockTimingAway(a *dockAway, st *statusOut, chain *dockChainInput, entries []dockChainEntry, now time.Time) *dockAway {
	m := st.State.Timing.LastMove
	a.timing, a.Return, a.How = m, dockReturnTiming, dockLeftTiming
	at := m.At
	a.Since = &at
	position := map[string]int{state.Primary: 0}
	for i, cand := range chain.Candidates {
		position[cand.ID] = i + 1
		if cand.ID == st.Target {
			a.Choice = i + 2
		}
	}
	routeKey := func(pos int) string {
		for _, e := range entries {
			if e.Position == pos {
				return e.Route
			}
		}
		return ""
	}
	if pos, ok := position[m.From]; ok {
		a.Departures = append(a.Departures, dockDeparture{Position: pos, Route: routeKey(pos), How: dockLeftTiming, At: &at})
	}
	if m.From != state.Primary {
		// An earlier timing move left the first route; only the last is kept.
		a.Departures = append(a.Departures, dockDeparture{Position: 0, Route: routeKey(0), How: dockLeftTiming})
	}
	cfgSteps := chain.TimingSteps
	a.timingLeft = !chain.TimingOn || !containsString(cfgSteps, st.Target)
	a.finish(now)
	return a
}

// timingWhy is the Route card's second line for a loop on a timing step.
func (a *dockAway) timingWhy() dockText {
	m := a.timing
	if m == nil {
		return txt("Moved off its first choice by usage-aware timing")
	}
	verb := "stepped up"
	if m.Action == "down" {
		verb = "stepped down"
	}
	s := txt("Usage-aware timing " + verb)
	if m.Window != "" && !m.ResetsAt.IsZero() {
		s = txt(s, " for the "+dockWindowName(m.Window)+" window, which resets ", tm(m.ResetsAt, "datetime"))
	}
	if a.timingLeft {
		return txt(s, "; it is no longer a timing step, so the next turn boundary returns it to its first choice")
	}
	return s
}

// timingDeparture is the sentence about the route timing moved the loop off.
func (a *dockAway) timingDeparture(d dockDeparture, who string, on dockText) dockText {
	m := a.timing
	if m == nil || d.At == nil {
		return txt("Usage-aware timing moved " + lowerFirst(who) + " off this route.")
	}
	var s dockText
	switch m.Action {
	case "down":
		s = txt("Usage-aware timing stepped down from here", on, ": the "+dockWindowName(m.Window)+" window was projected to run out before its reset.")
	default:
		s = txt("Usage-aware timing stepped up from here", on, ", to use the "+dockWindowName(m.Window)+" allowance before it resets.")
	}
	if d.Position != 0 {
		return s
	}
	switch {
	case a.timingLeft:
		return txt(s, " That route is no longer a timing step: the next turn boundary returns here.")
	case !m.ResetsAt.IsZero():
		return txt(s, " Timing returns here when that window resets, ", tm(m.ResetsAt, "datetime"), ", or earlier if the readings show it running out.")
	}
	return s
}
