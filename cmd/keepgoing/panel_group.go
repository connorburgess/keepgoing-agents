package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Pause all: one review for every registered loop, then one confirmation
// that changes each loop on its own, only if its control record is still
// the one reviewed. The page (api/v1/loops/all/...) and client review|apply
// pause --all (the macOS menu) both use these functions, so they say the
// same. The review names each loop with a one-line effect and a total; it
// never repeats a loop's whole review. A partial result never reads as
// success.

const (
	// panelGroupKey stands for every loop in a change request's path.
	panelGroupKey = "all"
	// panelGroupShown is how many loops a review or result names before
	// "And N more".
	panelGroupShown = 8
)

// panelGroupActions are the changes offered for every loop at once.
var panelGroupActions = map[string]bool{"pause": true, "stop": true}

// panelGroupParallel bounds how many loops' lanes a review counts at once.
const panelGroupParallel = 8

// panelGroupItem is one loop in a group review.
type panelGroupItem struct {
	Key, Name, ID string
	Fits          bool   // the change fits and the record was read
	why           string // when not: already, busy or unreadable
	digest        string // the controlDigest of the record reviewed
	// stop: the loop's own stop options, from its own review (#209); nil
	// for a loop whose agent has no lanes, whose stop is the plain one.
	stop *stopChoices
}

// lanes and now say what this loop's own review offered: a stop of its
// lanes too, and a stop carried out at once.
func (it panelGroupItem) lanes() bool {
	if it.stop == nil {
		return false
	}
	for _, o := range it.stop.Options {
		if o.Lanes && !o.DropQueued {
			return true
		}
	}
	return false
}
func (it panelGroupItem) now() bool { return it.stop != nil && it.stop.Immediately }

// panelGroupReviewed is a group review and what its confirmation is bound to.
type panelGroupReviewed struct {
	preview panelPreview
	items   []panelGroupItem
}

// panelGroupTarget is one loop a group confirmation changes, and, for a
// stop, what its own review offered: its lanes too (Lanes) and at once (Now).
type panelGroupTarget struct {
	Key, Digest string
	Lanes, Now  bool
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// panelGroupReview reads every registered loop and words the review of one
// change to all of them. refused is set when there is nothing to confirm.
func panelGroupReview(ctx context.Context, registryDir string, reader *dockReader, action string, now time.Time) (panelGroupReviewed, *panelResult) {
	if !panelGroupActions[action] {
		return panelGroupReviewed{}, &panelResult{Outcome: "refused", Text: "This change cannot be made to every loop at once. Nothing was changed."}
	}
	dir, err := registryDirectory(registryDir)
	if err != nil {
		return panelGroupReviewed{}, &panelResult{Outcome: "refused", Text: "The list of loops could not be read. Nothing was changed.", Retry: true}
	}
	f, err := registry.Load(dir)
	if err != nil {
		return panelGroupReviewed{}, &panelResult{Outcome: "refused", Text: "The list of loops could not be read. Nothing was changed.", Retry: true}
	}
	// Names and order as the page shows them: loops that want attention first.
	report := reader.read(ctx, false)
	order := map[string]int{}
	phrase := map[string]string{}
	for i, l := range report.Loops {
		order[l.Key], phrase[l.Key] = i, l.Phrase
	}
	var items []panelGroupItem
	for _, e := range f.Entries {
		it := panelGroupItem{Key: dockKey(e.ID), Name: clip(e.Loop, dockMaxText), ID: e.ID}
		ctl, why := panelReadControl(e.StateDir)
		switch {
		case why != "" && strings.HasPrefix(why, "keepgoing is in the middle"):
			it.why = "busy"
		case why != "":
			it.why = "unreadable"
		case action == "pause" && ctl.Paused, action == "stop" && ctl.Stop:
			it.why = "already"
		default:
			it.Fits, it.digest = true, controlDigest(ctl)
		}
		items = append(items, it)
	}
	if len(items) == 0 {
		return panelGroupReviewed{}, &panelResult{Outcome: "refused", Text: "No loops are registered. Nothing was changed."}
	}
	// Those it changes first, then the rest, each in the page's order.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Fits != items[j].Fits {
			return items[i].Fits
		}
		oi, iok := order[items[i].Key]
		oj, jok := order[items[j].Key]
		if iok != jok {
			return iok
		}
		return oi < oj
	})
	if action == "stop" {
		panelGroupStopChoices(ctx, f.Entries, items)
	}
	fits, already, cannot := 0, 0, 0
	for _, it := range items {
		switch {
		case it.Fits:
			fits++
		case it.why == "already":
			already++
		default:
			cannot++
		}
	}
	var rest []string
	if already > 0 {
		rest = append(rest, fmt.Sprintf("%d %s already %s", already, plural(already, "is", "are"), panelGroupHeld[action]))
	}
	if cannot > 0 {
		rest = append(rest, fmt.Sprintf("%d cannot be reviewed now", cannot))
	}
	if fits == 0 {
		return panelGroupReviewed{}, &panelResult{Outcome: "refused", Text: "No loop can be " + panelDone[action] + " now: " + panelJoin(rest) + ". Nothing was changed.", Retry: cannot > 0}
	}
	summary := fmt.Sprintf("%ss %d of %d %s", panelActions[action], fits, len(items), plural(len(items), "loop", "loops"))
	if len(rest) > 0 {
		summary += "; " + panelJoin(rest)
	}
	summary += "."
	expires := now.Add(panelHoldFormTTL)
	p := panelPreview{Action: action, Key: panelGroupKey, Title: panelActions[action] + " all loops?", Lines: []dockText{},
		Confirm: fmt.Sprintf("%s %d %s", panelActions[action], fits, plural(fits, "Loop", "Loops")), ExpiresAt: expires,
		Lost:    "keepgoing could not confirm which loops were " + panelDone[action] + ". Check each loop's state before doing anything else.",
		Refused: "The panel refused this request before doing anything. Nothing was changed."}
	add := func(a ...any) { p.Lines = append(p.Lines, txt(a...)) }
	for _, l := range panelGroupItemLines(items, phrase, action) {
		add(l)
	}
	add(summary)
	if action == "stop" {
		var lines []string
		p.Stop, lines = panelGroupStopReview(items)
		for _, l := range lines {
			add(l)
		}
	} else {
		add("keepgoing sends each loop it pauses nothing until that loop is resumed. A check already running finishes. Routes, notes, other holds and ownership do not change.")
	}
	add("If a loop's control record changes before you confirm, that loop is not changed and you are asked to review it again; the others are " + panelDone[action] + ".")
	add("This review expires ", dockUntil(now, expires), ".")
	return panelGroupReviewed{preview: p, items: items}, nil
}

// panelGroupItemLines is a line per loop, up to panelGroupShown, then "And N
// more.", so a review of many loops stays short.
func panelGroupItemLines(items []panelGroupItem, phrase map[string]string, action string) []string {
	var out []string
	for i, it := range items {
		if i == panelGroupShown {
			out = append(out, fmt.Sprintf("And %d more.", len(items)-panelGroupShown))
			break
		}
		out = append(out, panelGroupItemLine(it, phrase[it.Key], action))
	}
	return out
}

func panelGroupItemLine(it panelGroupItem, phrase, action string) string {
	done := panelDone[action]
	name := it.Name
	if it.Fits && phrase != "" {
		name += " (" + phrase + ")"
	}
	switch {
	case it.Fits && action == "stop" && it.stop != nil && it.stop.Lanes != nil && *it.stop.Lanes > 0:
		n := *it.stop.Lanes
		more := ""
		if it.stop.LanesAtLeast {
			more = " or more"
		}
		return fmt.Sprintf("%s will be stopped; %d%s of its lanes %s running.", name, n, more, plural(n, "is", "are"))
	case it.Fits && action == "stop" && it.lanes() && it.stop.Lanes == nil && it.now():
		return name + " will be stopped; its lanes could not be counted from here."
	case it.Fits && action == "stop" && it.lanes() && it.stop.Lanes == nil:
		return name + " will be stopped; its lanes could not be counted from here, and its next check stops them."
	case it.Fits:
		return name + " will be " + done + "."
	case it.why == "already":
		return it.Name + " is already " + panelGroupHeld[action] + "."
	case it.why == "busy":
		return it.Name + " will not be " + done + ": keepgoing is in the middle of a check on it."
	}
	return it.Name + " will not be " + done + ": its control record could not be read."
}

// panelGroupHeld is how a loop that already has the hold is said to be.
var panelGroupHeld = map[string]string{"pause": "paused", "stop": "stopped"}

// panelGroupStopChoices reads each loop's own stop options, as its own
// review would (stopChoicesFor), several loops at once and within the
// review's time.
func panelGroupStopChoices(ctx context.Context, entries []registry.Entry, items []panelGroupItem) {
	byID := map[string]registry.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, panelGroupParallel)
	for i := range items {
		if !items[i].Fits {
			continue
		}
		cfg, ok := clientConfig(byID[items[i].ID])
		if !ok {
			continue
		}
		wg.Add(1)
		go func(it *panelGroupItem) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			it.stop, _ = stopChoicesFor(ctx, cfg, nil)
		}(&items[i])
	}
	wg.Wait()
}

// panelGroupStopReview words what stopping every loop does, from each
// loop's own options, and the options of the whole: the conversations
// alone, or with the lanes of the loops that have them. Deleting queued
// messages is never offered for every loop at once.
func panelGroupStopReview(items []panelGroupItem) (*stopChoices, []string) {
	fits, now, withLanes, counted, lanes, queued, atLeast := 0, 0, 0, 0, 0, 0, false
	for _, it := range items {
		if !it.Fits {
			continue
		}
		fits++
		if it.now() {
			now++
		}
		if it.lanes() {
			withLanes++
			if it.stop.Lanes != nil {
				counted++
				lanes += *it.stop.Lanes
				atLeast = atLeast || it.stop.LanesAtLeast
			}
		}
		if it.stop != nil && it.stop.Queued != nil {
			queued += *it.stop.Queued
		}
	}
	var lines []string
	switch {
	case now == fits:
		lines = append(lines, "Each stop is carried out when you confirm, as a check does it; this review cannot confirm that the sessions stopped. If a check of a loop is running then, that check or the next one does it.")
	case now == 0:
		lines = append(lines, "Each loop's next check carries out its stop; this review cannot confirm that the sessions stopped.")
	default:
		lines = append(lines, fmt.Sprintf("%d of the stops are carried out when you confirm, as a check does it; the other %d at %s next check.", now, fits-now, plural(fits-now, "its", "their")))
	}
	var c *stopChoices
	if now > 0 || withLanes > 0 {
		c = &stopChoices{Immediately: now > 0, Options: []stopOption{{Label: "Stop the Conversations", Lanes: false}}}
	}
	if withLanes > 0 {
		c.Options = append(c.Options, stopOption{Label: "Stop Them and Their Lanes", Lanes: true})
		line := fmt.Sprintf("Stop Them and Their Lanes also stops the lanes of the %d %s that %s them", withLanes, plural(withLanes, "loop", "loops"), plural(withLanes, "has", "have"))
		if withLanes == fits {
			line = "Stop Them and Their Lanes also stops each loop's lanes"
		}
		more := ""
		if atLeast {
			more = " or more"
		}
		switch {
		case counted == 0:
			line += "; how many are running could not be counted from here."
		case counted == withLanes:
			line += fmt.Sprintf(": %d%s %s running now.", lanes, more, plural(lanes, "is", "are"))
		default:
			line += fmt.Sprintf(": %d%s running now in the %d whose lanes could be counted.", lanes, more, counted)
		}
		if withLanes < fits {
			line += " The others stop their conversation alone."
		}
		lines = append(lines, line, "Each loop's lanes are stopped as its own stop would: 3 levels down, 50 threads in all, leaving another loop's lanes to it.")
	} else {
		lines = append(lines, "This stops each conversation only: no loop here has lanes keepgoing can stop.")
	}
	if queued > 0 {
		lines = append(lines, fmt.Sprintf("%d %s queued in lanes that have not started. Stopping the lanes keeps them, and BB may run them; to delete them, stop each loop on its own.", queued, plural(queued, "message is", "messages are")))
	}
	lines = append(lines, "Nothing is sent to a stopped loop until it is resumed.")
	return c, lines
}

// panelGroupResult is what a group change came to: one result per loop, and
// a total. Outcome is done only when every loop was changed; partial when
// some were; otherwise the outcome they share, or refused.
type panelGroupResult struct {
	Outcome string        `json:"outcome"`
	Text    string        `json:"text"`
	Lines   []string      `json:"lines"`
	Retry   bool          `json:"retry,omitempty"`
	Loops   []panelLoopTo `json:"loops"`
}

// panelLoopTo is one loop's outcome in a group change.
type panelLoopTo struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Text    string `json:"text"`
	// For a stop that was saved: whether it was carried out at once, why
	// not when it was to be, what happened to its lanes, and whether it is
	// the loop of the conversation the command runs in.
	now   bool
	why   string
	lanes bool
	laneR *state.LaneStop
	own   bool
}

// panelGroupApply makes one reviewed change to each target, on its own:
// each is refused, inside its control lock, if its record is not the one
// reviewed. A refusal for one loop does not stop the others. Nothing is
// retried.
//
// A stop is two passes. The first saves every reviewed stop, checked against
// its reviewed record and not yet carried out: milliseconds a loop, so the
// time a change is given never leaves a loop unsaved. The second carries out
// the stops each loop's own review offered at once, in the time left; one cut
// off is left to that loop's next check. The loop of the conversation the
// command runs in, if any, is carried out last, so its stop cannot end the
// command before the others are carried out.
func panelGroupApply(ctx context.Context, registryDir, action, origin, caller string, targets []panelGroupTarget, names map[string]string, lanes bool) panelGroupResult {
	// One loop at a time: each change takes the registry's short lock, which
	// a second change at the same moment would find busy.
	out := panelGroupResult{}
	type later struct {
		i   int
		cfg *config.Config
	}
	var carry []later
	for _, t := range targets {
		name := names[t.Key]
		var res panelResult
		loop := panelLoopTo{Key: t.Key}
		entry, _, _, ok := panelEntry(registryDir, t.Key)
		if !ok {
			res = panelResult{Outcome: "refused", Text: "This loop is no longer registered. Nothing was changed."}
		} else {
			if name == "" {
				name = clip(entry.Loop, dockMaxText)
			}
			// Each loop as its own review offered it: its lanes too only if
			// it has them. Saved only: carried out in the second pass.
			opts := holdOptions{}
			if action == "stop" {
				loop.lanes = lanes && t.Lanes
				opts = holdOptions{Lanes: loop.lanes}
			}
			res = panelOutcome(action, panelApply(ctx, registryDir, entry.ID, action, origin, caller, t.Digest, opts))
			if action == "stop" && res.Outcome == "done" && t.Now {
				if cfg, ok := clientConfig(entry); ok {
					id, isSession := session.Of(cfg.Loop.Agent)
					loop.own = caller != "" && isSession && id.Kind == "bb" && id.ID == caller
					carry = append(carry, later{len(out.Loops), cfg})
				} else {
					loop.why = "sources_changed"
				}
			}
		}
		loop.Name, loop.Outcome, loop.Text = name, res.Outcome, res.Text
		out.Loops = append(out.Loops, loop)
	}
	// The second pass: the caller's own loop last.
	sort.SliceStable(carry, func(i, j int) bool { return !out.Loops[carry[i].i].own && out.Loops[carry[j].i].own })
	for _, c := range carry {
		r, _ := stopNow(ctx, c.cfg, stopNowWait, caller)
		l := &out.Loops[c.i]
		l.now, l.why, l.laneR = r.CarriedOut == "now", r.Why, r.Lanes
	}
	for i := range out.Loops {
		l := &out.Loops[i]
		l.Text = panelGroupLoopText(l.Name, action, panelResult{Outcome: l.Outcome, Text: l.Text}, *l)
	}
	return panelGroupTotal(action, out)
}

// panelGroupLoopText is one loop's line in a group result. A stop says
// whether it was carried out now, with what happened to its lanes, or was
// requested and left to the loop's next check, and why.
func panelGroupLoopText(name, action string, res panelResult, l panelLoopTo) string {
	if res.Outcome == "done" && action == "stop" {
		own := ""
		if l.own {
			own = " (the loop of this conversation, stopped last)"
		}
		if l.now {
			return name + own + ": stopped now" + panelGroupLaneText(l) + "."
		}
		with := ""
		if l.lanes {
			with = ", with its lanes"
		}
		why := map[string]string{
			"check_running":   "a check of it was running, so that check or the next one carries it out",
			"session_claimed": "another loop's check is acting on its session, so its next check carries it out",
			"bb_not_found":    "bb could not be found from here, so its next check carries it out",
			"cancelled":       "there was no time left to carry it out here, so its next check does",
		}[l.why]
		if why == "" && l.why != "" {
			why = "it could not be carried out here, so its next check does"
		}
		if why == "" {
			why = "its next check carries it out"
		}
		return name + own + ": stop requested" + with + "; " + why + "."
	}
	switch res.Outcome {
	case "done":
		return name + ": " + panelDone[action] + "."
	case "review_again":
		return name + ": changed since you reviewed it, so it was not " + panelDone[action] + "; review it again."
	case "uncertain":
		return name + ": keepgoing could not confirm whether it was " + panelDone[action] + "; check it before anything else."
	}
	return name + ": not " + panelDone[action] + ". " + res.Text
}

// panelGroupLaneText says what a stop carried out now did to the lanes it
// was asked to stop: every running one stopped, or not all reached.
func panelGroupLaneText(l panelLoopTo) string {
	if !l.lanes {
		return ""
	}
	r := l.laneR
	if r == nil {
		return "; its lanes were not reached, and its next check goes on"
	}
	failed := 0
	for _, n := range r.Failed {
		failed += n
	}
	if panelGroupLanesComplete(l) {
		if r.Stopped == 0 {
			return "; none of its lanes was running"
		}
		return fmt.Sprintf("; %d running %s stopped", r.Stopped, plural(r.Stopped, "lane", "lanes"))
	}
	if r.Stopped+failed > 0 {
		return fmt.Sprintf("; %d of %d lanes stopped, lanes not all reached, and its next check goes on", r.Stopped, r.Stopped+failed)
	}
	return "; lanes not all reached, and its next check goes on"
}

// panelGroupLanesComplete: a stop carried out now reached every lane it was
// asked to stop (or was not asked to stop any).
func panelGroupLanesComplete(l panelLoopTo) bool {
	if !l.lanes || !l.now {
		return true
	}
	r := l.laneR
	if r == nil {
		return false
	}
	failed := 0
	for _, n := range r.Failed {
		failed += n
	}
	return (r.Outcome == "done" || r.Outcome == "not_applicable") && failed == 0 && r.NotChecked == 0 && r.ListingsUnread == 0
}

// panelGroupTotal words the total, never as success unless every loop was
// changed, and lists the loops that were not first. For a stop it says how
// many were stopped now and how many requested, separately, and whether
// every lane asked for was reached.
func panelGroupTotal(action string, out panelGroupResult) panelGroupResult {
	counts := map[string]int{}
	now, requested, short := 0, 0, 0
	for _, l := range out.Loops {
		counts[l.Outcome]++
		if l.Outcome == "done" && action == "stop" {
			if l.now {
				now++
			} else {
				requested++
			}
			if !panelGroupLanesComplete(l) {
				short++
			}
		}
	}
	n, done := len(out.Loops), counts["done"]
	verb := strings.ToUpper(panelDone[action][:1]) + panelDone[action][1:]
	var failures []string
	if c := counts["review_again"]; c > 0 {
		failures = append(failures, fmt.Sprintf("%d %s a new review", c, plural(c, "needs", "need")))
		out.Retry = true
	}
	if c := counts["refused"]; c > 0 {
		failures = append(failures, fmt.Sprintf("%d %s refused", c, plural(c, "was", "were")))
	}
	if c := counts["uncertain"]; c > 0 {
		failures = append(failures, fmt.Sprintf("%d could not be confirmed", c))
	}
	switch {
	case action == "stop" && done == n:
		switch {
		case requested == 0:
			out.Text = fmt.Sprintf("Stopped %d %s now.", n, plural(n, "loop", "loops"))
		case now == 0:
			out.Text = fmt.Sprintf("Stop requested for %d %s; %s next check carries it out.", n, plural(n, "loop", "loops"), plural(n, "its", "each loop's"))
		default:
			out.Text = fmt.Sprintf("Stopped %d %s now; stop requested for %d, which %s next check carries out.", now, plural(now, "loop", "loops"), requested, plural(requested, "its", "their"))
		}
	case action == "stop":
		var parts []string
		if now > 0 {
			parts = append(parts, fmt.Sprintf("%d stopped now", now))
		}
		if requested > 0 {
			parts = append(parts, fmt.Sprintf("%d with a stop requested for %s next check", requested, plural(requested, "its", "their")))
		}
		out.Text = fmt.Sprintf("Of %d %s: %s.", n, plural(n, "loop", "loops"), panelJoin(append(parts, failures...)))
	case done == n:
		out.Text = fmt.Sprintf("%s %d %s.", verb, n, plural(n, "loop", "loops"))
	default:
		out.Text = fmt.Sprintf("%s %d of %d; %s.", verb, done, n, panelJoin(failures))
	}
	if short > 0 {
		out.Text += fmt.Sprintf(" Not every lane was reached for %d of them; %s next check goes on.", short, plural(short, "its", "their"))
	}
	switch {
	case done == n && short == 0:
		out.Outcome = "done"
	case done > 0:
		out.Outcome = "partial"
	case counts["uncertain"] > 0:
		out.Outcome = "uncertain"
	case counts["review_again"] > 0:
		out.Outcome = "review_again"
	default:
		out.Outcome = "refused"
	}
	// The caller's own loop first, then the loops that were not changed, so
	// they are always named.
	sorted := append([]panelLoopTo(nil), out.Loops...)
	rank := func(l panelLoopTo) int {
		switch {
		case l.own:
			return 0
		case l.Outcome != "done":
			return 1
		case !panelGroupLanesComplete(l):
			return 2
		}
		return 3
	}
	sort.SliceStable(sorted, func(i, j int) bool { return rank(sorted[i]) < rank(sorted[j]) })
	out.Lines = []string{}
	for i, l := range sorted {
		if i == panelGroupShown {
			out.Lines = append(out.Lines, fmt.Sprintf("And %d more.", n-panelGroupShown))
			break
		}
		out.Lines = append(out.Lines, l.Text)
	}
	return out
}
