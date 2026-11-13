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
	// Allowances is one line per account group: the freshest usage reading
	// recorded for any of its routes, and a recorded limit if a loop left
	// one of them on a usage limit whose reset has not passed.
	Allowances []dockAllowance `json:"allowances"`
}

// dockAllowance is what is recorded about one account group's usage. It is
// the newest reading any loop holds for a route in the group, not a live one.
type dockAllowance struct {
	Provider string          `json:"provider,omitempty"`
	Account  string          `json:"account,omitempty"`
	Routes   int             `json:"routes"`
	Usage    dockUsageTarget `json:"usage"`
	// Limit is set when a loop left a route in this group on a usage limit
	// whose reported reset has not passed, or was not reported.
	Limit *dockSharedLimit `json:"limit,omitempty"`
}

// dockAllowances groups routes by provider and account group, in route order.
func dockAllowances(routes []dockRoute) []dockAllowance {
	index := map[string]int{}
	out := []dockAllowance{}
	for _, r := range routes {
		key := r.Provider + "\x00" + r.Account
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, dockAllowance{Provider: r.Provider, Account: r.Account, Usage: dockUsageTarget{Availability: "unavailable", Windows: []dockUsageWindow{}}})
		}
		a := &out[i]
		a.Routes++
		if u := r.Usage; u.AgeSeconds != nil && (a.Usage.AgeSeconds == nil || *u.AgeSeconds < *a.Usage.AgeSeconds) {
			a.Usage = u
		}
		for _, left := range r.LeftBy {
			if left.Reason != dockUsageLimit || left.ResetPassed {
				continue
			}
			if a.Limit == nil || (left.ResetsAt != nil && (a.Limit.ResetsAt == nil || left.ResetsAt.After(*a.Limit.ResetsAt))) {
				a.Limit = &dockSharedLimit{Loop: left.Loop, Since: left.Since, ResetsAt: left.ResetsAt}
			}
		}
	}
	return out
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
	// waiting_on_network, checks_failing, paused, stopped, finished, off,
	// outside_hours, not_checked_recently or unknown. Phrase is its label, Detail one
	// sentence about it.
	State    string `json:"state"`
	Phrase   string `json:"phrase"`
	Detail   string `json:"detail"`
	Evidence string `json:"evidence"` // available, partial or unavailable
	// CurrentRoute is the target the loop's records name as current. It says
	// nothing about activity: see InUse on the chain entry.
	CurrentRoute dockTarget `json:"currentRoute"`
	// Away is set when the loop's current route is not its first: when it
	// left, why, and what is recorded about going back.
	Away *dockAway `json:"away,omitempty"`
	// Decision is the last recorded check's action and fixed cause; Hold the
	// hold in force; Device this computer; Lanes each lane of the last
	// verified listing; Events the newest retained history entries.
	Decision    dockDecision     `json:"decision"`
	Hold        dockHoldState    `json:"hold"`
	Device      dockDevice       `json:"device"`
	Lanes       dockLanes        `json:"lanes"`
	Events      dockEvents       `json:"events"`
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
	// Reason says, when a record shows it, why there is no reading.
	Reason string `json:"reason,omitempty"`
	// FromAnotherLoop: the reading is another loop's record of the same
	// route, shown because it is newer than this loop's own.
	FromAnotherLoop bool `json:"fromAnotherLoop,omitempty"`
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
	// Effort is the configured effort level; empty when none is configured.
	Effort string `json:"effort,omitempty"`
	// Enter is how the loop gets onto this route, in the vocabulary of the
	// route lists: first, same_conversation, other_conversation,
	// new_conversation, headless or command. Source is where the route
	// comes from: loop for the first route, own for the loop's own list.
	// (A shared standard list is planned; until it exists every other route
	// is the loop's own.)
	Enter  string `json:"enter"`
	Source string `json:"source"`
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
	Readiness string `json:"readiness"`
	Phrase    string `json:"phrase"`
	// ProbeNote adds the last test's age for a current route, which carries
	// no readiness verdict. OtherTest is another loop's newer test result of
	// the same route, when it differs from this loop's own record.
	ProbeNote string          `json:"probeNote,omitempty"`
	OtherTest string          `json:"otherTest,omitempty"`
	ProbedAt  *time.Time      `json:"probedAt,omitempty"`
	ProbeAge  *int64          `json:"probeAgeSeconds,omitempty"`
	Usage     dockUsageTarget `json:"usage"`
}

// dockAway is what the loop's state records about having left its first
// route: state.json's lastHandoffAt, primaryResetsAt and lastSwitchBackTry,
// the handoff's recorded error class, and whether automatic return is on.
type dockAway struct {
	// Choice is the current route's place in the order, 2 for the second.
	Choice int        `json:"choice"`
	Since  *time.Time `json:"since,omitempty"`
	// Reason is in plain words ("a usage limit"); empty when not recorded.
	Reason string `json:"reason,omitempty"`
	// ResetsAt is the reset the provider reported at the handoff; absent
	// when none was reported. ResetPassed compares it with the read time.
	ResetsAt      *time.Time `json:"resetsAt,omitempty"`
	ResetPassed   bool       `json:"resetPassed"`
	LastReturnTry *time.Time `json:"lastReturnTry,omitempty"`
	// Return is on, off (automatic return is not configured), stopped
	// (given up after rejected attempts) or unknown.
	Return string `json:"return"`
}

// dockRouteLimit is one loop that left this route, with the reset recorded.
type dockRouteLimit struct {
	Loop        string     `json:"loop"`
	LoopKey     string     `json:"loopKey"`
	Since       *time.Time `json:"since,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
	ResetPassed bool       `json:"resetPassed"`
	Return      string     `json:"return"`
}

// dockSharedLimit says another route in the same account group is recorded
// at a limit. It changes nothing about this route's own readiness.
type dockSharedLimit struct {
	Loop     string     `json:"loop,omitempty"`
	Since    *time.Time `json:"since,omitempty"`
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
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
	Effort   string `json:"effort,omitempty"`
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
	// LeftBy are loops that left this route, with what their state records
	// of the limit. SharedLimit is set on a route in the same account group
	// as one a loop left on a limit whose reset has not passed.
	LeftBy      []dockRouteLimit `json:"leftBy"`
	SharedLimit *dockSharedLimit `json:"sharedLimit,omitempty"`
	// Attention is limit (a loop left it on a usage limit that has not
	// reset, or a recorded window is used up), not_ready (its newest test
	// failed or is too old), or empty. Routes are listed with these first.
	Attention string `json:"attention,omitempty"`
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

// dockRouteKey includes the effort level, as a route is a model on an account
// at an effort level, and the probe's usage identity (a hash of what selects
// the account a probe reads), so two accounts configured under one group are
// two routes and never share a reading.
func dockRouteKey(provider, account, model, effort, tool, usageIdentity string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{"keepgoing-dock-route", provider, account, model, effort, tool, usageIdentity}, "\x00")))
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
				routes = append(routes, dockRoute{Key: e.Route, Provider: e.Provider, Account: e.Account, Model: e.Model, Effort: e.Effort, Tool: e.Tool, Readiness: "unknown", Phrase: "Unknown",
					Usage:      dockUsageTarget{Availability: "unavailable", Windows: []dockUsageWindow{}},
					RunningNow: []dockRouteLoop{}, RecordedFor: []dockRouteLoop{}, StandbyFor: []dockRouteLoop{}, UnknownFor: []dockRouteLoop{}, LeftBy: []dockRouteLimit{}, Lanes: "unknown"})
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
			if a := l.Away; a != nil && e.Position == 0 && len(n.LeftBy) < dockMaxRouteLoops {
				n.LeftBy = append(n.LeftBy, dockRouteLimit{Loop: l.Name, LoopKey: l.Key, Since: a.Since, Reason: a.Reason, ResetsAt: a.ResetsAt, ResetPassed: a.ResetPassed, Return: a.Return})
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
	// A route in the same account group as one left on a usage limit that
	// has not reset carries a note; its own readiness is left as it is.
	for i := range routes {
		for j := range routes {
			if i == j || routes[i].Provider != routes[j].Provider || routes[i].Account != routes[j].Account {
				continue
			}
			for _, left := range routes[j].LeftBy {
				if left.Reason != dockUsageLimit || left.ResetPassed {
					continue
				}
				if s := routes[i].SharedLimit; s == nil || (left.ResetsAt != nil && (s.ResetsAt == nil || left.ResetsAt.After(*s.ResetsAt))) {
					routes[i].SharedLimit = &dockSharedLimit{Loop: left.Loop, Since: left.Since, ResetsAt: left.ResetsAt}
				}
			}
		}
	}
	rank := func(r dockRoute) int {
		switch {
		case r.Attention == "limit":
			return 0
		case r.Attention == "not_ready":
			return 1
		case len(r.RunningNow) > 0:
			return 2
		}
		return 3
	}
	for i := range routes {
		r := &routes[i]
		for _, left := range r.LeftBy {
			if left.Reason == dockUsageLimit && !left.ResetPassed {
				r.Attention = "limit"
			}
		}
		for _, w := range r.Usage.Windows {
			if !w.Stale && w.UsedPercent >= 100 {
				r.Attention = "limit"
			}
		}
		if r.Attention == "" && r.Readiness == "not_ready" {
			r.Attention = "not_ready"
		}
	}
	sort.SliceStable(routes, func(i, j int) bool {
		if a, b := rank(routes[i]), rank(routes[j]); a != b {
			return a < b
		}
		return len(routes[i].RunningNow) > len(routes[j].RunningNow)
	})
	return routes
}

// dockShareAcrossLoops gives each loop's route the newest usage reading and
// test result any loop holds for that same route, marked as another loop's,
// so one route does not read differently on different pages. A route is keyed
// by what selects its account, so the record is of the same account.
func dockShareAcrossLoops(loops []dockLoop, routes []dockRoute) {
	byKey := map[string]dockRoute{}
	for _, r := range routes {
		byKey[r.Key] = r
	}
	for i := range loops {
		for j := range loops[i].Chain.Entries {
			e := &loops[i].Chain.Entries[j]
			r, ok := byKey[e.Route]
			if !ok {
				continue
			}
			if u := r.Usage; u.AgeSeconds != nil && (e.Usage.AgeSeconds == nil || *u.AgeSeconds < *e.Usage.AgeSeconds) {
				name := e.Usage.Name
				e.Usage = u
				e.Usage.Name, e.Usage.FromAnotherLoop = name, true
			}
			newer := r.ProbeAge != nil && (e.ProbeAge == nil || *r.ProbeAge < *e.ProbeAge)
			if newer && r.Phrase != e.Phrase && r.Readiness != "current" && r.Readiness != "main" && r.Readiness != "unknown" {
				e.OtherTest = r.Phrase
			}
		}
	}
}

// dockChainInput is the configuration-derived part of the chain plus the
// fallbacks list summary; Summary is nil when probe history was unreadable.
type dockChainInput struct {
	Primary    config.Candidate
	Candidates []config.Candidate
	FailoverOn []string
	Summary    *fallbacksOut
	PrimaryUse *usageView
	// ProbeEvery is the configured interval of scheduled probes; zero when
	// scheduled probes are off. SwitchBack is whether automatic return to
	// the first route is configured.
	ProbeEvery time.Duration
	SwitchBack bool
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
	// Timeline is the retained history as timeline reads it; nil when it was
	// not read. Hostname is this computer's name, shown only with identifiers.
	Timeline *timelineReport
	Hostname string
}

// dockHold is the retained history record of an awaiting-user hold starting.
type dockHold struct {
	At    time.Time
	Cause string // fixed vocabulary of incident.Cause; empty for a control command
	// Marked: the await-user command set it. Other control operations that
	// set the flag stay unexplained.
	Marked bool
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
	"waiting_for_you": 0, "unknown": 1, "checks_failing": 2, "not_checked_recently": 3, "waiting_on_limit": 4, "waiting_on_network": 4,
	"supervising": 5, "outside_hours": 6, "paused": 7, "off": 8, "stopped": 9, "finished": 10,
}

// buildDockReport sorts loops so the ones that want attention come first.
// complete is false when the read was cut off, so that an unread loop can
// never be part of an all-clear.
func buildDockReport(inputs []dockInput, now time.Time, refresh, staleAfter time.Duration, identifiers, registryOK bool, omitted int, complete bool) dockReport {
	r := dockReport{SchemaVersion: dockSchemaVersion, ReadOnly: true, ReadAt: now, RefreshSeconds: int(refresh / time.Second),
		StaleAfterSeconds: int(staleAfter / time.Second), Identifiers: identifiers, NeedsYou: []dockNeed{}, Loops: []dockLoop{}, Routes: []dockRoute{}, Allowances: []dockAllowance{},
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
	dockShareAcrossLoops(r.Loops, r.Routes)
	r.Allowances = dockAllowances(r.Routes)
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
	l.Lanes = dockLaneList(in.Runs, in.RunsKnown, identifiers)
	l.Decision = dockLastDecision(st, in.Timeline, now)
	l.Device = dockDeviceOf(st, in.Hostname, identifiers)
	l.Events = dockRecentEvents(in.Timeline, dockRouteNamesOf(in.Chain), now)
	l.Hold = dockHoldState{State: "unknown", Phrase: "Unknown"}
	if snap != nil {
		var updated time.Time
		if st != nil {
			updated = st.Control.UpdatedAt
		}
		l.Hold = dockCurrentHold(snap.Controls, updated, in.Timeline, now)
	}
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
		case !st.State.NetworkDownSince.IsZero() && !st.State.NetworkDistrusted:
			// The same record status prints as "network: down at the last
			// connectivity check".
			l.State, l.Phrase, l.Detail = "waiting_on_network", "Waiting for the network", "The network was down at the last connectivity check, failing for "+dockDuration(now.Sub(st.State.NetworkDownSince))+". Work that needs the network waits until a check finds it back."
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
	l.Away = dockAwayOf(st, in.Chain, in.Timeline, now)
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
	case "supervising", "waiting_on_limit", "waiting_on_network", "checks_failing":
		c := st.State.LastCheck
		if c != nil && c.At.Equal(st.State.CheckedAt) && c.Target == st.Target {
			u.InUse, u.Phrase = true, "In use now"
		} else {
			u.Phrase = "Current route, not confirmed by the last check"
		}
	case "waiting_for_you":
		u.Phrase = "Current route, loop held"
	case "paused":
		u.Phrase = "Current route, loop paused"
	case "stopped":
		u.Phrase = "Current route, loop stopped"
	case "finished":
		u.Phrase = "Current route, loop finished"
	case "off":
		u.Phrase = "Current route, loop off"
	case "outside_hours":
		u.Phrase = "Current route, outside its hours"
	case "not_checked_recently":
		u.Phrase = "Current route, no recent check"
	default:
		u.Phrase = "Current route"
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
		return "Codex CLI"
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
		n.Detail = "the loop's keepgoing scheduler was not asked"
	case d.State == "not_recorded":
		// The only evidence is the absence of an endpoint record. Whether a
		// scheduler outside keepgoing runs the loop is not inspected.
		n.Detail = "no keepgoing scheduler is on record for this loop, and one outside keepgoing is not visible"
	case d.State != "responsive":
		n.Detail = "a keepgoing scheduler is on record for this loop but did not answer"
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
			Route: dockRouteKey(ref.Provider, ref.Group, ref.Model, dockEffort(cand.Agent), ref.Adapter, probe.UsageIdentity(cand)), Account: clip(ref.Group, dockMaxText),
			Effort: dockEffort(cand.Agent),
			Model:  clip(ref.Model, dockMaxText), Disabled: cand.Disabled, Readiness: "unknown", Phrase: "Unknown",
			Usage: dockUsageTarget{Name: clip(cand.ID, dockMaxText), Availability: "unavailable", Windows: []dockUsageWindow{}}}
		if use.Target != "" && cand.ID == use.Target {
			e.Current, e.InUse, e.Use = true, use.InUse, use.Phrase
		}
		e.Enter, e.Source = dockEnter(pos, cand.Agent, in.Primary.Agent), "own"
		if pos == 0 {
			e.Source = "loop"
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
	if main.Usage.Availability == "unavailable" && main.Current {
		main.Usage.Reason = "a loop's current route is not tested"
	}
	c.Entries[0] = main
	for i, cand := range in.Candidates {
		if len(c.Entries) > dockMaxChain {
			c.Omitted++
			continue
		}
		e := entry(i+1, cand)
		row, ok := rows[cand.ID]
		age := ""
		if ok && row.Last != nil && !row.Last.At.IsZero() {
			at := row.Last.At
			e.ProbedAt, e.ProbeAge = &at, secondsSince(now, at)
			age = dockDuration(now.Sub(at)) + " ago"
		}
		switch {
		case cand.Disabled:
			e.Readiness, e.Phrase = "disabled", "Turned off in its configuration"
		case in.Summary == nil || !ok:
			c.Availability = "unknown"
			e.Phrase = "Readiness unknown: its test history could not be read"
		case e.Current && (row.Last == nil || row.Last.OK):
			// The loop's current target is not probed, so an old probe says
			// nothing against it. Only a recorded failure is a verdict.
			e.Readiness, e.Phrase = "current", e.Use
			if age != "" {
				e.ProbeNote = "Not tested while it is the loop's route; last tested " + age
			}
		case row.Last == nil:
			e.Readiness, e.Phrase = "not_probed", "Not tested yet"
		case row.Healthy:
			e.Readiness, e.Phrase = "ready", "Ready: passed its test "+age
			if in.ProbeEvery > 0 && now.Sub(row.Last.At) > in.ProbeEvery {
				// Still counted ready, but the test is older than its schedule.
				e.Readiness, e.Phrase = "ready_overdue", "Passed its test "+age+"; a new test is overdue (it is tested every "+dockDuration(in.ProbeEvery)+")"
			}
		default:
			e.Readiness, e.Phrase = "not_ready", dockProbePhrase(row.Last.OK, row.Last.ErrorClass, age)
		}
		if ok {
			e.Usage = dockUsageOf(row.Usage)
		}
		if e.Usage.Availability == "unavailable" {
			e.Usage.Reason = dockUsageReason(cand, row, ok, e.Current)
		}
		c.Entries = append(c.Entries, e)
	}
	return c
}

// dockEnter says how a loop gets onto a route, from its configuration alone.
func dockEnter(pos int, a, first config.Agent) string {
	switch {
	case pos == 0:
		return "first"
	case a.BB != nil && a.BB.NewThread:
		return "new_conversation"
	case a.BB != nil && first.BB != nil && a.BB.Thread != "" && a.BB.Thread == first.BB.Thread:
		return "same_conversation"
	case a.BB != nil:
		return "other_conversation"
	case a.Adapter == "process":
		return "command"
	}
	return "headless"
}

// dockUsageReason says why a route has no usage reading, when a record shows
// it: only the Claude probe, and a failed Codex probe, carry one.
func dockUsageReason(cand config.Candidate, row candidateRow, known, current bool) string {
	switch {
	case cand.Disabled:
		return "it is turned off, so it is not tested"
	case !known:
		return ""
	case row.Last == nil && current:
		return "a loop's current route is not tested"
	case row.Last == nil:
		return "it has not been tested yet"
	case row.Last.Kind == "command":
		return "this kind of test does not report usage"
	case row.Last.Kind == "codex" && row.Last.OK:
		return "a passing Codex test does not report usage"
	}
	return ""
}

// dockUsageLimit is the plain-language reason for leaving a route on a limit.
const dockUsageLimit = "a usage limit"

// dockAwayOf reads what state records about having left the first route. The
// reason comes from the retained history's newest handoff record; without
// one, a recorded reset time means a limit (state records it only for one),
// as does a configuration that hands off on rate limits alone.
func dockAwayOf(st *statusOut, chain *dockChainInput, tl *timelineReport, now time.Time) *dockAway {
	if st == nil || !st.TargetConfigured || st.Target == state.Primary || chain == nil {
		return nil
	}
	a := &dockAway{Return: "off"}
	for i, cand := range chain.Candidates {
		if cand.ID == st.Target {
			a.Choice = i + 2
		}
	}
	if t := st.State.LastHandoffAt; !t.IsZero() {
		a.Since = &t
	}
	if t := st.State.PrimaryResetsAt; !t.IsZero() {
		a.ResetsAt, a.ResetPassed, a.Reason = &t, !now.Before(t), dockUsageLimit
	}
	if t := st.State.LastSwitchBackTry; !t.IsZero() {
		a.LastReturnTry = &t
	}
	switch {
	case st.TakeoverBriefs.SwitchBackStopped:
		a.Return = "stopped"
	case chain.SwitchBack:
		a.Return = "on"
	}
	if tl != nil {
		for i := len(tl.Events) - 1; i >= 0; i-- {
			r := tl.Events[i].Incident
			if r == nil || r.Kind != "tick" || r.Action != "handoff" || r.DryRun || r.TargetKeyAfter == r.TargetKey {
				continue
			}
			switch r.Observation.ErrorClass {
			case "rate_limit":
				a.Reason = dockUsageLimit
			case "auth":
				a.Reason = "a sign-in failure"
			case "overloaded":
				a.Reason = "the provider being overloaded"
			case "network":
				a.Reason = "a network failure"
			}
			return a
		}
	}
	if a.Reason == "" && len(chain.FailoverOn) == 1 && chain.FailoverOn[0] == "rate_limit" {
		a.Reason = dockUsageLimit
	}
	return a
}

// dockProbePhrase words the last test's recorded result for a route that is
// not ready. Each is tied to when it was tested: it is not a live reading.
// "Test" is the interface word for what the configuration calls a probe.
func dockProbePhrase(ok bool, class, age string) string {
	if ok {
		return "Passed its test " + age + ", too long ago to count as ready"
	}
	what := "Failed its test"
	switch class {
	case "rate_limit":
		what = "Hit a usage limit when tested"
	case "auth":
		what = "Sign-in failed when tested"
	case "network":
		what = "Could not be reached when tested"
	case "overloaded":
		what = "Provider overloaded when tested"
	}
	return what + " " + age
}
