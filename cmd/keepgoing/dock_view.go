package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The dock view is a plain-language projection of reads that already exist:
// registered status, status and runs. It adds no evidence of its own and
// never fills a gap with a default: what is missing is "unknown", and what is
// old is "stale".

const (
	dockSchemaVersion = 1
	dockMaxLoops      = registry.MaxEntries
	dockMaxNote       = 280 // runes of an owner's hold note
	dockMaxWindows    = 8   // windows per candidate
	dockMaxIssues     = 8
	dockMaxText       = 120 // runes of a name, alias, model or provider label
	dockMaxChain      = 32  // handoff chain entries per loop
)

type dockReport struct {
	SchemaVersion int `json:"schemaVersion"`
	// ReadOnly is true for every response of this schema: the page offers no
	// control and the server has no write endpoint.
	ReadOnly          bool         `json:"readOnly"`
	ReadAt            time.Time    `json:"readAt"`
	RefreshSeconds    int          `json:"refreshSeconds"`
	StaleAfterSeconds int          `json:"staleAfterSeconds"`
	Identifiers       bool         `json:"identifiers"`
	Registry          dockRegistry `json:"registry"`
	NeedsYou          []dockNeed   `json:"needsYou"`
	Loops             []dockLoop   `json:"loops"`
}

type dockRegistry struct {
	Availability string `json:"availability"` // available or unavailable
	Count        int    `json:"count"`
	Omitted      int    `json:"omitted"`
}

// dockNeed is one loop that is waiting on a person.
type dockNeed struct {
	Key    string `json:"key"`
	Loop   string `json:"loop"`
	Reason string `json:"reason"`
	// Note is the hold's note, in the words of whoever set it.
	Note  string     `json:"note,omitempty"`
	Since *time.Time `json:"since,omitempty"`
	// SinceEvidence is hold_recorded when Since is the time the hold was
	// saved, or control_changed when only the last control change is known,
	// which makes WaitedSeconds a lower bound.
	SinceEvidence string `json:"sinceEvidence,omitempty"`
	WaitedSeconds *int64 `json:"waitedSeconds,omitempty"`
	Action        string `json:"action"`
}

type dockLoop struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// State is one of supervising, waiting_for_you, waiting_on_limit,
	// checks_failing, paused, stopped, finished, off, not_checked_recently
	// or unknown. Phrase is its label, Detail one sentence about it.
	State       string           `json:"state"`
	Phrase      string           `json:"phrase"`
	Detail      string           `json:"detail"`
	Evidence    string           `json:"evidence"` // available, partial or unavailable
	RunningOn   dockTarget       `json:"runningOn"`
	Children    dockChildren     `json:"children"`
	LastCheck   dockCheck        `json:"lastCheck"`
	NextCheck   dockNext         `json:"nextCheck"`
	Fallbacks   dockFallbacks    `json:"fallbacks"`
	Chain       dockChain        `json:"chain"`
	Limits      []dockLimit      `json:"limits"`
	LimitWait   *dockLimitWait   `json:"limitWait,omitempty"`
	Issues      []string         `json:"issues"`
	Identifiers *dockIdentifiers `json:"identifiers,omitempty"`
}

type dockTarget struct {
	Availability string `json:"availability"`   // known or unknown
	Role         string `json:"role,omitempty"` // main or fallback
	Name         string `json:"name,omitempty"`
	Tool         string `json:"tool,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
}

type dockChildren struct {
	// current, held, stale, none_recorded or unknown.
	Availability string     `json:"availability"`
	Working      int        `json:"working"`
	Stuck        int        `json:"stuck"`
	Failed       int        `json:"failed"`
	CheckedAt    *time.Time `json:"checkedAt,omitempty"`
	AgeSeconds   *int64     `json:"ageSeconds,omitempty"`
	Phrase       string     `json:"phrase"`
}

type dockCheck struct {
	Availability string     `json:"availability"` // recent, stale, never or unknown
	At           *time.Time `json:"at,omitempty"`
	AgeSeconds   *int64     `json:"ageSeconds,omitempty"`
	Phrase       string     `json:"phrase"`
	FailedInRow  int        `json:"failedInRow,omitempty"`
}

type dockNext struct {
	Availability string     `json:"availability"` // known, in_progress or unknown
	At           *time.Time `json:"at,omitempty"`
	Phrase       string     `json:"phrase"`
	Detail       string     `json:"detail,omitempty"`
}

type dockFallbacks struct {
	// known, none_configured or unknown.
	Availability string `json:"availability"`
	Ready        int    `json:"ready"`
	Eligible     int    `json:"eligible"`
	Needed       int    `json:"needed"`
	Enough       bool   `json:"enough"`
	Phrase       string `json:"phrase"`
}

type dockUsageTarget struct {
	Name         string            `json:"name"`
	Availability string            `json:"availability"` // available, stale or unavailable
	ObservedAt   *time.Time        `json:"observedAt,omitempty"`
	AgeSeconds   *int64            `json:"ageSeconds,omitempty"`
	Windows      []dockUsageWindow `json:"windows"`
}

type dockUsageWindow struct {
	Name        string     `json:"name"`
	UsedPercent int        `json:"usedPercent"`
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
	Stale       bool       `json:"stale"`
}

type dockLimit struct {
	Label   string `json:"label"`
	Phrase  string `json:"phrase"`
	Used    *int64 `json:"used,omitempty"`
	Max     *int64 `json:"max,omitempty"`
	Reached bool   `json:"reached"`
	Unknown bool   `json:"unknown,omitempty"`
}

type dockLimitWait struct {
	Target   string     `json:"target"`
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
	Passed   bool       `json:"passed"`
}

// dockIdentifiers is shown only on request and holds nothing that registered
// status does not already print: the registration ID and the configured BB
// thread. Paths are never included.
type dockIdentifiers struct {
	RegistrationID string `json:"registrationId"`
	Thread         string `json:"thread,omitempty"`
}

// dockChain is the loop's handoff order as fallbacks list reads it: the main
// target first, then each fallback in configured order. Credentials, account
// directories, commands and paths are never part of it. Each entry carries a
// stable position and ID so later slices can add reorder, add and remove
// controls that call the guided fallbacks commands.
type dockChain struct {
	// known, none_configured or unknown.
	Availability string `json:"availability"`
	// HandoffOn lists the error classes that hand off automatically; empty
	// means automatic handoff is off.
	HandoffOn []string         `json:"handoffOn"`
	Entries   []dockChainEntry `json:"entries"`
	Omitted   int              `json:"omitted"`
}

type dockChainEntry struct {
	Position int    `json:"position"` // 0 is the main target
	ID       string `json:"id"`
	Tool     string `json:"tool,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Active   bool   `json:"active"`
	Disabled bool   `json:"disabled,omitempty"`
	// Readiness is ready, not_ready, not_probed, disabled or unknown.
	Readiness string          `json:"readiness"`
	Phrase    string          `json:"phrase"`
	ProbedAt  *time.Time      `json:"probedAt,omitempty"`
	ProbeAge  *int64          `json:"probeAgeSeconds,omitempty"`
	Usage     dockUsageTarget `json:"usage"`
}

// dockChainInput is the configuration-derived part of the chain plus the
// fallbacks list summary; Summary is nil when probe history was unreadable.
type dockChainInput struct {
	Primary    config.Candidate
	Candidates []config.Candidate
	FailoverOn []string
	Summary    *fallbacksOut
	PrimaryUse *usageView
}

// dockInput is everything one loop's card is built from.
type dockInput struct {
	Entry     registry.Entry
	Client    clientStatusReport
	Status    *statusOut // nil when status could not be read
	Runs      runsLoop
	RunsKnown bool
	Chain     *dockChainInput // nil when the configuration could not be read
	Hold      *dockHold       // nil when the retained history does not show the hold starting
}

// dockHold is the retained history record of an awaiting-user hold starting.
type dockHold struct {
	At     time.Time
	Cause  string // fixed vocabulary of incident.Cause
	Manual bool   // set by a person or agent with the await-user command
}

// dockHoldFrom finds the newest record in which the awaiting-user flag was
// entered and has not been cleared since. It returns nil when retention or a
// missing journal leaves that unknown.
func dockHoldFrom(j incident.Journal) *dockHold {
	for i := len(j.Records) - 1; i >= 0; i-- {
		r := j.Records[i]
		if r.ControlAfter == nil {
			continue
		}
		if !r.ControlAfter.AwaitingUser {
			return nil
		}
		if r.ControlBefore == nil || r.ControlBefore.AwaitingUser {
			continue
		}
		h := &dockHold{At: r.CheckStartedAt, Cause: r.Cause}
		if c := r.ControlChange; c != nil {
			h.Manual = true
			if !c.SaveObservedAt.IsZero() {
				h.At = c.SaveObservedAt
			}
		}
		return h
	}
	return nil
}

func dockKey(id string) string {
	h := sha256.Sum256([]byte("keepgoing-dock\x00" + id))
	return "l" + hex.EncodeToString(h[:6])
}

func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func secondsSince(now, t time.Time) *int64 {
	s := int64(now.Sub(t) / time.Second)
	return &s
}

var dockStatePriority = map[string]int{
	"waiting_for_you": 0, "unknown": 1, "checks_failing": 2, "not_checked_recently": 3, "waiting_on_limit": 4,
	"supervising": 5, "paused": 6, "off": 7, "stopped": 8, "finished": 9,
}

// buildDockReport sorts loops so the ones that want attention come first.
func buildDockReport(inputs []dockInput, now time.Time, refresh, staleAfter time.Duration, identifiers bool, registryOK bool, omitted int) dockReport {
	r := dockReport{SchemaVersion: dockSchemaVersion, ReadOnly: true, ReadAt: now, RefreshSeconds: int(refresh / time.Second),
		StaleAfterSeconds: int(staleAfter / time.Second), Identifiers: identifiers, NeedsYou: []dockNeed{}, Loops: []dockLoop{},
		Registry: dockRegistry{Availability: "unavailable"}}
	if !registryOK {
		return r
	}
	r.Registry = dockRegistry{Availability: "available", Count: len(inputs) + omitted, Omitted: omitted}
	for _, in := range inputs {
		loop, need := projectDockLoop(in, now, staleAfter, identifiers)
		r.Loops = append(r.Loops, loop)
		if need != nil {
			r.NeedsYou = append(r.NeedsYou, *need)
		}
	}
	sort.SliceStable(r.Loops, func(i, j int) bool {
		a, b := dockStatePriority[r.Loops[i].State], dockStatePriority[r.Loops[j].State]
		if a != b {
			return a < b
		}
		return strings.ToLower(r.Loops[i].Name) < strings.ToLower(r.Loops[j].Name)
	})
	sort.SliceStable(r.NeedsYou, func(i, j int) bool {
		a, b := r.NeedsYou[i].WaitedSeconds, r.NeedsYou[j].WaitedSeconds
		if a == nil || b == nil {
			return a != nil
		}
		return *a > *b
	})
	return r
}

func projectDockLoop(in dockInput, now time.Time, staleAfter time.Duration, identifiers bool) (dockLoop, *dockNeed) {
	l := dockLoop{Key: dockKey(in.Entry.ID), Name: clip(in.Entry.Loop, dockMaxText), Evidence: in.Client.Outcome,
		Limits: []dockLimit{}, Issues: []string{}}
	if identifiers {
		l.Identifiers = &dockIdentifiers{RegistrationID: in.Entry.ID}
	}
	snap := in.Client.Snapshot
	for _, code := range in.Client.Issues {
		if len(l.Issues) < dockMaxIssues {
			l.Issues = append(l.Issues, dockIssue(code))
		}
	}
	st := in.Status
	if snap != nil && st == nil && len(l.Issues) < dockMaxIssues {
		l.Issues = append(l.Issues, "Its full status could not be read; some details are unknown.")
	}

	// What it is running on.
	l.RunningOn = dockTarget{Availability: "unknown"}
	switch {
	case st != nil && st.TargetConfigured:
		a := st.TargetAgent
		l.RunningOn = dockTarget{Availability: "known", Role: dockRole(st.Target), Name: clip(st.Target, dockMaxText), Tool: dockTool(a.Adapter),
			Provider: clip(a.Provider, dockMaxText), Model: clip(a.Model, dockMaxText)}
		if identifiers && a.BBThread != "" && transferThreadID.MatchString(a.BBThread) {
			l.Identifiers.Thread = a.BBThread
		}
	case snap != nil && snap.Target != nil:
		l.RunningOn = dockTarget{Availability: "known", Role: dockRole(snap.Target.Alias), Name: clip(snap.Target.Alias, dockMaxText), Tool: dockTool(snap.Target.Adapter)}
		if identifiers && snap.Target.BBThread != "" {
			l.Identifiers.Thread = snap.Target.BBThread
		}
	}

	active := ""
	if st != nil {
		active = st.Target
	}
	l.Chain = dockHandoffChain(in.Chain, active, now)
	l.Children = dockChildWork(in.Runs, in.RunsKnown)
	l.LastCheck = dockLastCheck(st, now, staleAfter)
	l.NextCheck = dockNextCheck(snap)
	if st != nil {
		l.Fallbacks = dockFallbackReadiness(st)
		l.Limits = dockLimits(st, now)
		if w := st.LimitWait; w.State != "none" && w.ResetsAt != nil {
			l.LimitWait = &dockLimitWait{Target: clip(w.Target, dockMaxText), ResetsAt: w.ResetsAt, Passed: w.ResetPassed}
		}
	} else {
		l.Fallbacks = dockFallbacks{Availability: "unknown", Phrase: "Unknown"}
	}

	// State, in one phrase.
	var need *dockNeed
	switch {
	case snap == nil:
		l.State, l.Phrase = "unknown", "Can't be read"
		l.Detail = "keepgoing could not read this loop, so nothing about it is shown."
		if code := firstIssue(in.Client.Issues); code == "binding_unavailable" || code == "registration_changed" || code == "binding_changed" {
			need = &dockNeed{Reason: "Its registration no longer matches its configuration file.",
				Action: "Check that the configuration still exists and names this loop. If it moved, remove the registration and add it again."}
		}
	case snap.Controls == nil:
		l.State, l.Phrase = "unknown", "Unknown"
		l.Detail = "Its control record could not be read, so whether it is on, paused or stopped is unknown."
	default:
		c := snap.Controls
		switch {
		case c.Stop:
			l.State, l.Phrase, l.Detail = "stopped", "Stopped", "A stop was requested. keepgoing sends nothing more until it is resumed."
		case c.Completed:
			l.State, l.Phrase, l.Detail = "finished", "Finished", "Marked finished. keepgoing will not wake it again unless it is resumed."
		case c.AwaitingUser:
			l.State, l.Phrase = "waiting_for_you", "Waiting for you"
			need = dockHoldNeed(st, in.Hold)
			l.Detail = need.Reason
		case c.Paused:
			l.State, l.Phrase, l.Detail = "paused", "Paused", "Paused by a person. Nothing is sent until it is resumed."
		case !c.Enabled:
			l.State, l.Phrase, l.Detail = "off", "Off", "Supervision is turned off for this loop."
		case l.LastCheck.Availability == "never":
			l.State, l.Phrase, l.Detail = "not_checked_recently", "Not checked yet", "It is on, but no check has been recorded. Its scheduler may not be running."
		case l.LastCheck.Availability == "stale" || l.LastCheck.Availability == "unknown":
			l.State, l.Phrase, l.Detail = "not_checked_recently", "Not checked recently", "It is on, but the last check is old. Its scheduler or daemon may have stopped."
		case l.LimitWait != nil && !l.LimitWait.Passed:
			l.State, l.Phrase, l.Detail = "waiting_on_limit", "Waiting for a limit to reset", "The provider's usage limit was reached and no fallback could take over. It resumes on its own after the reset."
		case l.LastCheck.FailedInRow > 0:
			l.State, l.Phrase, l.Detail = "checks_failing", "Checks are failing", "Checks are running but could not observe or act on the session."
		default:
			l.State, l.Phrase, l.Detail = "supervising", "Supervising", "On, and checked recently."
			if l.Children.Availability == "current" && l.Children.Working > 0 {
				l.Detail = "On, and waiting while its child work runs."
			}
		}
	}
	if st != nil && st.NewThreads.Pending && need == nil {
		need = &dockNeed{Reason: "A new conversation it tried to create for a handoff could not be confirmed.",
			Action: "Find that conversation in BB, then settle it with keepgoing retarget."}
	}
	if need != nil {
		need.Key, need.Loop = l.Key, l.Name
		if need.Since != nil {
			need.WaitedSeconds = secondsSince(now, *need.Since)
		}
	}
	return l, need
}

func firstIssue(issues []string) string {
	if len(issues) == 0 {
		return ""
	}
	return issues[0]
}

func dockRole(alias string) string {
	if alias == state.Primary {
		return "main"
	}
	return "fallback"
}

func dockTool(adapter string) string {
	switch adapter {
	case "bb":
		return "BB thread"
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	case "process":
		return "Custom command"
	case "":
		return ""
	}
	return clip(adapter, dockMaxText)
}

var dockIssues = map[string]string{
	"registry_unavailable":     "The registry could not be read.",
	"registration_unavailable": "This registration is no longer in the registry.",
	"binding_unavailable":      "Its configuration file is missing, invalid, or no longer names this loop.",
	"binding_changed":          "Its configuration changed while it was being read.",
	"registration_changed":     "Its registration changed while it was being read.",
	"transfer_pending":         "A move to another conversation is in progress.",
	"target_binding_mismatch":  "Its configured conversation does not match the one it was moved to.",
	"control_unavailable":      "Its control record could not be read.",
	"state_unavailable":        "Its check history could not be read.",
	"target_unavailable":       "Its current target is not in its configuration.",
	"window_unavailable":       "Its operating hours could not be evaluated.",
	"goal_unavailable":         "Its goal record could not be read.",
	"cancelled":                "The read was cancelled.",
}

func dockIssue(code string) string {
	if s, ok := dockIssues[code]; ok {
		return s
	}
	return "Some evidence is unavailable."
}

// dockHoldNeed explains an awaiting-user hold from the fixed cause recorded
// when it started, and carries the note left with it. Nothing is inferred
// from the note's text.
func dockHoldNeed(st *statusOut, hold *dockHold) *dockNeed {
	n := &dockNeed{Reason: "It is held for a person.", Action: "Look at the conversation, then resume the loop when it is ready to continue."}
	cause := ""
	switch {
	case hold != nil && hold.Manual:
		n.Reason = "It was marked as waiting for a person."
	case hold != nil:
		cause = hold.Cause
	case st != nil && st.State.LastCheck != nil && st.State.LastCheck.Action == "await_user":
		cause = incident.Cause(st.State.LastCheck.Reason)
	}
	switch cause {
	case "child_input":
		n.Reason, n.Action = "A child conversation is waiting for an answer.", "Answer it in that conversation, then resume the loop."
	case "input":
		n.Reason, n.Action = "The agent asked a question or for permission.", "Answer it in the conversation, then resume the loop."
	case "deadline":
		n.Reason, n.Action = "It reached its deadline.", "Mark it finished, or extend the deadline in its configuration and resume."
	case "send_limit":
		n.Reason, n.Action = "It used every message it was allowed to send.", "Review the work, then raise the send limit or mark it finished."
	case "git_limit":
		n.Reason, n.Action = "No Git progress within its time limit.", "See why it stalled, then resume or mark it finished."
	case "stall_limit":
		n.Reason, n.Action = "No progress after repeated nudges.", "See why it stalled, then resume or mark it finished."
	}
	if hold != nil && !hold.At.IsZero() {
		at := hold.At
		n.Since, n.SinceEvidence = &at, "hold_recorded"
	}
	if st == nil {
		return n
	}
	if note := strings.TrimSpace(st.Control.Note); note != "" {
		n.Note = clip(note, dockMaxNote)
	}
	if t := st.Control.UpdatedAt; n.Since == nil && !t.IsZero() {
		since := t
		n.Since, n.SinceEvidence = &since, "control_changed"
	}
	return n
}

func dockChildWork(r runsLoop, known bool) dockChildren {
	c := dockChildren{Availability: "unknown", Phrase: "Unknown"}
	if !known {
		return c
	}
	b := r.Blocker
	c.CheckedAt, c.AgeSeconds = b.CheckedAt, b.AgeSeconds
	switch b.Availability {
	case blockerBlocked, blockerHeld, blockerListed, blockerNone:
		c.Availability = "current"
		if b.Availability == blockerHeld {
			c.Availability = "held"
		}
		for _, child := range b.Children {
			switch child.Status {
			case "active":
				c.Working++
			case "awaiting_input":
				c.Stuck++
			case "error":
				c.Failed++
			}
		}
		total := c.Working + c.Stuck + c.Failed + b.OmittedChildren
		switch {
		case total == 0:
			c.Phrase = "None running"
		default:
			parts := []string{}
			if c.Working > 0 {
				parts = append(parts, fmt.Sprintf("%d working", c.Working))
			}
			if c.Stuck > 0 {
				parts = append(parts, fmt.Sprintf("%d waiting for an answer", c.Stuck))
			}
			if c.Failed > 0 {
				parts = append(parts, fmt.Sprintf("%d failed", c.Failed))
			}
			if b.OmittedChildren > 0 {
				parts = append(parts, fmt.Sprintf("%d more not itemized", b.OmittedChildren))
			}
			c.Phrase = strings.Join(parts, ", ")
		}
		if b.Availability == blockerHeld {
			c.Phrase += " (as of the check that held it)"
		}
	case blockerUnknown:
		switch b.Reason {
		case "no_open_run", "no_child_evidence_recorded":
			c.Availability, c.Phrase = "none_recorded", "None recorded"
		case "last_check_older_than_stale_after":
			c.Availability, c.Phrase = "stale", "Stale: the last listing is too old to trust"
		}
	}
	return c
}

func dockLastCheck(st *statusOut, now time.Time, staleAfter time.Duration) dockCheck {
	c := dockCheck{Availability: "unknown", Phrase: "Unknown"}
	if st == nil {
		return c
	}
	c.FailedInRow = st.State.FailedChecks
	if st.State.CheckedAt.IsZero() {
		c.Availability, c.Phrase = "never", "No check recorded"
		return c
	}
	at := st.State.CheckedAt
	c.At, c.AgeSeconds = &at, secondsSince(now, at)
	c.Availability = "recent"
	if now.Sub(at) > staleAfter {
		c.Availability = "stale"
	}
	c.Phrase = "Checked"
	if lc := st.State.LastCheck; lc != nil && lc.At.Equal(at) {
		c.Phrase = dockAction(lc)
	}
	return c
}

func dockAction(c *state.Check) string {
	verb := map[string]string{
		"none": "Looked; nothing needed", "wake": "Woke the agent", "nudge": "Nudged the agent", "reprompt": "Asked the agent to try again",
		"await_user": "Held it for you", "stop": "Stopped the session", "handoff": "Handed off to a fallback", "switch_back": "Switched back to the main target",
	}[c.Action]
	if verb == "" {
		verb = "Checked"
	}
	switch {
	case c.DryRun || c.Outcome == "dry_run":
		return verb + " (dry run)"
	case c.Outcome == "failed" && c.Action == "none":
		return "Could not observe the session"
	case c.Outcome == "failed":
		return verb + ", but it failed"
	}
	return verb
}

func dockNextCheck(snap *clientSnapshot) dockNext {
	n := dockNext{Availability: "unknown", Phrase: "Unknown"}
	if snap == nil {
		return n
	}
	d := snap.Daemon
	switch {
	case d.State == "skipped":
		n.Detail = "the local daemon was not asked"
	case d.State != "responsive":
		n.Detail = "no keepgoing daemon answered"
	case d.Phase == "waiting" && d.NextCheckAt != nil:
		n.Availability, n.At, n.Phrase = "known", d.NextCheckAt, "Due"
	default:
		n.Availability, n.Phrase = "in_progress", "A check is running now"
	}
	return n
}

func dockFallbackReadiness(st *statusOut) dockFallbacks {
	switch {
	case st.Redundancy != nil:
		s := st.Redundancy
		f := dockFallbacks{Availability: "known", Ready: len(s.Healthy), Eligible: len(s.Eligible), Needed: s.Min, Enough: s.Meets}
		switch {
		case f.Eligible == 0:
			f.Phrase = "None can take over right now"
		default:
			f.Phrase = fmt.Sprintf("%d of %d ready", f.Ready, f.Eligible)
		}
		return f
	case len(st.Usage) == 0:
		// Usage rows are listed exactly when fallbacks are configured.
		return dockFallbacks{Availability: "none_configured", Phrase: "None set up"}
	}
	return dockFallbacks{Availability: "unknown", Phrase: "Unknown: probe history could not be read"}
}

func dockWindowName(key string) string {
	switch key {
	case "five_hour":
		return "5-hour"
	case "seven_day":
		return "7-day"
	}
	return clip(strings.ReplaceAll(key, "_", " "), 40)
}

func dockLimits(st *statusOut, now time.Time) []dockLimit {
	out := []dockLimit{}
	if a := st.SendAllowance; a != nil {
		l := dockLimit{Label: "Messages sent", Max: &a.Max, Reached: a.Reached}
		if a.Used != nil {
			l.Used = a.Used
			l.Phrase = fmt.Sprintf("%d of %d", *a.Used, a.Max)
		} else {
			l.Unknown, l.Phrase = true, fmt.Sprintf("Unknown of %d", a.Max)
		}
		out = append(out, l)
	}
	if st.Deadline != "" {
		l := dockLimit{Label: "Deadline", Phrase: clip(st.Deadline, 40), Reached: st.DeadlineReached}
		if t, err := time.Parse(time.RFC3339, st.Deadline); err == nil {
			l.Phrase = t.UTC().Format(time.RFC3339)
		}
		out = append(out, l)
	}
	if p := st.ProgressAllowance; p != nil {
		l := dockLimit{Label: "Time without Git progress", Reached: p.Due}
		switch {
		case p.Error != "":
			l.Unknown, l.Phrase = true, "Unknown (limit "+p.Timeout+")"
		case p.BaselinePending:
			l.Phrase = "Not measured yet (limit " + p.Timeout + ")"
		case p.ChangedAt != nil:
			l.Phrase = dockDuration(now.Sub(*p.ChangedAt)) + " of " + p.Timeout
		}
		out = append(out, l)
	}
	if n := st.NewThreads; n.Created > 0 || n.Pending {
		used, limit := int64(n.Created), int64(n.Max)
		out = append(out, dockLimit{Label: "New conversations created", Used: &used, Max: &limit, Reached: n.Created >= n.Max, Phrase: fmt.Sprintf("%d of %d", n.Created, n.Max)})
	}
	if len(out) == 0 && st.WorkBounds.Unbounded {
		out = append(out, dockLimit{Label: "", Phrase: "None set"})
	}
	return out
}

func dockDuration(d time.Duration) string {
	if d < 0 {
		return "0m"
	}
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

func dockUsageOf(v usageView) dockUsageTarget {
	t := dockUsageTarget{Name: clip(v.ID, dockMaxText), Availability: v.State, ObservedAt: v.ObservedAt, AgeSeconds: v.AgeSeconds, Windows: []dockUsageWindow{}}
	if t.Availability != "available" && t.Availability != "stale" {
		t.Availability = "unavailable"
	}
	for _, w := range v.Windows {
		if len(t.Windows) >= dockMaxWindows {
			break
		}
		t.Windows = append(t.Windows, dockUsageWindow{Name: dockWindowName(w.Key), UsedPercent: min(100, max(0, usagePercent(w.Used))), ResetsAt: w.ResetsAt, Stale: w.Stale})
	}
	return t
}

func dockHandoffChain(in *dockChainInput, active string, now time.Time) dockChain {
	c := dockChain{Availability: "unknown", HandoffOn: []string{}, Entries: []dockChainEntry{}}
	if in == nil {
		return c
	}
	for _, class := range in.FailoverOn {
		c.HandoffOn = append(c.HandoffOn, clip(class, 40))
	}
	entry := func(pos int, cand config.Candidate) dockChainEntry {
		ref := refOf(cand.Agent)
		return dockChainEntry{Position: pos, ID: clip(cand.ID, dockMaxText), Tool: dockTool(ref.Adapter), Provider: clip(ref.Provider, dockMaxText),
			Model: clip(ref.Model, dockMaxText), Active: cand.ID == active, Disabled: cand.Disabled, Readiness: "unknown", Phrase: "Unknown",
			Usage: dockUsageTarget{Name: clip(cand.ID, dockMaxText), Availability: "unavailable", Windows: []dockUsageWindow{}}}
	}
	main := entry(0, in.Primary)
	main.Readiness, main.Phrase = "main", "Main target"
	if in.PrimaryUse != nil {
		main.Usage = dockUsageOf(*in.PrimaryUse)
	}
	c.Entries = append(c.Entries, main)
	if len(in.Candidates) == 0 {
		c.Availability = "none_configured"
		return c
	}
	c.Availability = "known"
	rows := map[string]candidateRow{}
	if in.Summary != nil {
		for _, r := range in.Summary.Candidates {
			rows[r.ID] = r
		}
	}
	for i, cand := range in.Candidates {
		if len(c.Entries) > dockMaxChain {
			c.Omitted++
			continue
		}
		e := entry(i+1, cand)
		row, ok := rows[cand.ID]
		switch {
		case cand.Disabled:
			e.Readiness, e.Phrase = "disabled", "Turned off"
		case in.Summary == nil || !ok:
			c.Availability = "unknown"
		case row.Healthy:
			e.Readiness, e.Phrase = "ready", "Ready"
		case row.Last == nil:
			e.Readiness, e.Phrase = "not_probed", "Not checked yet"
		default:
			e.Readiness, e.Phrase = "not_ready", dockProbePhrase(row.Last.OK, row.Last.ErrorClass)
		}
		if ok {
			if row.Last != nil && !row.Last.At.IsZero() {
				at := row.Last.At
				e.ProbedAt, e.ProbeAge = &at, secondsSince(now, at)
			}
			e.Usage = dockUsageOf(row.Usage)
		}
		c.Entries = append(c.Entries, e)
	}
	return c
}

func dockProbePhrase(ok bool, class string) string {
	if ok {
		return "Last check passed, but too long ago to rely on"
	}
	switch class {
	case "rate_limit":
		return "At its usage limit"
	case "auth":
		return "Needs signing in again"
	case "network":
		return "Could not be reached"
	case "overloaded":
		return "Provider overloaded"
	}
	return "Last check failed"
}
