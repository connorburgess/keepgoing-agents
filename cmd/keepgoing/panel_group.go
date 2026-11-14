package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/registry"
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
var panelGroupActions = map[string]bool{"pause": true}

// panelGroupItem is one loop in a group review.
type panelGroupItem struct {
	Key, Name, ID string
	Fits          bool   // the change fits and the record was read
	why           string // when not: already, busy or unreadable
	digest        string // the controlDigest of the record reviewed
}

// panelGroupReviewed is a group review and what its confirmation is bound to.
type panelGroupReviewed struct {
	preview panelPreview
	items   []panelGroupItem
}

// panelGroupTarget is one loop a group confirmation changes.
type panelGroupTarget struct {
	Key, Digest string
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
		case action == "pause" && ctl.Paused:
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
		rest = append(rest, fmt.Sprintf("%d %s already paused", already, plural(already, "is", "are")))
	}
	if cannot > 0 {
		rest = append(rest, fmt.Sprintf("%d cannot be reviewed now", cannot))
	}
	if fits == 0 {
		return panelGroupReviewed{}, &panelResult{Outcome: "refused", Text: "No loop can be paused now: " + panelJoin(rest) + ". Nothing was changed.", Retry: cannot > 0}
	}
	summary := fmt.Sprintf("Pauses %d of %d %s", fits, len(items), plural(len(items), "loop", "loops"))
	if len(rest) > 0 {
		summary += "; " + panelJoin(rest)
	}
	summary += "."
	expires := now.Add(panelHoldFormTTL)
	p := panelPreview{Action: action, Key: panelGroupKey, Title: "Pause all loops?", Lines: []dockText{},
		Confirm: fmt.Sprintf("Pause %d %s", fits, plural(fits, "Loop", "Loops")), ExpiresAt: expires,
		Lost:    "keepgoing could not confirm which loops were paused. Check each loop's state before doing anything else.",
		Refused: "The panel refused this request before doing anything. Nothing was changed."}
	add := func(a ...any) { p.Lines = append(p.Lines, txt(a...)) }
	for _, l := range panelGroupItemLines(items, phrase) {
		add(l)
	}
	add(summary)
	add("keepgoing sends each loop it pauses nothing until that loop is resumed. A check already running finishes. Routes, notes, other holds and ownership do not change.")
	add("If a loop's control record changes before you confirm, that loop is not changed and you are asked to review it again; the others are paused.")
	add("This review expires ", dockUntil(now, expires), ".")
	return panelGroupReviewed{preview: p, items: items}, nil
}

// panelGroupItemLines is a line per loop, up to panelGroupShown, then "And N
// more.", so a review of many loops stays short.
func panelGroupItemLines(items []panelGroupItem, phrase map[string]string) []string {
	var out []string
	for i, it := range items {
		if i == panelGroupShown {
			out = append(out, fmt.Sprintf("And %d more.", len(items)-panelGroupShown))
			break
		}
		out = append(out, panelGroupItemLine(it, phrase[it.Key]))
	}
	return out
}

func panelGroupItemLine(it panelGroupItem, phrase string) string {
	switch {
	case it.Fits && phrase != "":
		return it.Name + " (" + phrase + ") will be paused."
	case it.Fits:
		return it.Name + " will be paused."
	case it.why == "already":
		return it.Name + " is already paused."
	case it.why == "busy":
		return it.Name + " will not be paused: keepgoing is in the middle of a check on it."
	}
	return it.Name + " will not be paused: its control record could not be read."
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
}

// panelGroupApply makes one reviewed change to each target, on its own:
// each is refused, inside its control lock, if its record is not the one
// reviewed. A refusal for one loop does not stop the others. Nothing is
// retried.
func panelGroupApply(ctx context.Context, registryDir, action, origin, caller string, targets []panelGroupTarget, names map[string]string) panelGroupResult {
	var out panelGroupResult
	for _, t := range targets {
		name := names[t.Key]
		var res panelResult
		if entry, _, _, ok := panelEntry(registryDir, t.Key); !ok {
			res = panelResult{Outcome: "refused", Text: "This loop is no longer registered. Nothing was changed."}
		} else {
			if name == "" {
				name = clip(entry.Loop, dockMaxText)
			}
			res = panelOutcome(action, panelApply(ctx, registryDir, entry.ID, action, origin, caller, t.Digest))
		}
		out.Loops = append(out.Loops, panelLoopTo{Key: t.Key, Name: name, Outcome: res.Outcome, Text: panelGroupLoopText(name, action, res)})
	}
	return panelGroupTotal(action, out)
}

// panelGroupLoopText is one loop's line in a group result.
func panelGroupLoopText(name, action string, res panelResult) string {
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

// panelGroupTotal words the total, never as success unless every loop was
// changed, and lists the loops that were not first.
func panelGroupTotal(action string, out panelGroupResult) panelGroupResult {
	counts := map[string]int{}
	for _, l := range out.Loops {
		counts[l.Outcome]++
	}
	n, done := len(out.Loops), counts["done"]
	verb := strings.ToUpper(panelDone[action][:1]) + panelDone[action][1:]
	if done == n {
		out.Outcome, out.Text = "done", fmt.Sprintf("%s %d %s.", verb, n, plural(n, "loop", "loops"))
	} else {
		var parts []string
		if c := counts["review_again"]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s a new review", c, plural(c, "needs", "need")))
			out.Retry = true
		}
		if c := counts["refused"]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s refused", c, plural(c, "was", "were")))
		}
		if c := counts["uncertain"]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d could not be confirmed", c))
		}
		out.Text = fmt.Sprintf("%s %d of %d; %s.", verb, done, n, panelJoin(parts))
		switch {
		case done > 0:
			out.Outcome = "partial"
		case counts["uncertain"] > 0:
			out.Outcome = "uncertain"
		case counts["review_again"] > 0:
			out.Outcome = "review_again"
		default:
			out.Outcome = "refused"
		}
	}
	// The loops that were not changed first, so they are always named.
	sorted := append([]panelLoopTo(nil), out.Loops...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Outcome != "done" && sorted[j].Outcome == "done" })
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
