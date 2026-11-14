package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The parts of one loop's page beyond its routes: the last decision, the
// current hold, the device, each lane, and recent history. Like the rest of
// the dock view they are projections of records that already exist (status,
// runs and the retained history timeline reads) and word only what a record
// says. The fixed cause vocabulary of incident.Cause is the only source of a
// reason; free-text reasons and notes are never shown here.

const (
	dockMaxLanes  = 24 // lanes listed per loop; the rest are counted
	dockMaxEvents = 5  // recent history entries per loop
)

// dockDecision is the last recorded check's action and its fixed cause.
type dockDecision struct {
	Availability string     `json:"availability"` // known or unknown
	Phrase       string     `json:"phrase"`
	Cause        string     `json:"cause,omitempty"`
	At           *time.Time `json:"at,omitempty"`
	AgeSeconds   *int64     `json:"ageSeconds,omitempty"`
	// Since is when the unbroken run of checks with this same result began,
	// from the retained history; absent when the last check stands alone or
	// the history does not show it.
	Since        *time.Time `json:"since,omitempty"`
	SinceSeconds *int64     `json:"sinceSeconds,omitempty"`
	// Sub is the line under the decision: its age, and since when.
	Sub dockText `json:"sub"`
}

// dockHoldState is the hold in force now, from the control flags.
type dockHoldState struct {
	// none, held or unknown.
	State  string     `json:"state"`
	Phrase string     `json:"phrase"`
	Since  *time.Time `json:"since,omitempty"`
	// SinceEvidence is hold_recorded or control_changed, as for dockNeed.
	SinceEvidence string `json:"sinceEvidence,omitempty"`
	SinceSeconds  *int64 `json:"sinceSeconds,omitempty"`
	// Sub is the line under the hold: for how long.
	Sub string `json:"sub,omitempty"`
}

// dockDevice is the machine the report was built on. There is one device:
// the one running this command. Its hostname is an identifier and is included
// only with identifiers.
type dockDevice struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Hostname string `json:"hostname,omitempty"`
	// Text is the whole line: which computer, and its role from a record.
	// HostText is the same line naming the hostname, only with identifiers.
	Text     string `json:"text"`
	HostText string `json:"hostText,omitempty"`
}

// dockLane is one lane from the last verified listing. keepgoing records a
// lane's state and a hashed key, not its title, its thread or the route it is
// on. Title is reserved for a later change that records one privately.
type dockLane struct {
	Number int    `json:"number"`
	State  string `json:"state"` // working, waiting or failed
	Phrase string `json:"phrase"`
	// Title is always empty in this schema; TitleEvidence says why.
	Title         string `json:"title,omitempty"`
	TitleEvidence string `json:"titleEvidence"`
	// Loop names the registered loop this lane is, when it is one; LoopKey
	// is that loop's key in this report.
	Loop    string `json:"loop,omitempty"`
	LoopKey string `json:"loopKey,omitempty"`
	// Key is the lane's hashed key, included only with identifiers.
	Key string `json:"key,omitempty"`
}

type dockLanes struct {
	// current, held, stale, none_recorded or unknown, as dockChildren.
	Availability string     `json:"availability"`
	Lanes        []dockLane `json:"lanes"`
	Omitted      int        `json:"omitted"`
	// Heading and Footer are the lines above and below the rows.
	Heading string `json:"heading"`
	Footer  string `json:"footer,omitempty"`
}

// dockEvent is one retained history entry in plain words.
type dockEvent struct {
	At         time.Time `json:"at"`
	AgeSeconds int64     `json:"ageSeconds"`
	// Tone is routine, key, hold or problem.
	Tone   string `json:"tone"`
	Phrase string `json:"phrase"`
	Detail string `json:"detail,omitempty"`
	// Folded counts further checks with the same result that the history
	// folded into this entry, since FoldedSince.
	Folded      uint64     `json:"folded,omitempty"`
	FoldedSince *time.Time `json:"foldedSince,omitempty"`
	// When is the entry's time; FoldedText says how many more and since when.
	When       dockText `json:"when"`
	FoldedText dockText `json:"foldedText,omitempty"`
}

type dockEvents struct {
	// known, none_recorded or unknown.
	Availability string      `json:"availability"`
	Events       []dockEvent `json:"events"`
	Retained     int         `json:"retained"`
	// Pruned says older history may have been pruned or dropped.
	Pruned bool `json:"pruned"`
	// Footer says how many entries are shown of how many retained.
	Footer string `json:"footer,omitempty"`
}

// dockReasons words each fixed cause. A cause outside this table reads as not
// in the fixed vocabulary; it is never guessed from text.
var dockReasons = map[string]string{
	"disabled":                  "the loop is off",
	"paused":                    "the loop is paused",
	"awaiting_user":             "the loop is held for a person",
	"completed":                 "the loop is finished",
	"stop":                      "a stop is requested",
	"idle":                      "the session was idle",
	"active":                    "the session was working",
	"retrying":                  "the provider was retrying the turn",
	"retry_unverified":          "the provider's retry state was unverified",
	"retry_silent":              "the provider's retry went silent",
	"handoff_deferred":          "a move to another route was put off for lack of time",
	"deadline":                  "its deadline was reached",
	"send_limit":                "its limit on messages sent was reached",
	"git_limit":                 "no Git progress within its time limit",
	"stall_limit":               "no progress after repeated nudges",
	"stall_limit_held":          "no progress after nudges held back because the turn looked hung",
	"nudge_held":                "a nudge held back because the turn looked hung",
	"hung_wait":                 "the turn looked hung; nudges are held back",
	"restricted":                "the provider denied permission for this request or account",
	"refused_continue":          "the model or provider declined the turn; a continue was sent in the same session",
	"refused_wait":              "the model or provider declined the turn; waiting before a continue",
	"refusal_ask":               "the model or provider declined turn after turn; a person is asked how to proceed",
	"decline_acknowledged":      "resumed by a person after a restriction; one re-prompt",
	"stall":                     "no progress for a while",
	"child_input":               "a lane is waiting for an answer",
	"child_busy":                "lanes are working",
	"child_error":               "lanes failed",
	"child_unverified":          "the state of its lanes could not be verified",
	"input":                     "the agent is waiting on a person",
	"limit_wait":                "a usage limit, waiting for its reset",
	"limit_no_fallback":         "a usage limit with no other route able to take over",
	"limit_failover_disabled":   "a usage limit, with automatic moves off",
	"limit_refusal_unrecovered": "a usage limit, with no move while a refusal is unrecovered",
}

func dockReason(cause string) string {
	if s, ok := dockReasons[cause]; ok {
		return s
	}
	return ""
}

// dockDecisionPhrase joins the recorded action with its fixed cause.
func dockDecisionPhrase(action, outcome string, dryRun bool, cause string) string {
	verb := dockAction(&state.Check{Action: action, Outcome: outcome, DryRun: dryRun})
	if action == "none" && outcome == "succeeded" && !dryRun {
		verb = "Sent nothing"
	}
	if reason := dockReason(cause); reason != "" {
		return verb + ": " + reason
	}
	return verb
}

// dockLastDecision reads the last check from status. The cause and the start
// of an unbroken run of the same result come from the retained history when
// its newest check is that same check; otherwise the cause is the fixed
// vocabulary of the recorded reason and no run is claimed.
func dockLastDecision(st *statusOut, tl *timelineReport, now time.Time) dockDecision {
	d := dockDecision{Availability: "unknown", Phrase: "Unknown"}
	if st == nil || st.State.LastCheck == nil || st.State.CheckedAt.IsZero() || !st.State.LastCheck.At.Equal(st.State.CheckedAt) {
		if st != nil && st.State.CheckedAt.IsZero() {
			d.Phrase = "No check recorded"
		}
		return d
	}
	c := st.State.LastCheck
	at := c.At
	d.Availability, d.At, d.AgeSeconds, d.Cause = "known", &at, secondsSince(now, at), incident.Cause(c.Reason)
	if ticks := dockTicks(tl); len(ticks) > 0 && ticks[len(ticks)-1].CheckStartedAt.Equal(at) {
		last := ticks[len(ticks)-1]
		d.Cause = last.Cause
		var since time.Time
		if last.Collapsed != nil && last.Collapsed.Count > 0 {
			since = last.Collapsed.FirstAt
		}
		// A folded run keeps its first check as its own record just before.
		if len(ticks) > 1 && sameResult(ticks[len(ticks)-2], last) {
			since = ticks[len(ticks)-2].CheckStartedAt
		}
		if !since.IsZero() && since.Before(at) {
			d.Since, d.SinceSeconds = &since, secondsSince(now, since)
		}
	}
	d.Phrase = dockDecisionPhrase(c.Action, c.Outcome, c.DryRun, d.Cause)
	d.Sub = txt(dockAgo(now, at))
	if d.Since != nil {
		d.Sub = txt(d.Sub, " \u00B7 this result since ", tm(*d.Since, "clock"))
	}
	return d
}

func sameResult(a, b incident.Record) bool {
	return a.Action == b.Action && a.Cause == b.Cause && a.Outcome == b.Outcome && a.DryRun == b.DryRun && a.TargetKey == b.TargetKey
}

// dockTicks returns the retained check records, oldest first.
func dockTicks(tl *timelineReport) []incident.Record {
	var out []incident.Record
	if tl == nil {
		return out
	}
	for _, e := range tl.Events {
		if e.Incident != nil && e.Incident.Kind == "tick" {
			out = append(out, *e.Incident)
		}
	}
	return out
}

// dockFlagSince finds the newest retained record in which a control flag was
// entered and has not been cleared since. ok is false when retention or a
// missing history leaves that unknown.
func dockFlagSince(tl *timelineReport, flag func(*incident.Controls) bool) (incident.Record, bool) {
	if tl == nil {
		return incident.Record{}, false
	}
	for i := len(tl.Events) - 1; i >= 0; i-- {
		r := tl.Events[i].Incident
		if r == nil || r.ControlAfter == nil {
			continue
		}
		if !flag(r.ControlAfter) {
			return incident.Record{}, false
		}
		if r.ControlBefore == nil || flag(r.ControlBefore) {
			continue
		}
		return *r, true
	}
	return incident.Record{}, false
}

func dockRecordTime(r incident.Record) time.Time {
	if c := r.ControlChange; c != nil && !c.SaveObservedAt.IsZero() {
		return c.SaveObservedAt
	}
	return r.CheckStartedAt
}

// dockCurrentHold words the hold flags in force. Off is not a hold.
func dockCurrentHold(c *clientControls, updatedAt time.Time, tl *timelineReport, now time.Time) dockHoldState {
	if c == nil {
		return dockHoldState{State: "unknown", Phrase: "Unknown"}
	}
	var parts []string
	var flag func(*incident.Controls) bool
	add := func(set bool, phrase string, f func(*incident.Controls) bool) {
		if !set {
			return
		}
		parts = append(parts, phrase)
		if flag == nil {
			flag = f
		}
	}
	add(c.Stop, "stop requested", func(x *incident.Controls) bool { return x.Stop })
	add(c.Completed, "finished", func(x *incident.Controls) bool { return x.Completed })
	add(c.AwaitingUser, "held for a person", func(x *incident.Controls) bool { return x.AwaitingUser })
	add(c.Paused, "paused", func(x *incident.Controls) bool { return x.Paused })
	if len(parts) == 0 {
		return dockHoldState{State: "none", Phrase: "None"}
	}
	phrase := strings.Join(parts, ", ")
	h := dockHoldState{State: "held", Phrase: strings.ToUpper(phrase[:1]) + phrase[1:]}
	if r, ok := dockFlagSince(tl, flag); ok {
		at := dockRecordTime(r)
		h.Since, h.SinceEvidence, h.SinceSeconds = &at, "hold_recorded", secondsSince(now, at)
		h.Sub = "for " + dockSpan(now.Sub(at))
	} else if !updatedAt.IsZero() {
		at := updatedAt
		h.Since, h.SinceEvidence, h.SinceSeconds = &at, "control_changed", secondsSince(now, at)
		h.Sub = "for at least " + dockSpan(now.Sub(at))
	} else {
		h.Sub = "since an unknown time"
	}
	return h
}

// dockHoldFromTimeline is the awaiting-user hold's start, for needs-you.
func dockHoldFromTimeline(tl *timelineReport) *dockHold {
	r, ok := dockFlagSince(tl, func(x *incident.Controls) bool { return x.AwaitingUser })
	if !ok {
		return nil
	}
	h := &dockHold{At: dockRecordTime(r), Cause: r.Cause}
	if c := r.ControlChange; c != nil {
		h.Cause, h.Marked = "", c.Operation == "await-user"
	}
	return h
}

// dockDeviceOf describes this computer for one loop. The role is stated only
// from a record: the loop's last check, which is recorded in its state here.
func dockDeviceOf(st *statusOut, hostname string, identifiers bool, now time.Time) dockDevice {
	d := dockDevice{Name: "This computer", Role: "Holds this loop's records; no check is recorded here"}
	if st == nil {
		d.Role = "Holds this loop's registration; its records could not be read"
	} else if t := st.State.CheckedAt; !t.IsZero() {
		d.Role = "The loop's last check ran here, " + dockAgo(now, t)
	}
	d.Text = d.Name + " \u00B7 " + d.Role
	if identifiers && hostname != "" {
		d.Hostname = clip(hostname, dockMaxText)
		d.HostText = d.Hostname + " (this computer) \u00B7 " + d.Role
	}
	return d
}

// dockLaneList lists each lane of the last verified listing, most in need of
// a person first. keys maps a registration ID to that loop's key in the report.
func dockLaneList(r runsLoop, known bool, identifiers bool, current bool, now time.Time) dockLanes {
	c := dockChildWork(r, known)
	out := dockLanes{Availability: c.Availability, Lanes: []dockLane{}, Heading: "This loop's lanes"}
	if out.Availability != "current" && out.Availability != "held" {
		return out
	}
	rank := map[string]int{"awaiting_input": 0, "error": 1, "active": 2}
	children := append([]runsChild{}, r.Blocker.Children...)
	// Stable, so lanes of one state keep the listing's order.
	for i := 1; i < len(children); i++ {
		for j := i; j > 0 && rank[children[j].Status] < rank[children[j-1].Status]; j-- {
			children[j], children[j-1] = children[j-1], children[j]
		}
	}
	for i, child := range children {
		if len(out.Lanes) >= dockMaxLanes {
			out.Omitted++
			continue
		}
		l := dockLane{Number: i + 1, TitleEvidence: "not_recorded"}
		switch child.Status {
		case "active":
			l.State, l.Phrase = "working", "Working"
		case "awaiting_input":
			l.State, l.Phrase = "waiting", "Waiting for an answer"
		case "error":
			l.State, l.Phrase = "failed", "Failed"
		default:
			l.State, l.Phrase = "unknown", "Unknown"
		}
		if s := child.Supervised; s.Availability == recordRecorded && s.RegistrationID != "" {
			l.Loop, l.LoopKey = clip(s.Loop, dockMaxText), dockKey(s.RegistrationID)
		}
		if identifiers {
			l.Key = strings.TrimPrefix(child.Ref, "child:")
		}
		out.Lanes = append(out.Lanes, l)
	}
	out.Omitted += r.Blocker.OmittedChildren
	if len(out.Lanes) == 0 {
		return out
	}
	counts := []string{}
	for _, x := range []struct {
		n    int
		word string
	}{{c.Working, "working"}, {c.Stuck, "waiting for an answer"}, {c.Failed, "failed"}} {
		if x.n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", x.n, x.word))
		}
	}
	when := "listed "
	if c.Availability == "held" {
		when = "as of the check that held it, "
	}
	if c.CheckedAt != nil {
		when += dockAgo(now, *c.CheckedAt)
	} else {
		when += "at an unknown time"
	}
	out.Heading = "This loop's lanes \u00B7 " + strings.Join(counts, ", ") + " \u00B7 " + when
	out.Footer = "keepgoing records each lane's state, not its title or which route it is on"
	if current {
		out.Footer += "; they are listed under the loop's current route"
	}
	out.Footer += "."
	if out.Omitted > 0 {
		out.Footer = dockPlural(out.Omitted, "more lane is", "more lanes are") + " not listed. " + out.Footer
	}
	return out
}

// dockEffort is the configured effort level of an agent definition.
func dockEffort(a config.Agent) string {
	switch {
	case a.BB != nil:
		return clip(a.BB.Reasoning, 40)
	case a.Claude != nil:
		return clip(a.Claude.Effort, 40)
	}
	return ""
}

// dockRouteNames maps the history's hashed target keys to route titles.
type dockRouteNames map[string]string

func dockRouteNamesOf(chain *dockChainInput) dockRouteNames {
	names := dockRouteNames{}
	if chain == nil {
		return names
	}
	for _, cand := range append([]config.Candidate{chain.Primary}, chain.Candidates...) {
		ref := refOf(cand.Agent)
		title := dockRouteTitle(dockTool(ref.Adapter), ref.Model)
		if acct := dockTitleAccount(title, ref.Group, ref.Provider); acct != "" {
			title += " on " + acct
		}
		names[incident.TargetKey(cand.ID)] = clip(title, dockMaxText)
	}
	return names
}

func (n dockRouteNames) of(key string) string {
	if s, ok := n[key]; ok && s != "" {
		return s
	}
	return "a route no longer in its configuration"
}

// dockRecentEvents words the newest retained history entries, newest first.
func dockRecentEvents(tl *timelineReport, names dockRouteNames, now time.Time) dockEvents {
	out := dockEvents{Availability: "unknown", Events: []dockEvent{}}
	if tl == nil {
		return out
	}
	for _, s := range tl.Sources {
		if s.Source == incident.FileName && s.Availability == "unavailable" {
			return out
		}
		out.Pruned = out.Pruned || s.Dropped > 0
	}
	out.Pruned = out.Pruned || tl.Filter.ReachesPrunedHistory
	out.Retained = len(tl.Events) + tl.Omitted
	out.Availability = "none_recorded"
	// The history keeps the first and the latest check of an unbroken run of
	// identical checks as two records, with the rest folded into the latest.
	// They are one line here.
	var run *incident.Record
	for i := len(tl.Events) - 1; i >= 0; i-- {
		e := tl.Events[i]
		if r := e.Incident; r != nil && r.Kind == "tick" && run != nil && sameResult(*r, *run) && r.Collapsed == nil {
			last := &out.Events[len(out.Events)-1]
			at := e.At
			last.Folded, last.FoldedSince = last.Folded+1, &at
			last.FoldedText = dockFoldedText(last.Folded, at)
			run = nil
			continue
		}
		if len(out.Events) >= dockMaxEvents {
			break
		}
		run = nil
		ev := dockEvent{At: e.At, AgeSeconds: *secondsSince(now, e.At), Tone: "routine", When: txt(tm(e.At, "clock"))}
		switch {
		case e.Incident != nil:
			dockWordIncident(&ev, *e.Incident, names)
			if e.Incident.Kind == "tick" && e.Incident.Action == "none" {
				run = e.Incident
			}
		case e.Delivery != nil:
			dockWordDelivery(&ev, *e.Delivery)
		default:
			continue
		}
		out.Events = append(out.Events, ev)
		out.Availability = "known"
	}
	if len(out.Events) > 0 {
		entries := "retained entries."
		if out.Retained == 1 {
			entries = "retained entry."
		}
		out.Footer = fmt.Sprintf("%d of %d %s", len(out.Events), out.Retained, entries)
		if out.Pruned {
			out.Footer += " Older history has been pruned."
		}
	}
	return out
}

func dockFoldedText(n uint64, since time.Time) dockText {
	checks := "more checks"
	if n == 1 {
		checks = "more check"
	}
	return txt(fmt.Sprintf("%d %s with this result since ", n, checks), tm(since, "clock"))
}

func dockWordIncident(ev *dockEvent, r incident.Record, names dockRouteNames) {
	entered := enteredTimelineHold(r.ControlBefore, r.ControlAfter)
	switch r.Kind {
	case "tick":
		ev.Phrase = dockDecisionPhrase(r.Action, r.Outcome, r.DryRun, r.Cause)
		moved := r.TargetKeyAfter != r.TargetKey && r.TargetKeyAfter != "unavailable"
		switch {
		case r.Outcome == "failed":
			ev.Tone = "problem"
		case (r.Action == "handoff" || r.Action == "switch_back") && moved && !r.DryRun:
			ev.Tone, ev.Detail = "key", "to "+names.of(r.TargetKeyAfter)
		case entered && !r.DryRun:
			ev.Tone = "hold"
		case timelineLimitWait(r.Cause):
			ev.Tone = "problem"
		case r.Action == "wake" || r.Action == "nudge" || r.Action == "reprompt":
			ev.Detail = "Delivery is not confirmed."
		}
		if c := r.Collapsed; c != nil && c.Count > 0 {
			first := c.FirstAt
			ev.Folded, ev.FoldedSince = c.Count, &first
			ev.FoldedText = dockFoldedText(c.Count, first)
		}
	case "control":
		ev.Tone, ev.Phrase = "key", dockControlPhrase(r)
		if entered {
			ev.Tone = "hold"
		}
		if c := r.ControlChange; c != nil && c.OwnershipChanged {
			ev.Detail = "The loop's owner changed."
		}
	case "retarget":
		ev.Tone, ev.Phrase, ev.Detail = "key", "Pointed at another route by a command", "to "+names.of(r.TargetKeyAfter)
	default:
		ev.Phrase = "A history entry of a kind this page does not know"
	}
}

// dockControlPhrase words a saved control change from the flags before and
// after it, not from the operation's name.
func dockControlPhrase(r incident.Record) string {
	b, a := r.ControlBefore, r.ControlAfter
	if b == nil || a == nil {
		return "A control change was saved"
	}
	var parts []string
	add := func(before, after bool, set, cleared string) {
		switch {
		case !before && after:
			parts = append(parts, set)
		case before && !after:
			parts = append(parts, cleared)
		}
	}
	add(b.Enabled, a.Enabled, "turned on", "turned off")
	add(b.Stop, a.Stop, "stop requested", "stop request released")
	add(b.Paused, a.Paused, "paused", "pause released")
	add(b.AwaitingUser, a.AwaitingUser, "held for a person", "hold for a person released")
	add(b.Completed, a.Completed, "marked finished", "finished mark removed")
	if len(parts) == 0 {
		return "A control change was saved; no hold changed"
	}
	s := strings.Join(parts, ", ")
	by := " by a command"
	if c := r.ControlChange; c != nil && strings.HasPrefix(c.Operation, "panel_") {
		by = " from the panel"
	}
	return strings.ToUpper(s[:1]) + s[1:] + by
}

func dockWordDelivery(ev *dockEvent, d report.Delivery) {
	event := clip(strings.ReplaceAll(d.Event, "_", " "), 40)
	switch d.Outcome {
	case "http_accepted":
		ev.Phrase, ev.Detail = fmt.Sprintf("Report sent: %s", event), "Accepted by the receiver; whether a person saw it is not known."
	case "not_sent":
		ev.Phrase = fmt.Sprintf("Report not sent for lack of time: %s", event)
	default:
		ev.Tone, ev.Phrase = "problem", fmt.Sprintf("Report not confirmed: %s", event)
	}
}
