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
// registered status, status, runs, fallbacks list and the retained history.
// It adds no evidence of its own and never fills a gap with a default: what
// is missing is "unknown", and what is old is "stale". Every phrase and
// sentence it can produce is assembled in Go and says only what the record
// behind it says; the table tests map each record to its words, so wording
// that says more than its record has to be written there to pass.

const (
	dockSchemaVersion = 1
	dockMaxLoops      = registry.MaxEntries
	dockMaxNote       = 280 // runes of a hold note
	dockMaxWindows    = 4   // usage windows per route
	dockMaxIssues     = 8
	dockMaxText       = 120 // runes of a name, model or provider label
	dockMaxChain      = 8   // fallbacks listed per loop; the rest are counted
	dockMaxRoutes     = 64  // routes in the view across loops; the rest are counted
	dockMaxRouteLoops = 16  // loops listed under one route, per list; the rest are counted
	dockMaxAllowances = 32  // account lines in the allowances strip
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
	// NeedsLine is the picker's first line: what needs you, or that nothing
	// does, or that this cannot be said.
	NeedsLine string `json:"needsLine"`
	// Lede is the overview's count line.
	Lede string `json:"lede"`
	// Worth are loops that do not need a person but are not routine.
	Worth []dockWorth `json:"worth"`
	Loops []dockLoop  `json:"loops"`
	// Routes is the same data grouped by route: a model on an account at an
	// effort level, as configured. Each loop's chain entries name their
	// route by key, so the views are one feed.
	Routes        []dockRoute    `json:"routes"`
	RoutesOmitted int            `json:"routesOmitted"`
	RoutesSummary dockRoutesLine `json:"routesSummary"`
	// Allowances is one line per account: the freshest usage reading
	// recorded for any of its routes, and a usage limit a loop left one of
	// them on whose reset has not passed.
	Allowances        []dockAllowance `json:"allowances"`
	AllowancesOmitted int             `json:"allowancesOmitted"`
}

// dockRoutesLine is the count line of the routes view and the picker.
type dockRoutesLine struct {
	Count   int    `json:"count"`
	InUse   int    `json:"inUse"`
	Trouble int    `json:"trouble"`
	First   string `json:"first,omitempty"` // the key of the first route in trouble
	Picker  string `json:"picker"`          // "9 routes, 4 at a limit or not ready"
	Total   string `json:"total"`           // "9 routes"
	InUseT  string `json:"inUseText"`       // "3 in use"
	Problem string `json:"problem"`         // "4 at a limit or not ready, listed first"
}

// dockWorth is one loop on the overview that is not routine.
type dockWorth struct {
	Key   string   `json:"key"`
	Loop  string   `json:"loop"`
	State string   `json:"state"`
	Text  dockText `json:"text"`
}

// dockAllowance is what is recorded about one account's usage: the newest
// reading any loop holds for a route on it. An account is what selects the
// account a test reads (the probe's usage identity), so two accounts under
// one group name are two lines.
type dockAllowance struct {
	Title    string          `json:"title"`
	Sub      string          `json:"sub"`
	Provider string          `json:"provider,omitempty"`
	Account  string          `json:"account,omitempty"`
	Routes   int             `json:"routes"`
	Usage    dockUsageTarget `json:"usage"`
	Notes    []dockNote      `json:"notes"`
	Next     dockText        `json:"next,omitempty"`
	identity string
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
	// Waited is how long, in words.
	Waited string `json:"waited"`
	Action string `json:"action"`
}

type dockLoop struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// State is one of supervising, waiting_for_you, waiting_on_limit,
	// waiting_on_network, checks_failing, paused, stopped, finished, off,
	// outside_hours, not_checked_recently or unknown. Phrase is its label,
	// Detail one sentence about it, PickerState the picker row's second line.
	State       string `json:"state"`
	Phrase      string `json:"phrase"`
	Detail      string `json:"detail"`
	PickerState string `json:"pickerState"`
	Evidence    string `json:"evidence"` // available, partial or unavailable
	// CurrentRoute is the target the loop's records name as current. It says
	// nothing about activity: see InUse on the chain entry.
	CurrentRoute dockTarget `json:"currentRoute"`
	// Away is set when the loop's current route is not its first: each route
	// it left, attributed to the record of that departure.
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
	// Can is which changes fit the loop's control flags as read: pause when
	// not paused, stop when no stop is requested, resume when any hold is
	// set. Empty when the flags could not be read. A page offers them only
	// when the server that serves it allows changes.
	Can []string `json:"can"`
}

type dockTarget struct {
	Availability string `json:"availability"`   // known or unknown
	Role         string `json:"role,omitempty"` // main or fallback
	// Name is the configuration's name for the route, only with identifiers.
	Name     string `json:"name,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
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
	// Text is the phrase with its age; Failed counts failures in a row.
	Text   string `json:"text"`
	Failed string `json:"failed,omitempty"`
}

type dockNext struct {
	Availability string     `json:"availability"` // known, in_progress or unknown
	At           *time.Time `json:"at,omitempty"`
	Phrase       string     `json:"phrase"`
	Detail       string     `json:"detail,omitempty"`
	Text         dockText   `json:"text,omitempty"`
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
	Availability string            `json:"availability"` // available, stale or unavailable
	ObservedAt   *time.Time        `json:"observedAt,omitempty"`
	AgeSeconds   *int64            `json:"ageSeconds,omitempty"`
	Windows      []dockUsageWindow `json:"windows"`
	Omitted      int               `json:"omitted,omitempty"`
	// Reason says, when a record shows it, why there is no reading.
	Reason string `json:"reason,omitempty"`
	// FromAnotherLoop: the reading is another loop's record of the same
	// route, shown because it is newer than this loop's own.
	FromAnotherLoop bool `json:"fromAnotherLoop,omitempty"`
	// ReadLine says how old the reading is; UsedUp is a sentence about the
	// first window that is used up.
	ReadLine string   `json:"readLine"`
	UsedUp   dockText `json:"usedUp,omitempty"`
	// ReadingsNote is said once for the route when one of its windows could
	// not be projected for want of readings: where its readings come from.
	ReadingsNote string `json:"readingsNote,omitempty"`
}

type dockUsageWindow struct {
	Name        string     `json:"name"`
	UsedPercent int        `json:"usedPercent"`
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
	Stale       bool       `json:"stale"`
	Reset       dockText   `json:"reset,omitempty"`
	// Projection is what the recorded readings say about the window at its
	// reset, or why they cannot, in the words status uses. A window that
	// has reset has none: Reset already says it ended.
	Projection dockText `json:"projection,omitempty"`
}

type dockLimit struct {
	Label   string   `json:"label"`
	Phrase  string   `json:"phrase"`
	Text    dockText `json:"text"`
	Used    *int64   `json:"used,omitempty"`
	Max     *int64   `json:"max,omitempty"`
	Reached bool     `json:"reached"`
	Unknown bool     `json:"unknown,omitempty"`
}

type dockLimitWait struct {
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
	Passed   bool       `json:"passed"`
	Text     dockText   `json:"text"`
}

// dockIdentifiers is shown only on request and holds nothing that registered
// status does not already print: the registration ID and the configured BB
// thread. Paths are never included.
type dockIdentifiers struct {
	RegistrationID string `json:"registrationId"`
	Thread         string `json:"thread,omitempty"`
}

// dockChain is the loop's routes in order of preference, as fallbacks list
// reads them: the first route, then each fallback in configured order.
// Credentials, account directories, commands and paths are never part of it.
// Each entry carries a stable position and an opaque route key so later
// slices can add reorder, add and remove controls that call the routes
// commands.
type dockChain struct {
	// known, none_configured or unknown.
	Availability string `json:"availability"`
	// HandoffOn lists the error classes that move the loop automatically;
	// empty means automatic moves are off. How says so in words; Brief is the
	// count of other routes ready, and Short whether that is fewer than the
	// loop is set to keep ready.
	HandoffOn []string         `json:"handoffOn"`
	How       string           `json:"how"`
	Brief     string           `json:"brief"`
	Short     bool             `json:"short"`
	Entries   []dockChainEntry `json:"entries"`
	Omitted   int              `json:"omitted"`
	More      string           `json:"more,omitempty"`
}

type dockChainEntry struct {
	Position int `json:"position"` // 0 is the first route
	// ID is the configuration's name for the route, only with identifiers.
	ID string `json:"id,omitempty"`
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
	// Readiness is ready, ready_overdue, not_ready, not_probed, disabled,
	// unknown, main for the first route, or current for a fallback that is
	// the loop's current target, which carries no verdict because that route
	// is not tested.
	Readiness string `json:"readiness"`
	Phrase    string `json:"phrase"`
	// Title, TitleAccount and Meta say what the route is; Notes are what is
	// recorded against it, first; After are further lines about its test.
	Title        string   `json:"title"`
	TitleAccount string   `json:"titleAccount,omitempty"`
	Meta         []string `json:"meta"`
	// Named is "named <id> in its configuration", only with identifiers.
	Named    string          `json:"named,omitempty"`
	Notes    []dockNote      `json:"notes"`
	After    []string        `json:"after"`
	ProbedAt *time.Time      `json:"probedAt,omitempty"`
	ProbeAge *int64          `json:"probeAgeSeconds,omitempty"`
	Usage    dockUsageTarget `json:"usage"`
	identity string
	probeOK  bool
}

// dockRoute is one route across every loop that names it. keepgoing knows it
// from configuration only: it does not verify which account or model a BB
// thread is actually using. Lanes are not attributed to a route, because
// keepgoing records a loop's lanes only as counts.
type dockRoute struct {
	Key          string   `json:"key"`
	Provider     string   `json:"provider,omitempty"`
	Account      string   `json:"account,omitempty"`
	Model        string   `json:"model,omitempty"`
	Effort       string   `json:"effort,omitempty"`
	Tool         string   `json:"tool,omitempty"`
	Title        string   `json:"title"`
	TitleAccount string   `json:"titleAccount,omitempty"`
	Meta         []string `json:"meta"`
	// Readiness is as on a chain entry: the newest test result any loop
	// holds for this route. A route used only as a first route is never
	// tested and reads unknown.
	Readiness string          `json:"readiness"`
	Phrase    string          `json:"phrase"`
	ProbeAge  *int64          `json:"probeAgeSeconds,omitempty"`
	Usage     dockUsageTarget `json:"usage"` // the newest reading any loop holds
	// Notes are what is recorded against the route: each loop that left it,
	// a used-up window, and a usage limit on a route sharing its account.
	Notes []dockNote `json:"notes"`
	// UseText is "3 loops on it now". RunningNow are loops in use on this
	// route (see dockChainEntry.InUse); RecordedFor are loops whose records
	// name it as current but that are not in use; Standby groups loops that
	// list it and are on another route by which choice it is for them;
	// Unknown are loops that list it but whose current route could not be
	// read.
	UseText     string          `json:"useText,omitempty"`
	RunningNow  []dockRouteLoop `json:"runningNow"`
	RecordedFor []dockRouteLoop `json:"recordedFor"`
	Standby     []dockStandby   `json:"standby"`
	Unknown     []dockStandby   `json:"unknown"`
	Omitted     int             `json:"omitted"`
	More        string          `json:"more,omitempty"`
	// LeftBy are departures from this route, one per loop that left it.
	LeftBy []dockDeparture `json:"leftBy"`
	// Attention is limit (a loop left it on a usage limit that has not reset,
	// or a recorded window is used up), not_ready (its newest test failed or
	// is too old), or empty. Routes are listed with these first.
	Attention string `json:"attention,omitempty"`
	// Lanes is always unknown in this schema.
	Lanes    string `json:"lanes"`
	identity string
}

type dockRouteLoop struct {
	Key      string `json:"key"`
	Loop     string `json:"loop"`
	State    string `json:"state"`
	Phrase   string `json:"phrase"`
	Position int    `json:"position"`
	Working  int    `json:"lanesWorking"`
	Line     string `json:"line"` // what follows the loop's name
}

// dockStandby is the loops for which a route is the same choice.
type dockStandby struct {
	Label string          `json:"label"` // "Third choice for"
	Loops []dockRouteLoop `json:"loops"`
}

// dockRouteKey includes the effort level, as a route is a model on an account
// at an effort level, and the probe's usage identity (a hash of what selects
// the account a probe reads), so two accounts configured under one group are
// two routes and never share a reading.
func dockRouteKey(provider, account, model, effort, tool, usageIdentity string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{"keepgoing-dock-route", provider, account, model, effort, tool, usageIdentity}, "\x00")))
	return "r" + hex.EncodeToString(h[:6])
}

// dockRouteTitle says what is known about a route: its model, or the tool
// and that no model is configured.
func dockRouteTitle(tool, model string) string {
	switch {
	case model != "":
		return model
	case tool == "BB thread":
		return "BB thread · model not configured"
	case tool != "":
		return tool + " · default model"
	}
	return "Model not configured"
}

// dockTitleAccount is the account group the title names after "on", when it
// is a group of its own (not the provider's name) and repeats no word the
// title already has. The group is always on the detail line.
func dockTitleAccount(title, account, provider string) string {
	if account == "" || account == provider {
		return ""
	}
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(title), dockNotAlnum) {
		words[w] = true
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(account), dockNotAlnum) {
		if words[w] {
			return ""
		}
	}
	return account
}

func dockNotAlnum(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }

var dockEnterWords = map[string]string{
	"same_conversation": "continues in the same conversation", "other_conversation": "moves to another conversation",
	"new_conversation": "starts a new conversation", "headless": "runs without a window", "command": "runs a command",
}

// dockRouteMeta is the route's detail line, part by part.
func dockRouteMeta(tool, model, account, provider, effort, enter string) []string {
	out := []string{}
	if model != "" && tool != "" {
		out = append(out, tool)
	}
	if account != "" {
		out = append(out, "account group "+account)
	}
	if provider != "" && provider != account {
		out = append(out, "provider "+provider)
	}
	if effort != "" {
		out = append(out, "effort "+effort)
	} else {
		out = append(out, "effort not configured")
	}
	if w := dockEnterWords[enter]; w != "" {
		out = append(out, w)
	}
	return out
}

// dockRoutes groups every loop's chain by route. Newest evidence wins.
func dockRoutes(loops []dockLoop, now time.Time) ([]dockRoute, int) {
	index := map[string]int{}
	routes := []dockRoute{}
	omitted := 0
	type standby struct{ by map[int][]dockRouteLoop }
	stand, unknown := map[int]*standby{}, map[int]*standby{}
	add := func(m map[int]*standby, i, pos int, ref dockRouteLoop) {
		if m[i] == nil {
			m[i] = &standby{by: map[int][]dockRouteLoop{}}
		}
		m[i].by[pos] = append(m[i].by[pos], ref)
	}
	for _, l := range loops {
		for _, e := range l.Chain.Entries {
			i, ok := index[e.Route]
			if !ok {
				i = len(routes)
				index[e.Route] = i
				routes = append(routes, dockRoute{Key: e.Route, Provider: e.Provider, Account: e.Account, Model: e.Model, Effort: e.Effort, Tool: e.Tool,
					Title: e.Title, TitleAccount: e.TitleAccount, Meta: dockRouteMeta(e.Tool, e.Model, e.Account, e.Provider, e.Effort, ""),
					Readiness: "unknown", Phrase: "Unknown", Usage: dockUsageTarget{Availability: "unavailable", Windows: []dockUsageWindow{}, ReadLine: "No usage reading"},
					Notes: []dockNote{}, RunningNow: []dockRouteLoop{}, RecordedFor: []dockRouteLoop{}, Standby: []dockStandby{}, Unknown: []dockStandby{},
					LeftBy: []dockDeparture{}, Lanes: "unknown", identity: e.identity})
			}
			n := &routes[i]
			if n.identity == "" {
				n.identity = e.identity
			}
			switch {
			case e.ProbeAge != nil && (n.ProbeAge == nil || *e.ProbeAge < *n.ProbeAge) && e.Readiness != "current":
				n.Readiness, n.Phrase, n.ProbeAge = e.Readiness, e.Phrase, e.ProbeAge
			case e.Position > 0 && n.ProbeAge == nil && n.Readiness == "unknown" && e.Readiness != "current":
				// Never tested anywhere: say why (turned off, or not tested yet).
				n.Readiness, n.Phrase = e.Readiness, e.Phrase
			}
			if u := e.Usage; u.AgeSeconds != nil && (n.Usage.AgeSeconds == nil || *u.AgeSeconds < *n.Usage.AgeSeconds) {
				n.Usage = u
				n.Usage.FromAnotherLoop = false
				n.Usage.ReadLine = "Usage read " + dockAgo(now, *u.ObservedAt)
			}
			if a := l.Away; a != nil {
				for _, d := range a.Departures {
					if d.Position == e.Position {
						d.Others, d.loop, d.key = a.departureText(d, l.Name, now), l.Name, l.Key
						n.LeftBy = append(n.LeftBy, d)
					}
				}
			}
			ref := dockRouteLoop{Key: l.Key, Loop: l.Name, State: l.State, Phrase: l.Phrase, Position: e.Position, Line: l.Phrase}
			var list *[]dockRouteLoop
			switch {
			case l.CurrentRoute.Availability != "known":
				add(unknown, i, e.Position, ref)
				continue
			case e.InUse:
				list = &n.RunningNow
				if l.Children.Availability == "current" && l.Children.Working > 0 {
					ref.Working = l.Children.Working
					ref.Line += " · " + dockPlural(ref.Working, "lane", "lanes") + " working"
				}
			case e.Current:
				list = &n.RecordedFor
			default:
				add(stand, i, e.Position, ref)
				continue
			}
			if len(*list) >= dockMaxRouteLoops {
				n.Omitted++
				continue
			}
			*list = append(*list, ref)
		}
	}
	group := func(m *standby, r *dockRoute) []dockStandby {
		out := []dockStandby{}
		if m == nil {
			return out
		}
		positions := make([]int, 0, len(m.by))
		for p := range m.by {
			positions = append(positions, p)
		}
		sort.Ints(positions)
		for _, p := range positions {
			loops := m.by[p]
			if len(loops) > dockMaxRouteLoops {
				r.Omitted += len(loops) - dockMaxRouteLoops
				loops = loops[:dockMaxRouteLoops]
			}
			out = append(out, dockStandby{Label: dockCapital(dockOrdinal(p+1)) + " choice for", Loops: loops})
		}
		return out
	}
	for i := range routes {
		r := &routes[i]
		r.Standby, r.Unknown = group(stand[i], r), group(unknown[i], r)
		if n := len(r.RunningNow); n > 0 {
			r.UseText = dockPlural(n, "loop on it now", "loops on it now")
		}
		// Departures at a limit first, then the newest; the rest are counted.
		sort.SliceStable(r.LeftBy, func(a, b int) bool {
			x, y := r.LeftBy[a], r.LeftBy[b]
			if x.atLimit() != y.atLimit() {
				return x.atLimit()
			}
			return x.At != nil && (y.At == nil || x.At.After(*y.At))
		})
		if len(r.LeftBy) > dockMaxRouteLoops {
			r.Omitted += len(r.LeftBy) - dockMaxRouteLoops
			r.LeftBy = r.LeftBy[:dockMaxRouteLoops]
		}
		if r.Omitted > 0 {
			r.More = dockPlural(r.Omitted, "more loop is", "more loops are") + " not listed."
		}
		for _, d := range r.LeftBy {
			tone := "plain"
			if d.atLimit() {
				tone = "limit"
			}
			r.Notes = append(r.Notes, dockNote{Tone: tone, Text: d.Others})
		}
		if len(r.LeftBy) == 0 || !anyAtLimit(r.LeftBy) {
			if u := r.Usage.UsedUp; len(u) > 0 {
				r.Notes = append(r.Notes, dockNote{Tone: "limit", Text: u})
			}
		}
	}
	// A route on the same account as one a loop left on a usage limit that
	// has not reset says so; its own readiness is left as it is. "The same
	// account" is the same recorded account identity (see dockAccountIdentity),
	// never the same group name alone; a route with none claims nothing.
	for i := range routes {
		var best *dockDeparture
		for j := range routes {
			if i == j || routes[i].identity == "" || routes[i].identity != routes[j].identity {
				continue
			}
			for k := range routes[j].LeftBy {
				d := &routes[j].LeftBy[k]
				if !d.atLimit() {
					continue
				}
				if best == nil || (d.ResetsAt != nil && (best.ResetsAt == nil || d.ResetsAt.After(*best.ResetsAt))) {
					best = d
				}
			}
		}
		if best != nil {
			s := txt("Shares its account with a route where " + best.loop + " hit a usage limit")
			if best.At != nil {
				s = txt(s, " on ", tm(*best.At, "date"))
			}
			if best.ResetsAt != nil {
				s = txt(s, "; the provider reported a reset for ", tm(*best.ResetsAt, "date"), ".")
			} else {
				s = txt(s, "; no reset time was reported.")
			}
			s = txt(s, " This route's own test result below is separate.")
			routes[i].Notes = append(routes[i].Notes, dockNote{Tone: "limit", Text: s})
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
		if anyAtLimit(r.LeftBy) || len(r.Usage.UsedUp) > 0 {
			r.Attention = "limit"
		} else if r.Readiness == "not_ready" {
			r.Attention = "not_ready"
		}
	}
	sort.SliceStable(routes, func(i, j int) bool {
		if a, b := rank(routes[i]), rank(routes[j]); a != b {
			return a < b
		}
		return len(routes[i].RunningNow) > len(routes[j].RunningNow)
	})
	// The cap comes after the sort, so the routes left out are the least
	// important ones, and they are counted.
	if len(routes) > dockMaxRoutes {
		omitted = len(routes) - dockMaxRoutes
		routes = routes[:dockMaxRoutes]
	}
	return routes, omitted
}

func anyAtLimit(ds []dockDeparture) bool {
	for _, d := range ds {
		if d.atLimit() {
			return true
		}
	}
	return false
}

// dockAllowances groups routes by account (provider, group and usage
// identity), in route order.
func dockAllowances(routes []dockRoute) ([]dockAllowance, int) {
	index := map[string]int{}
	out := []dockAllowance{}
	omitted := 0
	for _, r := range routes {
		// A route with no recorded account identity is a line of its own: it
		// is not said to share an allowance with anything.
		key := r.Provider + "\x00" + r.Account + "\x00" + r.identity
		if r.identity == "" {
			key = "route\x00" + r.Key
		}
		i, ok := index[key]
		if !ok {
			if len(out) >= dockMaxAllowances {
				omitted++
				index[key] = -1
				continue
			}
			i = len(out)
			index[key] = i
			title := r.Account
			if title == "" {
				title = r.Provider
			}
			if title == "" {
				title = "Account not named"
			}
			out = append(out, dockAllowance{Title: title, Provider: r.Provider, Account: r.Account, identity: r.identity, Notes: []dockNote{},
				Usage: dockUsageTarget{Availability: "unavailable", Windows: []dockUsageWindow{}, ReadLine: "No reading"}})
		}
		if i < 0 {
			continue
		}
		a := &out[i]
		a.Routes++
		if u := r.Usage; u.AgeSeconds != nil && (a.Usage.AgeSeconds == nil || *u.AgeSeconds < *a.Usage.AgeSeconds) {
			a.Usage = u
			a.Usage.ReadLine = strings.Replace(u.ReadLine, "Usage read", "Read", 1)
			if u.ReadingsNote != "" {
				a.Usage.ReadingsNote = accountReadingsNote
			}
		}
		for _, d := range r.LeftBy {
			if !d.atLimit() {
				continue
			}
			where := " hit a usage limit on a route on this account"
			if a.identity == "" {
				where = " hit a usage limit on this route"
			}
			s := txt(d.loop + where)
			if d.At != nil {
				s = txt(s, " on ", tm(*d.At, "date"))
			}
			if d.ResetsAt != nil {
				s = txt(s, "; the provider reported a reset for ", tm(*d.ResetsAt, "datetime"), ".")
			} else {
				s = txt(s, "; no reset time was reported.")
			}
			a.Notes = append(a.Notes, dockNote{Tone: "limit", Text: s})
		}
	}
	names := map[string]int{}
	for _, a := range out {
		if a.identity != "" {
			names[a.Provider+"\x00"+a.Title]++
		}
	}
	seen := map[string]int{}
	for i := range out {
		a := &out[i]
		parts := []string{}
		if a.Provider != "" && a.Provider != a.Account {
			parts = append(parts, "provider "+a.Provider)
		}
		parts = append(parts, dockPlural(a.Routes, "route", "routes"))
		if a.identity == "" {
			parts = append(parts, "which account is not identified by a test")
		} else if n := names[a.Provider+"\x00"+a.Title]; n > 1 {
			seen[a.Provider+"\x00"+a.Title]++
			parts = append(parts, fmt.Sprintf("account %d of %d named %s", seen[a.Provider+"\x00"+a.Title], n, a.Title))
		}
		a.Sub = strings.Join(parts, " · ")
		if len(a.Notes) == 0 && len(a.Usage.UsedUp) > 0 {
			a.Notes = append(a.Notes, dockNote{Tone: "limit", Text: a.Usage.UsedUp})
		}
		var next *time.Time
		future := 0
		for _, w := range a.Usage.Windows {
			if w.ResetsAt != nil && !w.Stale {
				future++
				if next == nil || w.ResetsAt.Before(*next) {
					next = w.ResetsAt
				}
			}
		}
		if future > 1 && next != nil {
			a.Next = txt("Next reset ", tm(*next, "clock"))
		}
	}
	return out, omitted
}

// dockShareAcrossLoops gives each loop's route the newest usage reading and
// test result any loop holds for that same route, marked as another loop's,
// so one route does not read differently on different pages. A route is keyed
// by what selects its account, so the record is of the same account.
func dockShareAcrossLoops(loops []dockLoop, routes []dockRoute, now time.Time) {
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
				e.Usage = u
				e.Usage.FromAnotherLoop = true
				e.Usage.ReadLine = "Usage read " + dockAgo(now, *u.ObservedAt) + ", by another loop on this route"
			}
			newer := r.ProbeAge != nil && (e.ProbeAge == nil || *r.ProbeAge < *e.ProbeAge)
			if newer && r.Phrase != e.Phrase && r.Readiness != "current" && r.Readiness != "main" && r.Readiness != "unknown" {
				e.After = append(e.After, "Another loop tested it more recently ("+strings.ToLower(r.Phrase[:1])+r.Phrase[1:]+").")
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
	// Due is health.Due's reason for each fallback ("due", "probed recently",
	// "daily cap reached", ...), as the scheduler would see it now; nil when
	// the probe history was not read.
	Due map[string]string
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

// clip cuts s to n runes and to 2n bytes, whichever is shorter, so a name of
// four-byte characters costs no more than twice one of ASCII.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= n && len(s) <= 2*n {
		return s
	}
	out := []rune{}
	size := len("…")
	for _, r := range s {
		if len(out) >= n-1 || size+utf8.RuneLen(r) > 2*n {
			break
		}
		out = append(out, r)
		size += utf8.RuneLen(r)
	}
	return string(out) + "…"
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
		StaleAfterSeconds: int(staleAfter / time.Second), Identifiers: identifiers, NeedsYou: []dockNeed{}, Worth: []dockWorth{}, Loops: []dockLoop{},
		Routes: []dockRoute{}, Allowances: []dockAllowance{}, Registry: dockRegistry{Availability: "unavailable"}, Complete: complete,
		NeedsLine: "Needs you: unknown"}
	r.RoutesSummary = dockRoutesLine{Picker: "No routes", Total: "No routes", InUseT: "0 in use", Problem: "none at a limit or failing its test"}
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
	r.Routes, r.RoutesOmitted = dockRoutes(r.Loops, now)
	dockShareAcrossLoops(r.Loops, r.Routes, now)
	r.Allowances, r.AllowancesOmitted = dockAllowances(r.Routes)
	sort.SliceStable(r.NeedsYou, func(i, j int) bool {
		a, b := r.NeedsYou[i].WaitedSeconds, r.NeedsYou[j].WaitedSeconds
		if a == nil || b == nil {
			return a != nil
		}
		return *a > *b
	})
	r.NeedsLine = dockNeedsLine(r)
	r.Lede = dockLede(r)
	r.RoutesSummary = dockRoutesSummary(r)
	quiet := map[string]bool{"supervising": true, "paused": true, "off": true, "stopped": true, "finished": true, "waiting_for_you": true, "unknown": true}
	for _, l := range r.Loops {
		if quiet[l.State] && l.Away == nil {
			continue
		}
		t := txt()
		if !quiet[l.State] {
			t = txt(l.Detail)
		}
		if l.Away != nil {
			if len(t) > 0 {
				t = txt(t, " ")
			}
			t = txt(t, l.Away.Short, ": ", strings.ToLower(l.Away.Why[0].T[:1]), l.Away.Why[0].T[1:], dockText(l.Away.Why[1:]), ".")
		}
		r.Worth = append(r.Worth, dockWorth{Key: l.Key, Loop: l.Name, State: l.State, Text: t})
	}
	return r
}

// dockLede is "10 loops, 4 supervising" and what the page reads from.
func dockLede(r dockReport) string {
	sup := 0
	for _, l := range r.Loops {
		if l.State == "supervising" {
			sup++
		}
	}
	s := dockPlural(len(r.Loops), "loop", "loops")
	if sup > 0 {
		s += fmt.Sprintf(", %d supervising", sup)
	}
	if r.Unread > 0 {
		s += fmt.Sprintf(", %d could not be read", r.Unread)
	}
	return s + ". Read from keepgoing's own records; nothing here is a live view of a conversation."
}

// dockNeedsLine is the picker's first line.
func dockNeedsLine(r dockReport) string {
	if r.Registry.Availability != "available" {
		return "Needs you: unknown"
	}
	needs, held := len(r.NeedsYou), len(r.NeedsYou)-r.Unread
	switch {
	case needs == 0 && r.AllClear:
		return "Nothing needs you"
	case needs == 0:
		return "Needs you: not fully read"
	}
	words := []string{}
	if held > 0 {
		words = append(words, dockPlural(held, "loop needs you", "loops need you"))
	}
	if r.Unread > 0 {
		words = append(words, fmt.Sprintf("%d could not be read", r.Unread))
	}
	return strings.Join(words, ", ")
}

func dockRoutesSummary(r dockReport) dockRoutesLine {
	s := dockRoutesLine{Count: len(r.Routes) + r.RoutesOmitted}
	for _, x := range r.Routes {
		if len(x.RunningNow) > 0 {
			s.InUse++
		}
		if x.Attention != "" {
			if s.Trouble == 0 {
				s.First = x.Key
			}
			s.Trouble++
		}
	}
	s.Total = dockPlural(s.Count, "route", "routes")
	s.Picker = s.Total
	s.InUseT = fmt.Sprintf("%d in use", s.InUse)
	if s.Trouble > 0 {
		s.Picker += fmt.Sprintf(", %d at a limit or not ready", s.Trouble)
		s.Problem = fmt.Sprintf("%d at a limit or not ready, listed first", s.Trouble)
	} else {
		s.Problem = "none at a limit or failing its test"
	}
	if r.RoutesOmitted > 0 {
		s.Problem += fmt.Sprintf("; %d more routes are not shown", r.RoutesOmitted)
	}
	return s
}

// dockUnreadable is the entry for a loop whose hold state could not be read.
// Such a loop is never left out of what needs a person.
func dockUnreadable(issues []string) *dockNeed {
	n := &dockNeed{Kind: "unreadable", Reason: "It could not be read, so whether it needs you is unknown.",
		Action: "Look at it: run keepgoing status with its configuration.", Waited: "Could not be read"}
	switch code := firstIssue(issues); code {
	case "transfer_pending":
		n.Reason, n.Action = "A move to another conversation is in progress, which holds it.", "Finish the move with keepgoing transfer recover."
	case "binding_unavailable", "binding_changed", "registration_changed", "registration_unavailable", "target_binding_mismatch", "control_unavailable", "state_unavailable", "cancelled":
		n.Reason = dockIssue(code) + " Whether it needs you is unknown."
	}
	return n
}

// dockTesting says whether keepgoing runs scheduled tests for a loop now,
// and if not, why: a held, off or unchecked loop runs no check, so no test.
type dockTesting struct {
	notWhile string          // "while the loop is paused"; empty when tests run
	off      bool            // scheduled tests are switched off
	eligible map[string]bool // fallbacks the scheduler tests (nil: unknown)
}

func dockTestingOf(state string, st *statusOut, chain *dockChainInput) dockTesting {
	t := dockTesting{}
	switch state {
	case "paused":
		t.notWhile = "while the loop is paused"
	case "waiting_for_you":
		t.notWhile = "while the loop is held for a person"
	case "finished":
		t.notWhile = "while the loop is marked finished"
	case "off":
		t.notWhile = "while supervision is off"
	case "stopped":
		t.notWhile = "while a stop is requested"
	case "outside_hours":
		t.notWhile = "outside the loop's operating hours"
	case "not_checked_recently":
		t.notWhile = "while no check runs"
	}
	if t.notWhile == "" && st != nil && !st.State.NetworkDownSince.IsZero() && !st.State.NetworkDistrusted {
		// A scheduled test is skipped while the network is down.
		t.notWhile = "while the network is down"
	}
	t.off = chain == nil || chain.ProbeEvery <= 0
	if st != nil && st.Redundancy != nil {
		t.eligible = map[string]bool{}
		for _, id := range st.Redundancy.Eligible {
			t.eligible[id] = true
		}
	}
	return t
}

func projectDockLoop(in dockInput, now time.Time, staleAfter time.Duration, identifiers bool) (dockLoop, *dockNeed) {
	l := dockLoop{Key: dockKey(in.Entry.ID), Name: clip(in.Entry.Loop, dockMaxText), Evidence: in.Client.Outcome,
		Limits: []dockLimit{}, Issues: []string{}, Can: []string{}}
	if snap := in.Client.Snapshot; snap != nil && snap.Controls != nil {
		c := snap.Controls
		if !c.Paused {
			l.Can = append(l.Can, "pause")
		}
		if !c.Stop {
			l.Can = append(l.Can, "stop")
		}
		if c.Paused || c.Stop || c.AwaitingUser || c.Completed {
			l.Can = append(l.Can, "resume")
		}
	}
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
		l.CurrentRoute = dockTarget{Availability: "known", Role: dockRole(st.Target), Tool: dockTool(a.Adapter),
			Provider: clip(a.Provider, dockMaxText), Model: clip(a.Model, dockMaxText)}
		if identifiers {
			l.CurrentRoute.Name = clip(st.Target, dockMaxText)
			if a.BBThread != "" && transferThreadID.MatchString(a.BBThread) {
				l.Identifiers.Thread = a.BBThread
			}
		}
	}

	l.Children = dockChildWork(in.Runs, in.RunsKnown)
	l.Decision = dockLastDecision(st, in.Timeline, now)
	l.Device = dockDeviceOf(st, in.Hostname, identifiers, now)
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
	l.NextCheck = dockNextCheck(snap, now)
	l.Fallbacks = dockFallbackReadiness(st, in.Chain)
	if st != nil {
		l.Limits = dockLimits(st, now)
		if w := st.LimitWait; w.State != "none" {
			lw := &dockLimitWait{Passed: w.ResetPassed}
			switch {
			case dockSaneReset(now, w.ResetsAt) && w.ResetPassed:
				lw.ResetsAt, lw.Text = w.ResetsAt, txt("Provider limit reset ", dockUntil(now, *w.ResetsAt))
			case dockSaneReset(now, w.ResetsAt):
				lw.ResetsAt, lw.Text = w.ResetsAt, txt("Provider limit resets ", dockUntil(now, *w.ResetsAt))
			case w.ResetsAt != nil && !w.ResetsAt.IsZero():
				lw.Text = txt("Provider limit: the reported reset is more than a year away; it is not shown")
			default:
				lw.Text = txt("Provider limit: no reset time was reported")
			}
			l.LimitWait = lw
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
			// The record status prints as "network: down at the last
			// connectivity check". State keeps when a connectivity check first
			// failed, and clears it when one passes; when a later check last
			// ran one is not recorded, so only that first time is said.
			l.State, l.Phrase = "waiting_on_network", "Waiting for the network"
			l.Detail = "A connectivity check found the network down " + dockAgo(now, st.State.NetworkDownSince) + ", and no connectivity check since has found it back. Work that needs the network waits until one does."
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
			if need.SinceEvidence == "hold_recorded" {
				need.Waited = "Waiting " + dockSpan(now.Sub(*need.Since))
			} else {
				need.Waited = "Waiting at least " + dockSpan(now.Sub(*need.Since))
			}
		} else if need.Waited == "" {
			need.Waited = "Waiting time unknown"
		}
	}
	use := dockUse(l, st)
	l.Lanes = dockLaneList(in.Runs, in.RunsKnown, identifiers, use.Target != "", now)
	l.Chain = dockHandoffChain(in.Chain, use, dockTestingOf(l.State, st, in.Chain), l.Fallbacks, now, identifiers)
	l.Away = dockAwayOf(st, in.Chain, l.Chain.Entries, in.Timeline, now)
	l.PickerState = l.Phrase
	if l.Away != nil {
		l.PickerState += " · " + l.Away.Picker
		for _, d := range l.Away.Departures {
			for i := range l.Chain.Entries {
				if e := &l.Chain.Entries[i]; e.Position == d.Position {
					tone := "plain"
					if d.atLimit() {
						tone = "limit"
					}
					// What is recorded against the route comes before anything else.
					e.Notes = append([]dockNote{{Tone: tone, Text: d.Text}}, e.Notes...)
					if d.atLimit() {
						e.Notes = dockDropUsedUp(e.Notes, e.Usage.UsedUp)
					}
				}
			}
		}
	}
	return l, need
}

// dockDropUsedUp removes the used-up note when a limit departure already says
// the route is at a limit.
func dockDropUsedUp(notes []dockNote, usedUp dockText) []dockNote {
	if len(usedUp) == 0 {
		return notes
	}
	out := notes[:0]
	for _, n := range notes {
		if len(n.Text) == len(usedUp) && len(n.Text) > 0 && n.Text[0].T == usedUp[0].T {
			continue
		}
		out = append(out, n)
	}
	return out
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
	c := dockCheck{Availability: "unknown", Phrase: "Unknown", Text: "Unknown"}
	if st == nil {
		return c
	}
	c.FailedInRow = st.State.FailedChecks
	if c.FailedInRow > 1 {
		c.Failed = fmt.Sprintf("%d checks in a row could not observe or act", c.FailedInRow)
	}
	if st.State.CheckedAt.IsZero() {
		c.Availability, c.Phrase, c.Text = "never", "No check recorded", "No check recorded yet"
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
	c.Text = c.Phrase + " · " + dockAgo(now, at)
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

func dockNextCheck(snap *clientSnapshot, now time.Time) dockNext {
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
		u := dockUntil(now, *d.NextCheckAt)
		if len(u) > 0 && u[0].T != "" {
			u[0].T = dockCapital(u[0].T)
		}
		n.Text = u
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
	return clip(strings.ReplaceAll(key, "_", " "), 24)
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
			l.Text = txt(tm(t, "datetime"))
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
	for i := range out {
		if len(out[i].Text) == 0 {
			out[i].Text = txt(out[i].Phrase)
		}
	}
	return out
}

// dockDuration prints a configured length of time: "45 min", "3 h 12 min",
// "24 h", "2 days 6 h".
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

func dockUsageOf(v usageView, now time.Time) dockUsageTarget {
	t := dockUsageTarget{Availability: v.State, ObservedAt: v.ObservedAt, AgeSeconds: v.AgeSeconds, Windows: []dockUsageWindow{}, ReadLine: "No usage reading"}
	if t.Availability != "available" && t.Availability != "stale" {
		t.Availability = "unavailable"
		return t
	}
	if v.ObservedAt != nil {
		t.ReadLine = "Usage read " + dockAgo(now, *v.ObservedAt)
	}
	for _, w := range v.Windows {
		win := dockUsageWindow{Name: dockWindowName(w.Key), UsedPercent: min(100, max(0, usagePercent(w.Used))), Stale: w.Stale}
		if !w.Stale {
			win.Projection = dockProjection(w.Projection, w.ResetsAt)
			if w.Projection.needsReadings() && v.ProbesOnly {
				t.ReadingsNote = readingsNote
			}
		}
		far := w.ResetsAt != nil && !w.ResetsAt.IsZero() && !dockSaneReset(now, w.ResetsAt)
		if dockSaneReset(now, w.ResetsAt) {
			win.ResetsAt = w.ResetsAt
			if w.Stale {
				win.Reset = txt("That window ended ", tm(*w.ResetsAt, "clock"))
			} else {
				win.Reset = txt("Resets ", dockUntil(now, *w.ResetsAt))
			}
		}
		if far {
			win.Reset = txt("Reported to reset more than a year away; not shown")
		}
		// A used-up window is said even when it is past the cap of windows shown.
		if len(t.UsedUp) == 0 && !win.Stale && win.UsedPercent >= 100 && v.ObservedAt != nil {
			s := txt(dockCapital(win.Name) + " allowance used up when read " + dockAgo(now, *v.ObservedAt))
			switch {
			case win.ResetsAt != nil:
				s = txt(s, "; resets ", dockUntil(now, *win.ResetsAt), ".")
			case far:
				s = txt(s, "; the reported reset is more than a year away and is not shown.")
			default:
				s = txt(s, "; no reset time was reported.")
			}
			t.UsedUp = s
		}
		if len(t.Windows) >= dockMaxWindows {
			t.Omitted++
			continue
		}
		t.Windows = append(t.Windows, win)
	}
	return t
}

// accountReadingsNote is readingsNote for a line of the allowances strip,
// which is an account, not a route.
const accountReadingsNote = "Readings for this account come only from its routes' probes."

// dockProjection words a window's projection as status does, with each
// time as a part the page shows in its own time zone and each length of time
// in the page's own style ("2 days 9 h", "25 h").
func dockProjection(p projectionView, resetsAt *time.Time) dockText {
	if p.State == "" {
		return nil
	}
	parts := p.words(resetsAt, func(t time.Time) any { return tm(t, "datetime") }, dockSpan)
	if first, ok := parts[0].(string); ok {
		parts[0] = dockCapital(first)
	}
	return txt(parts...)
}

func dockHandoffChain(in *dockChainInput, use dockRouteUse, test dockTesting, f dockFallbacks, now time.Time, identifiers bool) dockChain {
	c := dockChain{Availability: "unknown", HandoffOn: []string{}, Entries: []dockChainEntry{}}
	if in == nil {
		c.How = "Unknown: its configuration could not be read."
		return c
	}
	for _, class := range in.FailoverOn {
		c.HandoffOn = append(c.HandoffOn, clip(class, 40))
	}
	entry := func(pos int, cand config.Candidate) dockChainEntry {
		ref := refOf(cand.Agent)
		tool, model, group, provider, effort := dockTool(ref.Adapter), clip(ref.Model, dockMaxText), clip(ref.Group, dockMaxText), clip(ref.Provider, dockMaxText), dockEffort(cand.Agent)
		identity := probe.UsageIdentity(cand)
		e := dockChainEntry{Position: pos, Tool: tool, Provider: provider,
			Route: dockRouteKey(ref.Provider, ref.Group, ref.Model, effort, ref.Adapter, identity), Account: group, Effort: effort, Model: model,
			Disabled: cand.Disabled, Readiness: "unknown", Phrase: "Unknown", Notes: []dockNote{}, After: []string{},
			Usage: dockUsageTarget{Availability: "unavailable", Windows: []dockUsageWindow{}, ReadLine: "No usage reading"}}
		if identifiers {
			e.ID = clip(cand.ID, dockMaxText)
		}
		if use.Target != "" && cand.ID == use.Target {
			e.Current, e.InUse, e.Use = true, use.InUse, use.Phrase
		}
		e.Enter, e.Source = dockEnter(pos, cand.Agent, in.Primary.Agent), "own"
		if pos == 0 {
			e.Source = "loop"
		}
		e.Title = dockRouteTitle(tool, model)
		e.TitleAccount = dockTitleAccount(e.Title, group, provider)
		e.Meta = dockRouteMeta(tool, model, group, provider, effort, e.Enter)
		if e.ID != "" {
			e.Named = "named " + e.ID + " in its configuration"
		}
		return e
	}
	main := entry(0, in.Primary)
	main.Readiness, main.Phrase = "main", "First route"
	if in.PrimaryUse != nil {
		main.Usage = dockUsageOf(*in.PrimaryUse, now)
	}
	if main.Usage.Availability == "unavailable" && main.Current {
		main.Usage.Reason = "a loop's current route is not tested"
	}
	if len(main.Usage.UsedUp) > 0 {
		main.Notes = append(main.Notes, dockNote{Tone: "limit", Text: main.Usage.UsedUp})
	}
	main.identity = dockAccountIdentity(in.Primary, main.Usage)
	c.Entries = append(c.Entries, main)
	if len(in.Candidates) == 0 {
		c.Availability = "none_configured"
		c.How, c.Brief = "Nothing takes over if the first route cannot continue.", "No other routes set up"
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
		age := ""
		if ok && row.Last != nil && !row.Last.At.IsZero() {
			at := row.Last.At
			e.ProbedAt, e.ProbeAge = &at, secondsSince(now, at)
			age = dockAgo(now, at)
			e.probeOK = row.Last.OK
		}
		// Why no scheduled test ran since, when that is so.
		notTested := ""
		switch {
		case test.notWhile != "":
			notTested = "not tested again " + test.notWhile
		case test.off:
			notTested = "scheduled tests are off for this loop"
		case test.eligible != nil && !test.eligible[cand.ID]:
			notTested = "not tested while the loop runs on a route in the same account group"
		case in.Due[cand.ID] == "scheduled probes are off":
			notTested = "scheduled tests are off for this loop"
		case in.Due[cand.ID] == "daily cap reached":
			// health.Due: the scheduled tests allowed per day are spent.
			notTested = "the day's tests are used up"
		}
		switch {
		case cand.Disabled:
			e.Readiness, e.Phrase = "disabled", "Turned off in its configuration"
		case in.Summary == nil || !ok:
			c.Availability = "unknown"
			e.Phrase = "Readiness unknown: its test history could not be read"
		case e.Current && (row.Last == nil || row.Last.OK):
			// The loop's current target is not tested, so an old test says
			// nothing against it. Only a recorded failure is a verdict.
			e.Readiness, e.Phrase = "current", e.Use
			if age != "" {
				e.After = append(e.After, "Not tested while it is the loop's route; last tested "+age+".")
			}
		case row.Last == nil:
			e.Readiness, e.Phrase = "not_probed", "Not tested yet"
			if notTested != "" {
				e.Phrase += "; " + notTested
			}
		case row.Healthy:
			e.Readiness, e.Phrase = "ready", "Ready: passed its test "+age
			if in.ProbeEvery > 0 && now.Sub(row.Last.At) > in.ProbeEvery {
				// Still counted ready, but the test is older than its schedule:
				// say why no test ran, or that one is overdue.
				if notTested != "" {
					e.Readiness, e.Phrase = "ready_overdue", "Ready: passed its test "+age+"; "+notTested
				} else {
					e.Readiness, e.Phrase = "ready_overdue", "Passed its test "+age+"; a new test is overdue (it is tested every "+dockDuration(in.ProbeEvery)+")"
				}
			}
		default:
			e.Readiness, e.Phrase = "not_ready", dockProbePhrase(row.Last.OK, row.Last.ErrorClass, age)
			if row.Last.OK && notTested != "" {
				e.Phrase += "; " + notTested
			}
		}
		if ok {
			e.Usage = dockUsageOf(row.Usage, now)
		}
		if e.Usage.Availability == "unavailable" {
			e.Usage.Reason = dockUsageReason(cand, row, ok, e.Current)
		}
		if len(e.Usage.UsedUp) > 0 {
			e.Notes = append(e.Notes, dockNote{Tone: "limit", Text: e.Usage.UsedUp})
		}
		e.identity = dockAccountIdentity(cand, e.Usage)
		c.Entries = append(c.Entries, e)
	}
	if c.Omitted > 0 {
		c.More = dockPlural(c.Omitted, "more route is", "more routes are") + " not shown."
	}
	count := len(in.Candidates)
	if words := dockHandoffClasses(c.HandoffOn); words != "" {
		c.How = "This loop's own list, in order of preference. It moves automatically on " + words + ", to the first ready route below."
	} else {
		c.How = "This loop's own list, in order of preference. Automatic moves are off; " + dockPlural(count, "other route is", "other routes are") + " set up for a manual move."
	}
	switch {
	case f.Availability != "known":
		c.Brief = "Readiness of other routes unknown"
	case f.Ready == 0 && f.Eligible > 0:
		c.Brief, c.Short = "No other route is ready", true
	case f.Ready == 0:
		c.Brief, c.Short = "No other route is eligible to take over", true
	default:
		c.Brief, c.Short = fmt.Sprintf("%d of %d other routes ready", f.Ready, f.Eligible), !f.Enough
	}
	return c
}

// dockAccountIdentity is the route's account identity when one is recorded:
// the probe's usage identity, when its configuration selects an account (an
// environment or an executable of its own) or a usage reading was recorded
// under it. Without either the usage identity is only the provider and group
// names, which say nothing about which account a route uses, so it is "".
func dockAccountIdentity(c config.Candidate, u dockUsageTarget) string {
	if len(c.ProbeEnv()) == 0 && c.ProbeCLI() == "" && u.Availability == "unavailable" {
		return ""
	}
	return probe.UsageIdentity(c)
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
