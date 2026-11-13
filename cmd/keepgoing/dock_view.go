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
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The dock view is a plain-language projection of reads that already exist:
// registered status, status, runs and fallbacks list. It adds no evidence of
// its own and never fills a gap with a default: what is missing is "unknown",
// and what is old is "stale". Every phrase it can produce says only what the
// record behind it says; TestDockPhrasesFollowEvidence maps each record to
// its phrase, so a phrase that guesses has to be added there to pass.

const (
	dockSchemaVersion = 1
	dockMaxLoops      = registry.MaxEntries
	dockMaxNote       = 280 // runes of a hold note
	dockMaxWindows    = 8   // usage windows per route
	dockMaxIssues     = 8
	dockMaxText       = 120 // runes of a name, alias, model or provider label
	dockMaxChain      = 32  // fallbacks listed per loop
	dockMaxRoutes     = 128 // routes in the view across loops
	dockMaxRouteLoops = 64  // loops listed under one route, per list
)

type dockReport struct {
	SchemaVersion int `json:"schemaVersion"`
	// ReadOnly is true for every response of this schema: the page offers no
	// control and there is no write endpoint.
	ReadOnly          bool         `json:"readOnly"`
	ReadAt            time.Time    `json:"readAt"`
	RefreshSeconds    int          `json:"refreshSeconds"`
	StaleAfterSeconds int          `json:"staleAfterSeconds"`
	Identifiers       bool         `json:"identifiers"`
	Registry          dockRegistry `json:"registry"`
	// Complete is false when the read was cut off before every loop was read.
	// AllClear is true only when the registry was read, every loop in it was
	// read, and none needs a person: a page may say "nothing needs you" only
	// then. Unread counts loops that could not be read.
	Complete bool       `json:"complete"`
	AllClear bool       `json:"allClear"`
	Unread   int        `json:"unread"`
	NeedsYou []dockNeed `json:"needsYou"`
	Loops    []dockLoop `json:"loops"`
	// Routes is the same data grouped by route: a model on an account, as
	// configured. Each loop's chain entries name their route by key, so the
	// two views are one feed.
	Routes []dockRoute `json:"routes"`
}

type dockRegistry struct {
	Availability string `json:"availability"` // available or unavailable
	Count        int    `json:"count"`
	Omitted      int    `json:"omitted"`
}

// dockNeed is one loop a person should look at. Kind is held (it waits on a
// person) or unreadable (it could not be read, so whether it is held is
// unknown).
type dockNeed struct {
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Loop   string `json:"loop"`
	Reason string `json:"reason"`
	// Note is the hold's note, in the words of whoever set it. Registered
	// status withholds notes, so it is included only with identifiers;
	// NoteWithheld says one exists.
	Note         string     `json:"note,omitempty"`
	NoteWithheld bool       `json:"noteWithheld,omitempty"`
	Since        *time.Time `json:"since,omitempty"`
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
	// checks_failing, paused, stopped, finished, off, outside_hours,
	// not_checked_recently or unknown. Phrase is its label, Detail one
	// sentence about it.
	State    string `json:"state"`
	Phrase   string `json:"phrase"`
	Detail   string `json:"detail"`
	Evidence string `json:"evidence"` // available, partial or unavailable
	// CurrentRoute is the target the loop's records name as current. It says
	// nothing about activity: see InUse on the chain entry.
	CurrentRoute dockTarget       `json:"currentRoute"`
	Children     dockChildren     `json:"children"`
	LastCheck    dockCheck        `json:"lastCheck"`
	NextCheck    dockNext         `json:"nextCheck"`
	Fallbacks    dockFallbacks    `json:"fallbacks"`
	Chain        dockChain        `json:"chain"`
	Limits       []dockLimit      `json:"limits"`
	LimitWait    *dockLimitWait   `json:"limitWait,omitempty"`
	Issues       []string         `json:"issues"`
	Identifiers  *dockIdentifiers `json:"identifiers,omitempty"`
}

type dockTarget struct {
	Availability string `json:"availability"`   // known or unknown
	Role         string `json:"role,omitempty"` // main or fallback
	Name         string `json:"name,omitempty"`
	Tool         string `json:"tool,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
}

// dockChildren is the loop's lanes, which keepgoing knows only as counts.
type dockChildren struct {
	// current, held, stale, none_recorded or unknown.
	Availability string `json:"availability"`
	// Holding: the last check recorded lane work or lane input as its reason.
	Holding    bool       `json:"holding"`
	Working    int        `json:"working"`
	Stuck      int        `json:"stuck"`
	Failed     int        `json:"failed"`
	CheckedAt  *time.Time `json:"checkedAt,omitempty"`
	AgeSeconds *int64     `json:"ageSeconds,omitempty"`
	Phrase     string     `json:"phrase"`
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

// dockChain is the loop's routes in order of preference, as fallbacks list
// reads them: the main target first, then each fallback in configured order.
// Credentials, account directories, commands and paths are never part of it.
// Each entry carries a stable position and ID so later slices can add
// reorder, add and remove controls that call the guided fallbacks commands.
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
	// Route is the key of this entry's route in the report's routes.
	Route    string `json:"route"`
	Account  string `json:"account,omitempty"` // the configured quota group
	Tool     string `json:"tool,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Current: the loop's records name this entry as its current target.
	// InUse: also the loop is on with no hold, its last check is recent, and
	// that check recorded this target. Use says which, in plain words.
	Current  bool   `json:"current"`
	InUse    bool   `json:"inUse"`
	Use      string `json:"use,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// Readiness is ready, not_ready, not_probed, disabled, unknown, main for
	// the main target, or current for a fallback that is the loop's current
	// target, which carries no verdict because that route is not probed.
	Readiness string          `json:"readiness"`
	Phrase    string          `json:"phrase"`
	ProbedAt  *time.Time      `json:"probedAt,omitempty"`
	ProbeAge  *int64          `json:"probeAgeSeconds,omitempty"`
	Usage     dockUsageTarget `json:"usage"`
}

// dockRoute is one route across every loop that names it. keepgoing knows it
// from configuration only: it does not verify which account or model a BB
// thread is actually using. Lanes are not attributed to a route, because
// keepgoing records a loop's lanes only as counts.
type dockRoute struct {
	Key      string `json:"key"`
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
	Model    string `json:"model,omitempty"`
	Tool     string `json:"tool,omitempty"`
	// Readiness is ready, not_ready, not_probed, disabled, current or
	// unknown: the newest probe result any loop holds for this route. A
	// route used only as a main target is never probed and reads unknown.
	Readiness string          `json:"readiness"`
	Phrase    string          `json:"phrase"`
	ProbeAge  *int64          `json:"probeAgeSeconds,omitempty"`
	Usage     dockUsageTarget `json:"usage"` // the newest reading any loop holds
	// RunningNow are loops in use on this route (see dockChainEntry.InUse).
	// RecordedFor are loops whose records name it as current but that are not
	// in use: off, held, finished or not checked recently. StandbyFor are
	// loops that list it and are on another route. UnknownFor are loops that
	// list it but whose current route could not be read.
	RunningNow  []dockRouteLoop `json:"runningNow"`
	RecordedFor []dockRouteLoop `json:"recordedFor"`
	StandbyFor  []dockRouteLoop `json:"standbyFor"`
	UnknownFor  []dockRouteLoop `json:"unknownFor"`
	Omitted     int             `json:"omitted"`
	// Lanes is always unknown in this schema.
	Lanes string `json:"lanes"`
}

type dockRouteLoop struct {
	Key      string `json:"key"`
	Loop     string `json:"loop"`
	State    string `json:"state"`
	Phrase   string `json:"phrase"`
	Position int    `json:"position"`
	Working  int    `json:"lanesWorking"`
}

// dockRouteKey includes the probe's usage identity (a hash of what selects
// the account a probe reads), so two accounts configured under one group are
// two routes and never share a reading.
func dockRouteKey(provider, account, model, tool, usageIdentity string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{"keepgoing-dock-route", provider, account, model, tool, usageIdentity}, "\x00")))
	return "r" + hex.EncodeToString(h[:6])
}

// dockRoutes groups every loop's chain by route. Newest evidence wins.
func dockRoutes(loops []dockLoop) []dockRoute {
	index := map[string]int{}
	routes := []dockRoute{}
	for _, l := range loops {
		for _, e := range l.Chain.Entries {
			i, ok := index[e.Route]
			if !ok {
				if len(routes) >= dockMaxRoutes {
					continue
				}
				i = len(routes)
				index[e.Route] = i
				routes = append(routes, dockRoute{Key: e.Route, Provider: e.Provider, Account: e.Account, Model: e.Model, Tool: e.Tool, Readiness: "unknown", Phrase: "Unknown",
					Usage:      dockUsageTarget{Availability: "unavailable", Windows: []dockUsageWindow{}},
					RunningNow: []dockRouteLoop{}, RecordedFor: []dockRouteLoop{}, StandbyFor: []dockRouteLoop{}, UnknownFor: []dockRouteLoop{}, Lanes: "unknown"})
			}
			n := &routes[i]
			switch {
			case e.ProbeAge != nil && (n.ProbeAge == nil || *e.ProbeAge < *n.ProbeAge):
				n.Readiness, n.Phrase, n.ProbeAge = e.Readiness, e.Phrase, e.ProbeAge
			case e.Position > 0 && n.ProbeAge == nil && n.Readiness == "unknown":
				// Never probed anywhere: say why (turned off, or not probed yet).
				n.Readiness, n.Phrase = e.Readiness, e.Phrase
			}
			if u := e.Usage; u.AgeSeconds != nil && (n.Usage.AgeSeconds == nil || *u.AgeSeconds < *n.Usage.AgeSeconds) {
				n.Usage = u
				n.Usage.Name = ""
			}
			ref := dockRouteLoop{Key: l.Key, Loop: l.Name, State: l.State, Phrase: l.Phrase, Position: e.Position}
			list := &n.StandbyFor
			switch {
			case l.CurrentRoute.Availability != "known":
				list = &n.UnknownFor
			case e.InUse:
				list = &n.RunningNow
				if l.Children.Availability == "current" {
					ref.Working = l.Children.Working
				}
			case e.Current:
				list = &n.RecordedFor
			}
			if len(*list) >= dockMaxRouteLoops {
				n.Omitted++
				continue
			}
			*list = append(*list, ref)
		}
	}
	sort.SliceStable(routes, func(i, j int) bool { return len(routes[i].RunningNow) > len(routes[j].RunningNow) })
	return routes
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

// dockInput is everything one loop's view is built from.
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
	At    time.Time
	Cause string // fixed vocabulary of incident.Cause; empty for a control command
	// Marked: the await-user command set it. Other control operations that
	// set the flag stay unexplained.
	Marked bool
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
			h.Cause, h.Marked = "", c.Operation == "await-user"
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
	"supervising": 5, "outside_hours": 6, "paused": 7, "off": 8, "stopped": 9, "finished": 10,
}

// buildDockReport sorts loops so the ones that want attention come first.
// complete is false when the read was cut off, so that an unread loop can
// never be part of an all-clear.
func buildDockReport(inputs []dockInput, now time.Time, refresh, staleAfter time.Duration, identifiers, registryOK bool, omitted int, complete bool) dockReport {
	r := dockReport{SchemaVersion: dockSchemaVersion, ReadOnly: true, ReadAt: now, RefreshSeconds: int(refresh / time.Second),
		StaleAfterSeconds: int(staleAfter / time.Second), Identifiers: identifiers, NeedsYou: []dockNeed{}, Loops: []dockLoop{}, Routes: []dockRoute{},
		Registry: dockRegistry{Availability: "unavailable"}, Complete: complete}
	if !registryOK {
		return r
	}
	r.Registry = dockRegistry{Availability: "available", Count: len(inputs) + omitted, Omitted: omitted}
	for _, in := range inputs {
		loop, need := projectDockLoop(in, now, staleAfter, identifiers)
		r.Loops = append(r.Loops, loop)
		if need != nil {
			r.NeedsYou = append(r.NeedsYou, *need)
			if need.Kind == "unreadable" {
				r.Unread++
			}
		}
	}
	r.AllClear = complete && omitted == 0 && len(r.NeedsYou) == 0
	sort.SliceStable(r.Loops, func(i, j int) bool {
		a, b := dockStatePriority[r.Loops[i].State], dockStatePriority[r.Loops[j].State]
		if a != b {
			return a < b
		}
		return strings.ToLower(r.Loops[i].Name) < strings.ToLower(r.Loops[j].Name)
	})
	r.Routes = dockRoutes(r.Loops)
	sort.SliceStable(r.NeedsYou, func(i, j int) bool {
		a, b := r.NeedsYou[i].WaitedSeconds, r.NeedsYou[j].WaitedSeconds
		if a == nil || b == nil {
			return a != nil
		}
		return *a > *b
	})
	return r
}

// dockUnreadable is the entry for a loop whose hold state could not be read.
// Such a loop is never left out of what needs a person.
func dockUnreadable(issues []string) *dockNeed {
	n := &dockNeed{Kind: "unreadable", Reason: "It could not be read, so whether it needs you is unknown.",
		Action: "Look at it: run keepgoing status with its configuration."}
	switch code := firstIssue(issues); code {
	case "transfer_pending":
		n.Reason, n.Action = "A move to another conversation is in progress, which holds it.", "Finish the move with keepgoing transfer recover."
	case "binding_unavailable", "binding_changed", "registration_changed", "registration_unavailable", "target_binding_mismatch", "control_unavailable", "state_unavailable", "cancelled":
		n.Reason = dockIssue(code) + " Whether it needs you is unknown."
	}
	return n
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

	// The target its records name as current.
	l.CurrentRoute = dockTarget{Availability: "unknown"}
	if st != nil && st.TargetConfigured {
		a := st.TargetAgent
		l.CurrentRoute = dockTarget{Availability: "known", Role: dockRole(st.Target), Name: clip(st.Target, dockMaxText), Tool: dockTool(a.Adapter),
			Provider: clip(a.Provider, dockMaxText), Model: clip(a.Model, dockMaxText)}
		if identifiers && a.BBThread != "" && transferThreadID.MatchString(a.BBThread) {
			l.Identifiers.Thread = a.BBThread
		}
	}

	l.Children = dockChildWork(in.Runs, in.RunsKnown)
	l.LastCheck = dockLastCheck(st, now, staleAfter)
	l.NextCheck = dockNextCheck(snap)
	l.Fallbacks = dockFallbackReadiness(st, in.Chain)
	if st != nil {
		l.Limits = dockLimits(st, now)
		if w := st.LimitWait; w.State != "none" && w.ResetsAt != nil {
			l.LimitWait = &dockLimitWait{Target: clip(w.Target, dockMaxText), ResetsAt: w.ResetsAt, Passed: w.ResetPassed}
		}
	}

	// State, in one phrase. Each case names the record it rests on.
	var need *dockNeed
	switch {
	case snap == nil:
		// Registered status returned no snapshot.
		l.State, l.Phrase, l.Detail = "unknown", "Can't be read", "keepgoing could not read this loop, so nothing about it is shown."
		need = dockUnreadable(in.Client.Issues)
	case snap.Controls == nil:
		// control.json could not be read.
		l.State, l.Phrase, l.Detail = "unknown", "Unknown", "Its control record could not be read, so whether it is on, held or stopped is unknown."
		need = dockUnreadable([]string{"control_unavailable"})
	default:
		c := snap.Controls
		switch {
		case snap.ControlUnreadable && c.Stop:
			l.State, l.Phrase, l.Detail = "stopped", "Stopped (control record unreadable)", "Its control record could not be read. A stop is kept beside it, so nothing is sent. Restore the record, then resume."
			need = dockUnreadable([]string{"control_unavailable"})
		case snap.ControlUnreadable:
			l.State, l.Phrase, l.Detail = "paused", "Paused (control record unreadable)", "Its control record could not be read. A pause is kept beside it, so nothing is sent. Restore the record, then resume."
			need = dockUnreadable([]string{"control_unavailable"})
		case c.Stop:
			l.State, l.Phrase, l.Detail = "stopped", "Stopped", "A stop is requested. Nothing is sent until it is resumed."
		case c.Completed:
			l.State, l.Phrase, l.Detail = "finished", "Finished", "Marked finished, which is a declaration and is not verified. Nothing is sent until it is resumed."
		case c.AwaitingUser:
			l.State, l.Phrase = "waiting_for_you", "Waiting for you"
			need = dockHoldNeed(st, in.Hold, identifiers)
			l.Detail = need.Reason
		case c.Paused:
			l.State, l.Phrase, l.Detail = "paused", "Paused", "Paused. Nothing is sent until it is resumed."
		case !c.Enabled:
			l.State, l.Phrase, l.Detail = "off", "Off", "Supervision is off for this loop."
		case st == nil:
			// On, but state.json or the rest of status could not be read.
			l.State, l.Phrase, l.Detail = "unknown", "Unknown", "It is on, but its check history could not be read."
			need = dockUnreadable([]string{"state_unavailable"})
		case !st.InWindow:
			// The same answer status gives as outside_window.
			l.State, l.Phrase, l.Detail = "outside_hours", "Outside its hours", "It is on, and the time is outside its configured operating hours."
		case l.LastCheck.Availability == "never":
			l.State, l.Phrase, l.Detail = "not_checked_recently", "Not checked yet", "It is on, and no check is recorded."
		case l.LastCheck.Availability == "stale":
			l.State, l.Phrase, l.Detail = "not_checked_recently", "Not checked recently", "It is on, and its last recorded check is older than "+dockDuration(staleAfter)+"."
		case l.LimitWait != nil && !l.LimitWait.Passed:
			l.State, l.Phrase, l.Detail = "waiting_on_limit", "Waiting for a limit to reset", "A check recorded a provider usage limit with no other route able to take over. The loop is not held and keeps checking."
		case l.LastCheck.FailedInRow > 0:
			l.State, l.Phrase, l.Detail = "checks_failing", "Checks are failing", "Checks are running but could not observe or act."
		default:
			l.State, l.Phrase, l.Detail = "supervising", "Supervising", "On, with a recent check."
			if l.Children.Holding {
				l.Detail = "On. Its last check sent nothing because lanes are working or waiting."
			}
			if c := st.State.LastCheck; c != nil && c.At.Equal(st.State.CheckedAt) && (c.DryRun || c.Outcome == "dry_run") {
				l.Detail = "On, in dry-run mode: its last check decided and sent nothing."
			}
		}
	}
	if st != nil && st.NewThreads.Pending && need == nil {
		need = &dockNeed{Kind: "held", Reason: "A new conversation it tried to create for a move to another route could not be confirmed.",
			Action: "Find that conversation in BB, then settle it with keepgoing retarget."}
	}
	if need != nil {
		need.Key, need.Loop = l.Key, l.Name
		if need.Since != nil {
			need.WaitedSeconds = secondsSince(now, *need.Since)
		}
	}
	l.Chain = dockHandoffChain(in.Chain, dockUse(l, st), now)
	return l, need
}

// dockRouteUse says how the loop's current target is being used.
type dockRouteUse struct {
	Target string // empty when the current target is unknown
	InUse  bool
	Phrase string
}

// dockUse decides whether the current target is in use. It is in use only
// when the loop is on with no hold, its last check is recent, and that check
// recorded this target. Otherwise the phrase says what is recorded.
func dockUse(l dockLoop, st *statusOut) dockRouteUse {
	if st == nil || !st.TargetConfigured {
		return dockRouteUse{}
	}
	u := dockRouteUse{Target: st.Target}
	switch l.State {
	case "supervising", "waiting_on_limit", "checks_failing":
		c := st.State.LastCheck
		if c != nil && c.At.Equal(st.State.CheckedAt) && c.Target == st.Target {
			u.InUse, u.Phrase = true, "In use now"
		} else {
			u.Phrase = "Recorded route; the last check did not record it"
		}
	case "waiting_for_you":
		u.Phrase = "Recorded route; loop is held"
	case "paused":
		u.Phrase = "Recorded route; loop is paused"
	case "stopped":
		u.Phrase = "Recorded route; loop is stopped"
	case "finished":
		u.Phrase = "Recorded route; loop is finished"
	case "off":
		u.Phrase = "Recorded route; loop is off"
	case "outside_hours":
		u.Phrase = "Recorded route; outside its hours"
	case "not_checked_recently":
		u.Phrase = "Recorded route; no recent check"
	default:
		u.Phrase = "Recorded route"
	}
	return u
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
	"target_unavailable":       "Its current route is not in its configuration.",
	"window_unavailable":       "Its operating hours could not be evaluated.",
	"goal_unavailable":         "Its goal record could not be read.",
	"cancelled":                "The read was cut off before this loop was read.",
}

func dockIssue(code string) string {
	if s, ok := dockIssues[code]; ok {
		return s
	}
	return "Some evidence is unavailable."
}

// dockHoldNeed explains an awaiting-user hold from the fixed cause recorded
// when it started. Nothing is inferred from the note's text, and the note is
// included only with identifiers, as registered status withholds it.
func dockHoldNeed(st *statusOut, hold *dockHold, identifiers bool) *dockNeed {
	n := &dockNeed{Kind: "held", Reason: "It is held for a person.", Action: "Look at the conversation, then resume the loop when it is ready to continue."}
	cause := ""
	switch {
	case hold != nil && hold.Marked:
		n.Reason = "It was marked as waiting for a person."
	case hold != nil:
		cause = hold.Cause
	case st != nil && st.State.LastCheck != nil && st.State.LastCheck.Action == "await_user":
		cause = incident.Cause(st.State.LastCheck.Reason)
	}
	switch cause {
	case "child_input":
		n.Reason, n.Action = "A lane is waiting for an answer.", "Answer it in that conversation, then resume the loop."
	case "input":
		n.Reason, n.Action = "The agent is waiting on a question or a permission request.", "Answer it in the conversation, then resume the loop."
	case "deadline":
		n.Reason, n.Action = "It reached its deadline.", "Mark it finished, or extend the deadline in its configuration and resume."
	case "send_limit":
		n.Reason, n.Action = "It reached its limit on messages sent.", "Review the work, then raise the limit or mark it finished."
	case "git_limit":
		n.Reason, n.Action = "No Git progress within its time limit.", "See why, then resume or mark it finished."
	case "stall_limit":
		n.Reason, n.Action = "No progress after repeated nudges.", "See why, then resume or mark it finished."
	}
	if hold != nil && !hold.At.IsZero() {
		at := hold.At
		n.Since, n.SinceEvidence = &at, "hold_recorded"
	}
	if st == nil {
		return n
	}
	if note := strings.TrimSpace(st.Control.Note); note != "" {
		if identifiers {
			n.Note = clip(note, dockMaxNote)
		} else {
			n.NoteWithheld = true
		}
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
		c.Availability, c.Holding = "current", b.Availability == blockerBlocked
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
		if len(parts) == 0 {
			c.Phrase = "None at the last check"
		}
		if b.Availability == blockerHeld {
			c.Phrase += " (as of the check that held it)"
		}
	case blockerUnknown:
		switch b.Reason {
		case "no_open_run", "no_child_evidence_recorded":
			c.Availability, c.Phrase = "none_recorded", "None recorded"
		case "last_check_older_than_stale_after":
			c.Availability, c.Phrase = "stale", "Stale: the last listing is too old to show as current"
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

// dockAction words the recorded action and outcome of a check. A send that
// returned is not proof the agent received or acted on it.
func dockAction(c *state.Check) string {
	verb := map[string]string{
		"none": "Checked; sent nothing", "wake": "Sent a wake", "nudge": "Sent a nudge", "reprompt": "Sent a re-prompt or retried the turn",
		"await_user": "Held the loop for a person", "stop": "Stopped the session", "handoff": "Moved to another route", "switch_back": "Returned to the first route",
	}[c.Action]
	if verb == "" {
		verb = "Checked"
	}
	switch {
	case c.DryRun || c.Outcome == "dry_run":
		return verb + " (dry run: nothing was sent)"
	case c.Outcome == "failed" && c.Action == "none":
		return "The check failed"
	case c.Outcome == "failed":
		return verb + ", which failed"
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
	case d.State == "not_recorded":
		// The only evidence is the absence of an endpoint record. Whether a
		// scheduler outside keepgoing runs the loop is not inspected.
		n.Detail = "no keepgoing daemon endpoint is recorded for this loop; a scheduler outside keepgoing is not visible"
	case d.State != "responsive":
		n.Detail = "a keepgoing daemon endpoint is recorded for this loop but could not be verified"
	case d.Phase == "waiting" && d.NextCheckAt != nil:
		n.Availability, n.At, n.Phrase = "known", d.NextCheckAt, "Due"
	default:
		n.Availability, n.Phrase = "in_progress", "A check is running now"
	}
	return n
}

func dockFallbackReadiness(st *statusOut, chain *dockChainInput) dockFallbacks {
	switch {
	case chain != nil && len(chain.Candidates) == 0:
		return dockFallbacks{Availability: "none_configured", Phrase: "None set up"}
	case st != nil && st.Redundancy != nil:
		s := st.Redundancy
		f := dockFallbacks{Availability: "known", Ready: len(s.Healthy), Eligible: len(s.Eligible), Needed: s.Min, Enough: s.Meets}
		f.Phrase = fmt.Sprintf("%d of %d ready", f.Ready, f.Eligible)
		if f.Eligible == 0 {
			f.Phrase = "None eligible to take over"
		}
		return f
	}
	return dockFallbacks{Availability: "unknown", Phrase: "Unknown"}
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
			l.Unknown, l.Phrase = true, "Unknown (limit "+dockLimitDuration(p.Timeout)+")"
		case p.BaselinePending:
			l.Phrase = "Not measured yet (limit " + dockLimitDuration(p.Timeout) + ")"
		case p.ChangedAt != nil:
			l.Phrase = dockDuration(now.Sub(*p.ChangedAt)) + " of " + dockLimitDuration(p.Timeout)
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

// dockDuration prints a length of time the same way wherever the page shows
// one: "45 min", "3 h 12 min", "24 h", "2 days 6 h".
func dockDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	m := int(d.Round(time.Minute) / time.Minute)
	part := func(n int, unit string) string {
		if n == 0 {
			return ""
		}
		return fmt.Sprintf(" %d %s", n, unit)
	}
	switch {
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m < 48*60:
		return fmt.Sprintf("%d h", m/60) + part(m%60, "min")
	}
	return fmt.Sprintf("%d days", m/(24*60)) + part(m%(24*60)/60, "h")
}

// dockLimitDuration formats a configured duration string; one that does not
// parse is shown as written.
func dockLimitDuration(s string) string {
	d, err := time.ParseDuration(s)
	if err != nil {
		return clip(s, 40)
	}
	return dockDuration(d)
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

func dockHandoffChain(in *dockChainInput, use dockRouteUse, now time.Time) dockChain {
	c := dockChain{Availability: "unknown", HandoffOn: []string{}, Entries: []dockChainEntry{}}
	if in == nil {
		return c
	}
	for _, class := range in.FailoverOn {
		c.HandoffOn = append(c.HandoffOn, clip(class, 40))
	}
	entry := func(pos int, cand config.Candidate) dockChainEntry {
		ref := refOf(cand.Agent)
		e := dockChainEntry{Position: pos, ID: clip(cand.ID, dockMaxText), Tool: dockTool(ref.Adapter), Provider: clip(ref.Provider, dockMaxText),
			Route: dockRouteKey(ref.Provider, ref.Group, ref.Model, ref.Adapter, probe.UsageIdentity(cand)), Account: clip(ref.Group, dockMaxText),
			Model: clip(ref.Model, dockMaxText), Disabled: cand.Disabled, Readiness: "unknown", Phrase: "Unknown",
			Usage: dockUsageTarget{Name: clip(cand.ID, dockMaxText), Availability: "unavailable", Windows: []dockUsageWindow{}}}
		if use.Target != "" && cand.ID == use.Target {
			e.Current, e.InUse, e.Use = true, use.InUse, use.Phrase
		}
		return e
	}
	main := entry(0, in.Primary)
	main.Readiness, main.Phrase = "main", "First route"
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
		case e.Current && (row.Last == nil || row.Last.OK):
			// The loop's current target is not probed, so an old probe says
			// nothing against it. Only a recorded failure is a verdict.
			e.Readiness, e.Phrase = "current", e.Use
		case row.Healthy:
			e.Readiness, e.Phrase = "ready", "Ready"
		case row.Last == nil:
			e.Readiness, e.Phrase = "not_probed", "Not probed yet"
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

// dockProbePhrase words the last probe's recorded result for a fallback that
// is not ready. Each says "at its last probe": it is not a live reading.
func dockProbePhrase(ok bool, class string) string {
	if ok {
		return "Last probe passed, but too long ago to rely on"
	}
	switch class {
	case "rate_limit":
		return "Hit a usage limit at its last probe"
	case "auth":
		return "Sign-in failed at its last probe"
	case "network":
		return "Could not be reached at its last probe"
	case "overloaded":
		return "Provider overloaded at its last probe"
	}
	return "Last probe failed"
}
