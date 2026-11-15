package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/incident"
	"github.com/looptender/looptender/internal/snapshot"
	"github.com/looptender/looptender/internal/state"
)

// savingsVersion is the layout of the savings object in status --json.
const savingsVersion = 1

// savingsDays is the length of the savings period: the last seven UTC days,
// today included, so that it lines up with the context record's day summaries.
const savingsDays = 7

// savingsBaseline names what "withheld" is compared with. It is the only
// baseline the savings figures use.
const savingsBaseline = "compared with a timer that sends at every check"

// savingsStatus is what looptender's restraint amounts to for one loop over
// the savings period, projected from records it already keeps: the incident
// history (incidents.json) and the context record in state.json. Every
// figure is a count of records, except Estimate, which is labelled as one.
// It never shows money or one headline number, and nothing reads it to
// decide anything. Version is savingsVersion.
type savingsStatus struct {
	Version int           `json:"version"`
	Period  savingsPeriod `json:"period"`
	// History is the incident history the counts come from.
	History savingsHistory `json:"history"`
	// WithheldWakes are checks that sent nothing while the loop was not
	// held and the session was observed idle, by fixed reason: a timer's
	// send at each would have started a turn.
	WithheldWakes savingsCounts `json:"withheldWakes"`
	// SpacedWakes are checks at which an idle wake was spaced, by fixed
	// code, from state.json idleSpacedDays. Unavailable: not_recorded when
	// that record is absent, record_unreadable when it is malformed. They
	// are not added to WithheldWakes or to Estimate.
	SpacedWakes savingsCounts `json:"spacedWakes"`
	// ChecksSentNothing are all checks that sent nothing, holds included,
	// by fixed reason.
	ChecksSentNothing savingsCounts  `json:"checksSentNothing"`
	Sends             savingsSends   `json:"sends"`
	ContextNow        savingsContext `json:"contextNow"`
	Resets            savingsResets  `json:"resets"`
	// Estimate is the one figure that is not a count of records.
	Estimate savingsEstimate `json:"estimate"`
	// Lines are the text lines status prints.
	Lines []string `json:"lines"`
}

type savingsPeriod struct {
	From time.Time `json:"from"` // start of the first UTC day; it ends at status's readAt
	Days int       `json:"days"`
}

// savingsHistory says whether the incident history could be read, and
// whether it is incomplete for the period: Partial counts are at least the
// figures shown. PartialReasons are fixed codes: history_pruned (retention
// removed records from inside the period), history_dropped (an older writer
// removed records with no time) and collapsed_span (a summary of folded
// checks began before the period, so its folded checks are not counted).
type savingsHistory struct {
	Availability   string   `json:"availability"`
	Reason         string   `json:"reason,omitempty"`
	Partial        bool     `json:"partial"`
	PartialReasons []string `json:"partialReasons"`
}

// savingsCounts is a count by fixed reason. Total and ByReason are absent
// when Availability is unavailable.
type savingsCounts struct {
	Availability string          `json:"availability"`
	Reason       string          `json:"reason,omitempty"`
	Total        *int            `json:"total,omitempty"`
	ByReason     []savingsReason `json:"byReason"`
	Text         string          `json:"text"`
}

// savingsReason is one fixed cause code from the incident history, its
// count, and its fixed wording.
type savingsReason struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	Text   string `json:"text"`
}

// savingsSends counts sends by kind: wake, nudge, reprompt, turn_retry
// (BB retried the failed turn instead of a re-prompt), handoff and
// switch_back. A nudge held back is not a send.
type savingsSends struct {
	Availability string        `json:"availability"`
	Reason       string        `json:"reason,omitempty"`
	Total        *int          `json:"total,omitempty"`
	ByKind       []savingsKind `json:"byKind"`
	Text         string        `json:"text"`
}

type savingsKind struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// savingsContext is the latest context window reading, as the contextWindow object of
// status has it, with BB's estimated flag and its age.
type savingsContext struct {
	Availability string     `json:"availability"`
	Reason       string     `json:"reason,omitempty"`
	UsedTokens   *int64     `json:"usedTokens,omitempty"`
	WindowTokens *int64     `json:"windowTokens,omitempty"`
	Estimated    bool       `json:"estimated,omitempty"`
	ReadAt       *time.Time `json:"readAt,omitempty"`
	AgeSeconds   *int64     `json:"ageSeconds,omitempty"`
	Text         string     `json:"text"`
}

// savingsResets lists the session resets in the period, oldest first, each
// with the nearest recorded context reading before and after it, when the
// context record still holds one.
type savingsResets struct {
	Availability string         `json:"availability"`
	Reason       string         `json:"reason,omitempty"`
	Items        []savingsReset `json:"items"`
	Text         string         `json:"text"`
}

type savingsReset struct {
	At time.Time `json:"at"`
	// By is thread or terminal, never an identity.
	By     string                `json:"by"`
	Before *state.ContextReading `json:"before,omitempty"`
	After  *state.ContextReading `json:"after,omitempty"`
}

// savingsEstimate is tokens not spent because wakes were withheld, as a
// range: withheld wakes times the lowest and the highest context reading
// recorded in the period. It is absent (unavailable: no_readings) when the
// period has no reading, never zero for want of one. Estimated says BB
// marked at least one of those readings as its own estimate.
type savingsEstimate struct {
	Label          string `json:"label"` // always "estimate"
	Availability   string `json:"availability"`
	Reason         string `json:"reason,omitempty"`
	Baseline       string `json:"baseline"`
	WithheldWakes  *int   `json:"withheldWakes,omitempty"`
	LowestReading  *int64 `json:"lowestReading,omitempty"`
	HighestReading *int64 `json:"highestReading,omitempty"`
	LowTokens      *int64 `json:"lowTokens,omitempty"`
	HighTokens     *int64 `json:"highTokens,omitempty"`
	Estimated      bool   `json:"estimated,omitempty"`
	Text           string `json:"text"`
}

// savingsSendKinds is the order sends are listed in.
var savingsSendKinds = []string{"wake", "nudge", "reprompt", "turn_retry", "handoff", "switch_back"}

// savingsReasonTexts words the causes the panel's table does not.
var savingsReasonTexts = map[string]string{
	"network_down": "the network was down",
	"unavailable":  "no reason was recorded",
	"other":        "a reason outside the fixed vocabulary",
}

func savingsReasonText(cause string) string {
	if s := dockReason(cause); s != "" {
		return s
	}
	if s, ok := savingsReasonTexts[cause]; ok {
		return s
	}
	return "a reason outside the fixed vocabulary"
}

// describeSavings projects the savings figures. journal is nil when
// incidents.json could not be read. ctx is the status context object and
// rec the context record it came from. It reads nothing and writes nothing.
func describeSavings(journal *incident.Journal, spaced savingsSpaced, rec *state.ContextRecord, ctx contextStatus, now time.Time) savingsStatus {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := day.AddDate(0, 0, -(savingsDays - 1))
	out := savingsStatus{Version: savingsVersion, Period: savingsPeriod{From: from, Days: savingsDays}}
	out.History = savingsHistory{Availability: "recorded", PartialReasons: []string{}}
	out.SpacedWakes = savingsSpacedOf(spaced, from, now)
	out.ContextNow = savingsContextOf(ctx)

	var withheld, nothing map[string]int
	var sends map[string]int
	var resets []savingsReset
	if journal == nil {
		out.History.Availability, out.History.Reason = "unavailable", "record_unreadable"
	} else {
		withheld, nothing, sends, resets = savingsTally(*journal, from, now, &out.History)
	}
	out.WithheldWakes = savingsCountsOf(withheld, out.History, "the session idle and the loop not held; "+savingsBaseline)
	out.ChecksSentNothing = savingsCountsOf(nothing, out.History, "holds included")
	out.Sends = savingsSendsOf(sends, out.History)
	out.Resets = savingsResetsOf(resets, rec, out.History)
	out.Estimate = savingsEstimateOf(out.WithheldWakes, out.History.Partial, rec, from, now)
	out.Lines = savingsLines(out)
	return out
}

// savingsSpaced is state.json's idleSpacedDays as savings reads it: one
// entry per UTC day with counts by fixed code. Availability is recorded,
// not_recorded (absent) or record_unreadable (malformed).
type savingsSpaced struct {
	Availability string
	Days         []savingsSpacedDay
}

type savingsSpacedDay struct {
	Day    string         `json:"day"`
	Counts map[string]int `json:"counts"`
}

// savingsSpacedLimit bounds the read of state.json for the spacing record.
const savingsSpacedLimit = 4 << 20

// loadSavingsSpaced reads only idleSpacedDays from state.json, without
// depending on the writer's types: an absent field is not_recorded, and
// any entry that is not a UTC day with non-negative counts makes the whole
// record unreadable. It never writes.
func loadSavingsSpaced(stateDir string) savingsSpaced {
	r, err := snapshot.OpenRecord(filepath.Join(stateDir, state.FileName))
	if err != nil {
		return savingsSpaced{Availability: "not_recorded"}
	}
	b, err := io.ReadAll(io.LimitReader(r, savingsSpacedLimit+1))
	r.Close()
	if err != nil || len(b) > savingsSpacedLimit {
		return savingsSpaced{Availability: "record_unreadable"}
	}
	return parseSavingsSpaced(b)
}

func parseSavingsSpaced(b []byte) savingsSpaced {
	var raw struct {
		IdleSpacedDays json.RawMessage `json:"idleSpacedDays"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return savingsSpaced{Availability: "record_unreadable"}
	}
	if len(raw.IdleSpacedDays) == 0 || string(raw.IdleSpacedDays) == "null" {
		return savingsSpaced{Availability: "not_recorded"}
	}
	var days []savingsSpacedDay
	if json.Unmarshal(raw.IdleSpacedDays, &days) != nil {
		return savingsSpaced{Availability: "record_unreadable"}
	}
	for _, d := range days {
		if _, err := time.Parse(time.DateOnly, d.Day); err != nil {
			return savingsSpaced{Availability: "record_unreadable"}
		}
		for _, n := range d.Counts {
			if n < 0 {
				return savingsSpaced{Availability: "record_unreadable"}
			}
		}
	}
	return savingsSpaced{Availability: "recorded", Days: days}
}

// savingsSpacedTexts words the spacing codes.
var savingsSpacedTexts = map[string]string{
	"idle_unchanged": "the session was idle and nothing had changed",
}

// savingsSpacedOf sums the period's UTC days by code.
func savingsSpacedOf(s savingsSpaced, from, now time.Time) savingsCounts {
	out := savingsCounts{Availability: "unavailable", ByReason: []savingsReason{}}
	switch s.Availability {
	case "recorded":
	case "record_unreadable":
		out.Reason, out.Text = "record_unreadable", "unavailable; the record of spaced wakes in state.json could not be read"
		return out
	default:
		out.Reason, out.Text = "not_recorded", "unavailable; no record of spaced wakes is kept yet"
		return out
	}
	first, last := from.Format(time.DateOnly), now.UTC().Format(time.DateOnly)
	m := map[string]int{}
	for _, d := range s.Days {
		if d.Day < first || d.Day > last {
			continue
		}
		for code, n := range d.Counts {
			m[code] += n
		}
	}
	out = savingsCountsOf(m, savingsHistory{Availability: "recorded"}, "checks at which an idle wake was spaced; "+savingsBaseline)
	for i, r := range out.ByReason {
		if t, ok := savingsSpacedTexts[r.Reason]; ok {
			out.ByReason[i].Text = t
		}
	}
	return out
}

// savingsTally counts the period's records. A dry run executed nothing and
// is not counted. A check that failed is not counted as sending nothing.
func savingsTally(j incident.Journal, from, now time.Time, h *savingsHistory) (withheld, nothing, sends map[string]int, resets []savingsReset) {
	withheld, nothing, sends = map[string]int{}, map[string]int{}, map[string]int{}
	inPeriod := func(t time.Time) bool { return !t.Before(from) && !t.After(now) }
	var tiered uint64
	for _, t := range []incident.TierPruned{j.Pruned.Key, j.Pruned.Condition, j.Pruned.Activity, j.Pruned.Routine} {
		tiered += t.Count
		if t.NewestAt != nil && !t.NewestAt.Before(from) {
			savingsPartial(h, "history_pruned")
		}
	}
	if j.Dropped > tiered {
		savingsPartial(h, "history_dropped")
	}
	for _, r := range j.Records {
		if r.Kind == "control" && r.ControlChange != nil {
			c := r.ControlChange
			if (c.Operation == sessionResetByThread || c.Operation == sessionResetByTerminal) && inPeriod(c.SaveObservedAt) {
				by := "terminal"
				if c.Operation == sessionResetByThread {
					by = "thread"
				}
				resets = append(resets, savingsReset{At: c.SaveObservedAt, By: by})
			}
			continue
		}
		if r.Kind != "tick" || r.DryRun || r.Outcome == "dry_run" || !inPeriod(r.CheckStartedAt) {
			continue
		}
		n := 1
		if c := r.Collapsed; c != nil && c.Count > 0 {
			if c.FirstAt.Before(from) {
				savingsPartial(h, "collapsed_span")
			} else {
				n += int(c.Count)
			}
		}
		heldNudge := r.Action == "nudge" && r.Cause == "nudge_held"
		switch {
		case r.Outcome != "succeeded":
		case r.Action == "none" || r.Action == "await_user" || heldNudge:
			nothing[r.Cause] += n
			if r.Action != "await_user" && timelineGate(r.ControlBefore) == "enabled" && !savingsHoldCause(r.Cause) && savingsObservedIdle(r.Observation) {
				withheld[r.Cause] += n
			}
		case r.Action == "reprompt" && (r.TurnRetry == "retried" || r.TurnRetry == "queued"):
			sends["turn_retry"] += n
		case containsString(savingsSendKinds, r.Action):
			sends[r.Action] += n
		}
	}
	sort.SliceStable(resets, func(a, b int) bool { return resets[a].At.Before(resets[b].At) })
	return withheld, nothing, sends, resets
}

// savingsObservedIdle says the check observed the session idle, so a
// timer's send there would have started a turn that reads the whole
// context. At a working, waiting, failing or unobserved session it would
// have queued or failed instead, so such a check is not a withheld wake.
func savingsObservedIdle(o incident.Observation) bool {
	return o.Availability == "observed" && o.State == "idle"
}

// savingsHoldCause is a cause that is itself a hold: a timer would not have
// sent then either, so the check is not counted as a withheld wake.
func savingsHoldCause(cause string) bool {
	switch cause {
	case "disabled", "paused", "awaiting_user", "completed", "stop":
		return true
	}
	return false
}

func savingsPartial(h *savingsHistory, reason string) {
	h.Partial = true
	if !containsString(h.PartialReasons, reason) {
		h.PartialReasons = append(h.PartialReasons, reason)
	}
}

// savingsCountsOf sorts reasons by count, then code, so the order is stable.
func savingsCountsOf(m map[string]int, h savingsHistory, qualifier string) savingsCounts {
	out := savingsCounts{Availability: h.Availability, Reason: h.Reason, ByReason: []savingsReason{}}
	if h.Availability != "recorded" {
		out.Text = "unavailable; incidents.json could not be read"
		return out
	}
	total := 0
	for cause, n := range m {
		total += n
		out.ByReason = append(out.ByReason, savingsReason{Reason: cause, Count: n, Text: savingsReasonText(cause)})
	}
	sort.Slice(out.ByReason, func(a, b int) bool {
		x, y := out.ByReason[a], out.ByReason[b]
		return x.Count > y.Count || x.Count == y.Count && x.Reason < y.Reason
	})
	out.Total = &total
	parts := make([]string, 0, len(out.ByReason))
	for _, r := range out.ByReason {
		parts = append(parts, fmt.Sprintf("%d %s", r.Count, r.Reason))
	}
	out.Text = savingsAtLeast(h) + dockPlural(total, "check", "checks") + " (" + qualifier + ")"
	if len(parts) > 0 {
		out.Text += ": " + strings.Join(parts, ", ")
	}
	return out
}

func savingsAtLeast(h savingsHistory) string {
	if h.Partial {
		return "at least "
	}
	return ""
}

func savingsSendsOf(m map[string]int, h savingsHistory) savingsSends {
	out := savingsSends{Availability: h.Availability, Reason: h.Reason, ByKind: []savingsKind{}}
	if h.Availability != "recorded" {
		out.Text = "unavailable; incidents.json could not be read"
		return out
	}
	total := 0
	var parts []string
	for _, k := range savingsSendKinds {
		if n := m[k]; n > 0 {
			total += n
			out.ByKind = append(out.ByKind, savingsKind{Kind: k, Count: n})
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	out.Total = &total
	out.Text = savingsAtLeast(h) + dockPlural(total, "send", "sends")
	if len(parts) > 0 {
		out.Text += ": " + strings.Join(parts, ", ")
	}
	return out
}

func savingsContextOf(c contextStatus) savingsContext {
	out := savingsContext{Availability: c.Availability, Reason: c.Reason, Text: c.Text}
	if c.Availability == "recorded" {
		out.UsedTokens, out.WindowTokens, out.Estimated, out.ReadAt, out.AgeSeconds = c.UsedTokens, c.WindowTokens, c.Estimated, c.ReadAt, c.AgeSeconds
		out.Text = c.Text + ", read " + c.Age
	}
	return out
}

// savingsResetsOf pairs each reset with the context readings either side of
// it. Readings of a target other than the record's are never in the record.
func savingsResetsOf(resets []savingsReset, rec *state.ContextRecord, h savingsHistory) savingsResets {
	out := savingsResets{Availability: h.Availability, Reason: h.Reason, Items: []savingsReset{}}
	if h.Availability != "recorded" {
		out.Text = "unavailable; incidents.json could not be read"
		return out
	}
	for _, r := range resets {
		if rec != nil {
			for i := range rec.Recent {
				x := rec.Recent[i]
				if x.At.Before(r.At) {
					r.Before = &x
				} else if r.After == nil {
					r.After = &x
				}
			}
		}
		out.Items = append(out.Items, r)
	}
	if len(out.Items) == 0 {
		out.Text = "none in the period"
		return out
	}
	parts := make([]string, 0, len(out.Items))
	for _, r := range out.Items {
		parts = append(parts, fmt.Sprintf("%s by %s, before %s, after %s", when(r.At), r.By, savingsReading(r.Before), savingsReading(r.After)))
	}
	out.Text = strings.Join(parts, "; ")
	return out
}

func savingsReading(r *state.ContextReading) string {
	if r == nil {
		return "not recorded"
	}
	return groupDigits(r.UsedTokens) + " tokens"
}

// savingsEstimateOf multiplies the withheld wakes by the lowest and highest
// reading of the period: the day summaries of its UTC days. It never
// borrows a reading from outside the period.
func savingsEstimateOf(w savingsCounts, partial bool, rec *state.ContextRecord, from, now time.Time) savingsEstimate {
	out := savingsEstimate{Label: "estimate", Availability: "unavailable", Baseline: savingsBaseline}
	if w.Total == nil {
		out.Reason, out.Text = "record_unreadable", "estimate unavailable; incidents.json could not be read"
		return out
	}
	first, last := from.Format(time.DateOnly), now.UTC().Format(time.DateOnly)
	var low, high int64
	found := false
	if rec != nil {
		for _, d := range rec.Days {
			if d.Day < first || d.Day > last || d.Count <= 0 {
				continue
			}
			if !found || d.Lowest < low {
				low = d.Lowest
			}
			if !found || d.Highest > high {
				high = d.Highest
			}
			found = true
			out.Estimated = out.Estimated || d.Estimated
		}
	}
	if !found {
		out.Estimated = false
		out.Reason, out.Text = "no_readings", "estimate unavailable; no context window reading was recorded in the period"
		return out
	}
	n := *w.Total
	lo, hi := int64(n)*low, int64(n)*high
	out.Availability, out.WithheldWakes, out.LowestReading, out.HighestReading, out.LowTokens, out.HighTokens = "recorded", &n, &low, &high, &lo, &hi
	out.Text = fmt.Sprintf("estimate: %s to %s tokens not spent on %s%s, %s (withheld wakes times the lowest and highest context window reading of the period, %s; not tokens billed)",
		groupDigits(lo), groupDigits(hi), savingsAtLeast(savingsHistory{Partial: partial}), dockPlural(n, "withheld wake", "withheld wakes"), savingsBaseline, contextSource(out.Estimated))
	return out
}

// savingsLines words the figures for status, one per line. The estimate is
// on its own line, labelled, with its baseline.
func savingsLines(s savingsStatus) []string {
	period := fmt.Sprintf("last %d UTC days", s.Period.Days)
	lines := []string{
		fmt.Sprintf("savings:   %s, measured from looptender's records; diagnostic only, no decision uses it", period),
		"  withheld wakes: " + s.WithheldWakes.Text,
		"  spaced wakes: " + s.SpacedWakes.Text,
		"  checks that sent nothing: " + s.ChecksSentNothing.Text,
		"  sends: " + s.Sends.Text,
		"  context window now: " + s.ContextNow.Text,
		"  session resets: " + s.Resets.Text,
		"  " + s.Estimate.Text,
	}
	if s.History.Partial {
		lines = append(lines, "  history: incomplete for the period ("+strings.Join(s.History.PartialReasons, ", ")+"); counts are at least those shown")
	}
	return lines
}

func printSavings(w io.Writer, s savingsStatus) {
	for _, l := range s.Lines {
		fmt.Fprintln(w, l)
	}
}
