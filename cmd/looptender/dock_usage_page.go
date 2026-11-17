package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/state"
)

// The Usage page says, in counted sentences, what looptender did for each
// loop over the savings period: the checks that sent nothing, the wakes it
// withheld, the messages it sent, and what is recorded about the context
// window. It words the savings and contextWindow values of status --json
// (describeSavings, describeContext) and computes nothing of its own: a
// figure those values lack is left out, never derived here. Nothing on it
// is a warning, and it has no headline number, no money and no percentage
// of savings.

// dockUsagePath is the page's own read-only endpoint, beside dockAPIPath.
// It is filled by the same read as the main report, so reading it starts
// no check, probe, usage refresh or model call, and writes nothing.
const dockUsagePath = "api/v1/usage"

// dockMaxUsageResponse bounds the encoded Usage page. Every list in it is
// capped; the largest registry with every list at its cap measured well
// under three quarters of this (see TestDockUsagePageStaysUnderItsBound).
const dockMaxUsageResponse = 2 << 20

const (
	dockUsageMaxReasons = 6 // reasons named in one breakdown; the rest are summed
	dockUsageMaxResets  = 8 // session resets listed per loop; the rest are counted
)

// dockUsagePage is the api/v1/usage response.
type dockUsagePage struct {
	SchemaVersion int       `json:"schemaVersion"`
	ReadOnly      bool      `json:"readOnly"`
	ReadAt        time.Time `json:"readAt"`
	Complete      bool      `json:"complete"`
	// Lede says what the page is about, Period what "this week" means.
	Lede   string `json:"lede"`
	Period string `json:"period"`
	// Total is the plain sum across loops; Mechanism how a withheld wake
	// saves anything.
	Total     []string `json:"total"`
	Mechanism string   `json:"mechanism"`
	// Context is what looptender does and does not do about the context
	// window today; Chat the one sentence on an ordinary chat.
	Context []string        `json:"context"`
	Chat    string          `json:"chat"`
	Loops   []dockUsageLoop `json:"loops"`
}

// dockUsageLoop is one loop's part of the page. Brief is the three or four
// sentences its loop page shows; Did the full counted list; Window the
// context window sentences and Days its table, oldest first.
type dockUsageLoop struct {
	Key    string         `json:"key"`
	Name   string         `json:"name"`
	Brief  []string       `json:"brief"`
	Did    []string       `json:"did"`
	Window []string       `json:"window"`
	Days   []dockUsageDay `json:"days"`
}

// dockUsageDay is one UTC day of context readings, already worded.
type dockUsageDay struct {
	Day       string `json:"day"`
	Lowest    string `json:"lowest"`
	Highest   string `json:"highest"`
	Last      string `json:"last"`
	Window    string `json:"window"`
	Estimated bool   `json:"estimated,omitempty"`
}

const (
	dockUsageLede      = "This page is about what looptender did for your loops: the checks where it sent nothing, the messages it sent, and what is recorded about each conversation's context window. Allowance readings for each route are on the Routes page."
	dockUsageMechanism = "A plain timer would have woken the session at each withheld check, and each wake starts a turn that re-reads the conversation; looptender looked at its records first and sent nothing."
	dockUsageChat      = "In an ordinary chat nobody is woken at all, so there looptender adds messages rather than withholding them."
)

// dockUsageContext is what looptender does about context today, and no more.
var dockUsageContext = []string{
	"Withholding a wake keeps a turn out of the conversation, so the conversation grows more slowly.",
	"With prompt_goal_on_change set, a goal that has not changed is sent as one line instead of in full.",
	"A session reset is something a person runs, from the thread or a terminal.",
	"The automatic reset at a threshold (context_window.reset_at) is a preview only: this build never resets or compacts a conversation by itself.",
}

// dockUsageInput is what one loop's part is worded from: its projected key
// and name, and its status, nil when that could not be read.
type dockUsageInput struct {
	Key, Name string
	Status    *statusOut
}

// buildDockUsagePage words every loop's savings and context window values.
func buildDockUsagePage(loops []dockUsageInput, now time.Time, complete bool) dockUsagePage {
	p := dockUsagePage{SchemaVersion: dockSchemaVersion, ReadOnly: true, ReadAt: now, Complete: complete, Lede: dockUsageLede,
		Mechanism: dockUsageMechanism, Context: dockUsageContext, Chat: dockUsageChat, Total: []string{}, Loops: []dockUsageLoop{}}
	p.Period = dockUsagePeriod(loops, now)
	for _, in := range loops {
		p.Loops = append(p.Loops, dockUsageLoopOf(in))
	}
	p.Total = dockUsageTotals(loops)
	return p
}

// dockUsagePeriod names the period from the first readable value; every
// loop of one read shares it.
func dockUsagePeriod(loops []dockUsageInput, now time.Time) string {
	for _, in := range loops {
		if in.Status != nil && in.Status.Savings.Version != 0 {
			s := in.Status.Savings
			return fmt.Sprintf("This week means the last %d UTC days, from %s, today included. Every figure is a count from looptender's own records, except the estimate, which says so.",
				s.Period.Days, s.Period.From.UTC().Format("Mon 2 Jan"))
		}
	}
	return "This week means the last 7 UTC days, today included. Every figure is a count from looptender's own records, except the estimate, which says so."
}

// dockUsageReadable is a loop whose history could be counted.
func dockUsageReadable(st *statusOut) bool {
	return st != nil && st.Savings.Version != 0 && st.Savings.History.Availability == "recorded" && st.Savings.WithheldWakes.Total != nil
}

// dockUsagePicker is the picker row's sub-line: the week's withheld wakes
// across loops, as a plain count.
func dockUsagePicker(loops []dockUsageInput) string {
	n, any, partial := 0, false, false
	for _, in := range loops {
		if !dockUsageReadable(in.Status) {
			continue
		}
		any = true
		n += *in.Status.Savings.WithheldWakes.Total
		partial = partial || in.Status.Savings.History.Partial
	}
	switch {
	case !any:
		return "Nothing recorded"
	case n == 0:
		return "Nothing withheld this week"
	}
	return dockUsageAtLeast(partial, true) + dockUsageCount(n, "wake", "wakes") + " withheld this week"
}

// dockUsageTotals is the plain sum of counts across loops.
func dockUsageTotals(loops []dockUsageInput) []string {
	if len(loops) == 0 {
		return []string{"No loops are registered, so there is nothing to count."}
	}
	var nothing, withheld, sends, resets, counted, unread int
	partial := false
	for _, in := range loops {
		if !dockUsageReadable(in.Status) {
			unread++
			continue
		}
		s := in.Status.Savings
		counted++
		partial = partial || s.History.Partial
		withheld += *s.WithheldWakes.Total
		if s.ChecksSentNothing.Total != nil {
			nothing += *s.ChecksSentNothing.Total
		}
		if s.Sends.Total != nil {
			sends += *s.Sends.Total
		}
		resets += len(s.Resets.Items)
	}
	out := []string{}
	if counted == 0 {
		return append(out, "No loop's check history could be read, so nothing can be counted this week.")
	}
	least := dockUsageAtLeast(partial, false)
	out = append(out, fmt.Sprintf("Across %s this week: %s%s sent nothing, %s%s withheld, %s%s sent and %s%s.",
		dockUsageCount(counted, "loop", "loops"),
		least, dockUsageCount(nothing, "check", "checks"),
		least, dockUsageCount(withheld, "wake", "wakes"),
		least, dockUsageCount(sends, "message", "messages"),
		least, dockUsageCount(resets, "session reset", "session resets")))
	if unread > 0 {
		out = append(out, fmt.Sprintf("%s could not be read and %s not counted.", dockUsageCountOf(unread, "loop's history", "loops' histories"), dockUsageIsAre(unread)))
	}
	if partial {
		out = append(out, "Some of this week's history is missing for at least one loop, so these totals are at least the figures shown.")
	}
	return out
}

func dockUsageIsAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// dockUsageLoopOf words one loop.
func dockUsageLoopOf(in dockUsageInput) dockUsageLoop {
	out := dockUsageLoop{Key: in.Key, Name: in.Name, Brief: []string{}, Did: []string{}, Window: []string{}, Days: []dockUsageDay{}}
	st := in.Status
	if st == nil || st.Savings.Version == 0 {
		line := "Nothing is recorded for this loop that this page can read, so there is nothing to count."
		out.Brief, out.Did = []string{line}, []string{line}
		return out
	}
	s := st.Savings
	if s.History.Availability != "recorded" || s.WithheldWakes.Total == nil {
		line := "This loop's check history could not be read, so what looptender did this week cannot be counted."
		out.Brief, out.Did = []string{line}, []string{line}
	} else {
		withheld := dockUsageWithheld(s)
		sends := dockUsageSends(s)
		estimate := dockUsageEstimate(s)
		out.Did = append(out.Did, dockUsageNothing(s), withheld, sends)
		if c := dockUsageContextSent(s); c != "" {
			out.Did = append(out.Did, c)
		}
		out.Did = append(out.Did, dockUsageSpaced(s.SpacedWakes), estimate)
		if h := dockUsageHistory(s.History); h != "" {
			out.Did = append(out.Did, h)
		}
		out.Brief = []string{withheld, sends, estimate}
	}
	out.Window, out.Days = dockUsageContextOf(st)
	if len(out.Window) > 0 {
		out.Brief = append(out.Brief, out.Window[0])
	}
	return out
}

// dockUsageNothing: "173 checks sent nothing: 150 because …, 23 because …".
func dockUsageNothing(s savingsStatus) string {
	c := s.ChecksSentNothing
	if c.Total == nil {
		return "Checks that sent nothing: unavailable."
	}
	least := dockUsageAtLeast(s.History.Partial, true)
	if *c.Total == 0 {
		if least != "" {
			return "0 checks sent nothing in the history that remains for this week."
		}
		return "0 checks sent nothing this week."
	}
	return least + dockUsageCount(*c.Total, "check", "checks") + " sent nothing" + dockUsageBreakdown(c.ByReason, *c.Total) + "."
}

// dockUsageWithheld: "150 wakes withheld: …" or "No wakes were withheld
// this week."
func dockUsageWithheld(s savingsStatus) string {
	w := s.WithheldWakes
	if *w.Total == 0 {
		if s.History.Partial {
			return "No wakes were withheld in the history that remains for this week."
		}
		return "No wakes were withheld this week."
	}
	return dockUsageAtLeast(s.History.Partial, true) + dockUsageCount(*w.Total, "wake", "wakes") + " withheld" + dockUsageBreakdown(w.ByReason, *w.Total) + "."
}

// dockUsageBreakdown words the reasons of a count, largest first. Reasons
// with the same wording are joined; past dockUsageMaxReasons the rest are
// summed. A zero count is left out.
func dockUsageBreakdown(rs []savingsReason, total int) string {
	type part struct {
		text string
		n    int
	}
	var parts []part
	at := map[string]int{}
	for _, r := range rs {
		if r.Count <= 0 {
			continue
		}
		text := r.Text
		if text == "" {
			text = savingsReasonText(r.Reason)
		}
		if i, ok := at[text]; ok {
			parts[i].n += r.Count
			continue
		}
		at[text] = len(parts)
		parts = append(parts, part{text, r.Count})
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 && parts[0].n == total {
		return ", all because " + parts[0].text
	}
	words := []string{}
	rest := 0
	for i, p := range parts {
		if i >= dockUsageMaxReasons {
			rest += p.n
			continue
		}
		words = append(words, groupDigits(int64(p.n))+" because "+p.text)
	}
	if rest > 0 {
		words = append(words, groupDigits(int64(rest))+" for other reasons")
	}
	return ": " + strings.Join(words, ", ")
}

// dockUsageKinds words each kind of send.
var dockUsageKinds = map[string][2]string{
	"wake":        {"wake", "wakes"},
	"nudge":       {"nudge", "nudges"},
	"reprompt":    {"re-prompt", "re-prompts"},
	"turn_retry":  {"retry", "retries"},
	"handoff":     {"handoff to another route", "handoffs to another route"},
	"switch_back": {"switch back to the first route", "switches back to the first route"},
}

// dockUsageSends: "6 messages sent: 3 wakes, 2 nudges, 1 retry. Each is a
// turn looptender added to the conversation."
func dockUsageSends(s savingsStatus) string {
	m := s.Sends
	if m.Total == nil {
		return "Messages sent: unavailable."
	}
	if *m.Total == 0 {
		if s.History.Partial {
			return "No messages were sent in the history that remains for this week."
		}
		return "No messages were sent this week, so looptender added no turns."
	}
	words := []string{}
	for _, k := range m.ByKind {
		if k.Count <= 0 {
			continue
		}
		w, ok := dockUsageKinds[k.Kind]
		if !ok {
			w = [2]string{"other message", "other messages"}
		}
		words = append(words, dockUsageCount(k.Count, w[0], w[1]))
	}
	added := "Each is a turn looptender added to the conversation."
	if *m.Total == 1 {
		added = "It is a turn looptender added to the conversation."
	}
	text := dockUsageAtLeast(s.History.Partial, true) + dockUsageCount(*m.Total, "message", "messages") + " sent"
	if len(words) > 0 {
		text += ": " + strings.Join(words, ", ")
	}
	return text + ". " + added
}

// dockUsageContextSent says how much supervision context the sends carried.
func dockUsageContextSent(s savingsStatus) string {
	m := s.Sends
	if m.ContextBlocks == nil || m.ContextBytes == nil || *m.ContextBlocks == 0 {
		return ""
	}
	return fmt.Sprintf("%s carried the supervision context, %s bytes in all.",
		dockUsageCountOf(*m.ContextBlocks, "check's messages", "checks' messages"), groupDigits(*m.ContextBytes))
}

// dockUsageSpaced words the spaced wakes, or why they are unavailable.
func dockUsageSpaced(c savingsCounts) string {
	if c.Availability != "recorded" || c.Total == nil {
		if c.Reason == "record_unreadable" {
			return "Spaced wakes: unavailable; the record of spaced wakes could not be read."
		}
		return "Spaced wakes: unavailable; this loop's records hold no count of spaced wakes."
	}
	if *c.Total == 0 {
		return "No wakes were spaced this week."
	}
	return dockUsageCount(*c.Total, "wake", "wakes") + " spaced: at " + dockUsageThese(*c.Total) +
		" the session was idle and nothing had changed, so looptender spaced its wakes out where a plain timer would have sent. They are not added to the withheld wakes or the estimate."
}

func dockUsageThese(n int) string {
	if n == 1 {
		return "that check"
	}
	return "each of those checks"
}

// dockUsageEstimate words the estimate with its baseline, or why there is
// none. The word "estimate" is always in it.
func dockUsageEstimate(s savingsStatus) string {
	e := s.Estimate
	if e.Availability != "recorded" || e.WithheldWakes == nil || e.LowTokens == nil || e.HighTokens == nil || e.LowestReading == nil || e.HighestReading == nil {
		switch {
		case e.Reason == "record_unreadable":
			return "No estimate: the check history could not be read."
		case s.ContextNow.Reason == "not_supported":
			return "No estimate: this loop's agent reports no context window size."
		}
		return "No estimate: no context window reading was recorded this week."
	}
	if *e.WithheldWakes == 0 {
		return "Estimate: no tokens, since no wakes were withheld."
	}
	least := dockUsageAtLeast(s.History.Partial, false)
	amount := groupDigits(*e.LowTokens) + " to " + groupDigits(*e.HighTokens) + " tokens"
	if *e.LowTokens == *e.HighTokens {
		amount = groupDigits(*e.LowTokens) + " tokens"
	}
	by := "reported by BB"
	if e.Estimated {
		by = "some estimated by BB"
	}
	return fmt.Sprintf("Estimate: %s%s not re-read, from %s%s times the lowest (%s tokens) and the highest (%s tokens) context reading this week (%s), against a plain timer that wakes the session at every check. It is not tokens billed.",
		least, amount, least, dockUsageCount(*e.WithheldWakes, "withheld wake", "withheld wakes"), groupDigits(*e.LowestReading), groupDigits(*e.HighestReading), by)
}

// dockUsageHistory says why the counts are a floor.
func dockUsageHistory(h savingsHistory) string {
	if !h.Partial {
		return ""
	}
	why := []string{}
	for _, r := range h.PartialReasons {
		switch r {
		case "history_pruned":
			why = append(why, "older records were pruned")
		case "history_dropped":
			why = append(why, "an older build removed records")
		case "collapsed_span":
			why = append(why, "a run of repeated checks began before the week")
		}
	}
	text := "Some of this week's history is missing"
	if len(why) > 0 {
		text += " (" + strings.Join(why, "; ") + ")"
	}
	return text + ", so these counts are at least the figures shown."
}

// dockUsageWindow words the context window: the latest reading and its
// age, the week's lowest and highest, the session resets and the recent
// readings, and the days of the period as a table.
func dockUsageContextOf(st *statusOut) ([]string, []dockUsageDay) {
	s, c := st.Savings, st.ContextWindow
	out, days := []string{}, []dockUsageDay{}
	switch {
	case c.Reason == "not_supported" || s.ContextNow.Reason == "not_supported":
		out = append(out, "This loop's agent reports no context window size, so there is no context reading for it.")
	case c.Availability != "recorded" || s.ContextNow.Availability != "recorded":
		out = append(out, "No context window reading has been recorded for this thread yet.")
	default:
		out = append(out, "The latest context reading: "+s.ContextNow.Text+".")
		if e := s.Estimate; e.LowestReading != nil && e.HighestReading != nil {
			if *e.LowestReading == *e.HighestReading {
				out = append(out, "This week's readings were all "+groupDigits(*e.LowestReading)+" tokens.")
			} else {
				out = append(out, "This week's lowest reading was "+groupDigits(*e.LowestReading)+" tokens and the highest "+groupDigits(*e.HighestReading)+" tokens.")
			}
		} else if e.Reason == "no_readings" {
			out = append(out, "No reading was recorded this week.")
		}
		first := s.Period.From.UTC().Format(time.DateOnly)
		last := s.Period.From.UTC().AddDate(0, 0, s.Period.Days-1).Format(time.DateOnly)
		for _, d := range c.Days {
			if d.Day < first || d.Day > last || d.Count <= 0 {
				continue
			}
			days = append(days, dockUsageDayOf(d))
		}
		if n := len(c.Recent); n > 0 {
			vals := make([]string, 0, n)
			for _, r := range c.Recent {
				vals = append(vals, groupDigits(r.UsedTokens))
			}
			out = append(out, fmt.Sprintf("The last %s, oldest first: %s tokens.", dockUsageCountOf(n, "reading", "readings"), strings.Join(vals, ", ")))
		}
	}
	if r := dockUsageResets(s); r != "" {
		out = append(out, r)
	}
	return out, days
}

func dockUsageDayOf(d state.ContextDay) dockUsageDay {
	label := d.Day
	if t, err := time.Parse(time.DateOnly, d.Day); err == nil {
		label = t.Format("Mon 2 Jan")
	}
	return dockUsageDay{Day: label, Lowest: groupDigits(d.Lowest), Highest: groupDigits(d.Highest), Last: groupDigits(d.Last), Window: groupDigits(d.WindowTokens), Estimated: d.Estimated}
}

// dockUsageResets words each session reset with the size before and after.
func dockUsageResets(s savingsStatus) string {
	r := s.Resets
	if r.Availability != "recorded" {
		return ""
	}
	if len(r.Items) == 0 {
		return "No session resets this week."
	}
	parts := []string{}
	for i, x := range r.Items {
		if i >= dockUsageMaxResets {
			parts = append(parts, fmt.Sprintf("and %s more", dockUsageCountOf(len(r.Items)-i, "", "")))
			break
		}
		by := "from a terminal"
		if x.By == "thread" {
			by = "in the thread"
		}
		parts = append(parts, fmt.Sprintf("%s, run %s, %s", x.At.UTC().Format("Mon 2 Jan 15:04 UTC"), by, dockUsageBeforeAfter(x.Before, x.After)))
	}
	return dockUsageCount(len(r.Items), "session reset", "session resets") + ": " + strings.Join(parts, "; ") + "."
}

func dockUsageBeforeAfter(before, after *state.ContextReading) string {
	switch {
	case before != nil && after != nil:
		return groupDigits(before.UsedTokens) + " tokens before and " + groupDigits(after.UsedTokens) + " after"
	case before != nil:
		return groupDigits(before.UsedTokens) + " tokens before, size after not recorded"
	case after != nil:
		return "size before not recorded, " + groupDigits(after.UsedTokens) + " tokens after"
	}
	return "sizes before and after not recorded"
}

// dockUsageCount is "1 check" or "1,204 checks".
func dockUsageCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return groupDigits(int64(n)) + " " + many
}

// dockUsageCountOf is dockUsageCount with an empty noun allowed.
func dockUsageCountOf(n int, one, many string) string {
	return strings.TrimSpace(dockUsageCount(n, one, many))
}

func dockUsageAtLeast(partial, capital bool) string {
	switch {
	case !partial:
		return ""
	case capital:
		return "At least "
	}
	return "at least "
}
