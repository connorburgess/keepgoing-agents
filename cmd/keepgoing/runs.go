package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// runs is an offline projection of graph.json. It reads private records
// without any lock, never creates a file or directory, runs no adapter or
// provider command, and changes no control. Everything it prints was recorded
// earlier: nothing is a live observation.

const (
	runsSchemaVersion = 2
	runsDefaultLimit  = 10
	runsMaxLimit      = 256
	runsLatestSends   = 3
)

// Evidence labels for a chat/session reference.
const (
	evidenceConfigured  = "configured"  // the open run's link for the saved active target, still matching configuration
	evidenceObserved    = "observed"    // the open run's link for the saved active target, still matching the runner's saved resume ID
	evidenceHistorical  = "historical"  // a closed run, another target, a superseded chat, or no saved active target to compare
	evidenceUnavailable = "unavailable" // nothing recorded
)

// Record availability. not_recorded means the file does not exist, which is
// different from a default: nothing is reported in its place.
const (
	recordAvailable   = "available"
	recordRecorded    = "recorded"
	recordNotRecorded = "not_recorded"
	recordUnavailable = "unavailable"
)

// runsRefPattern is every character a recorded reference can contain. --id is
// refused outside it, so it can be echoed without forging output lines.
var runsRefPattern = regexp.MustCompile(`^[A-Za-z0-9._:@/-]{1,512}$`)

type runsReport struct {
	SchemaVersion int        `json:"schemaVersion"`
	ReadAt        time.Time  `json:"readAt"`
	View          string     `json:"view"` // loop, node or registry
	Loops         []runsLoop `json:"loops"`
	Node          *runsNode  `json:"node,omitempty"`
	Omitted       int        `json:"omitted"`
	Limitations   []string   `json:"limitations"`
}

type runsLoop struct {
	RegistrationID string `json:"registrationId,omitempty"`
	// Registration says whether the graph has seen this registry reference
	// used: recorded, not_recorded, or unavailable when the graph is.
	Registration string `json:"registration,omitempty"`
	// Binding is set with a registry selector: available when the registered
	// configuration still names this loop and runtime directory, otherwise
	// unavailable, and then nothing is read from the registered directory.
	Binding      string      `json:"binding,omitempty"`
	Loop         string      `json:"loop,omitempty"`
	Graph        runsGraph   `json:"graph"`
	Control      runsControl `json:"control"`
	Conflict     string      `json:"conflict,omitempty"`
	ActiveTarget runsTarget  `json:"activeTarget"`
	Current      *runsRun    `json:"current,omitempty"`
	Recent       []runsRun   `json:"recent"`
	OmittedRuns  int         `json:"omittedRuns"`
	Blocker      runsBlocker `json:"blocker"`
}

type runsGraph struct {
	Availability  rungraph.Availability `json:"availability"`
	SchemaVersion int                   `json:"schemaVersion,omitempty"`
	LoopID        string                `json:"loopId,omitempty"`
	UpdatedAt     *time.Time            `json:"updatedAt,omitempty"`
	Dropped       *rungraph.Dropped     `json:"dropped,omitempty"`
}

type runsControl struct {
	Availability string `json:"availability"`
	State        string `json:"state,omitempty"`
}

// The saved active target alias from state.json. It is what the last check
// recorded, not a live observation of where the loop is running.
type runsTarget struct {
	Availability string `json:"availability"`
	Alias        string `json:"alias,omitempty"`
}

type runsUnavailable struct {
	Availability string `json:"availability"`
	Reason       string `json:"reason"`
}

type runsRun struct {
	ID           string          `json:"id"`
	State        string          `json:"state"` // open or completion_declared
	Origin       rd.Origin       `json:"origin"`
	RecordedAt   time.Time       `json:"recordedAt"`
	Source       rungraph.Source `json:"source"`
	ClosedAt     *time.Time      `json:"closedAt,omitempty"`
	ClosedSource rungraph.Source `json:"closedSource,omitempty"`
	Reopens      string          `json:"reopens,omitempty"`
	// Invocation is the routine invocation carried by the operation that
	// opened this run. Triggers are later invocations recorded while it was
	// open; none of them opened, reopened or closed it.
	Invocation      string        `json:"invocation,omitempty"`
	Triggers        []runsTrigger `json:"triggers"`
	OmittedTriggers int           `json:"omittedTriggers"`
	// SessionEvidence is unavailable when Sessions is empty, otherwise the
	// strongest label among them.
	SessionEvidence      string           `json:"sessionEvidence"`
	Sessions             []runsSession    `json:"sessions"`
	OmittedSessions      int              `json:"omittedSessions"`
	GoalRevisions        []runsGoal       `json:"goalRevisions"`
	OmittedGoalRevisions int              `json:"omittedGoalRevisions"`
	Attempts             runsAttempts     `json:"attempts"`
	Unrecorded           []runsUnrecorded `json:"unrecorded"`
}

type runsSession struct {
	Ref        string          `json:"ref"`
	Provider   string          `json:"provider"`
	ID         string          `json:"id"`
	Target     string          `json:"target"`
	Evidence   string          `json:"evidence"`
	Source     rungraph.Source `json:"source"`
	RecordedAt time.Time       `json:"recordedAt"`
	Link       runsUnavailable `json:"link"`
}

type runsTrigger struct {
	Ref        string          `json:"ref"`
	Source     rungraph.Source `json:"source"`
	RecordedAt time.Time       `json:"recordedAt"`
}

type runsGoal struct {
	Ref        string          `json:"ref"`
	Source     rungraph.Source `json:"source"`
	RecordedAt time.Time       `json:"recordedAt"`
}

type runsAttempts struct {
	Total       int           `json:"total"`
	Unconfirmed int           `json:"unconfirmed"`
	Latest      []runsAttempt `json:"latest"`
}

type runsAttempt struct {
	ID         string     `json:"id"`
	Run        string     `json:"run"`
	Target     string     `json:"target"`
	Action     string     `json:"action"`
	Mode       string     `json:"mode"`
	Outcome    rd.Outcome `json:"outcome"`
	RecordedAt time.Time  `json:"recordedAt"`
	ReturnedAt *time.Time `json:"returnedAt,omitempty"`
	GoalRef    string     `json:"goalRef,omitempty"`
	// Session is the one chat/session recorded for this attempt's run and
	// target alias. It is derived from those two recorded fields.
	Session runsAttemptSession `json:"session"`
}

type runsAttemptSession struct {
	Availability string `json:"availability"` // recorded, ambiguous or unavailable
	Ref          string `json:"ref,omitempty"`
}

type runsUnrecorded struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

type runsNode struct {
	Ref string `json:"ref"`
	// Status is found, not_recorded, not_recorded_or_expired, or unavailable
	// when the graph exists but cannot be read, or when a child reference is
	// in a listing that is not current; Reason then says why.
	Status       rungraph.Status `json:"status"`
	Reason       string          `json:"reason,omitempty"`
	Type         string          `json:"type,omitempty"`
	RecordedAt   *time.Time      `json:"recordedAt,omitempty"`
	Source       rungraph.Source `json:"source,omitempty"`
	Run          *runsRun        `json:"run,omitempty"`
	Attempt      *runsAttempt    `json:"attempt,omitempty"`
	Edges        []runsEdge      `json:"edges"`
	OmittedEdges int             `json:"omittedEdges"`
	// Attempts lists, for a chat/session node, the sends recorded for it
	// through its run and target alias. Newest first.
	Attempts        []runsAttempt `json:"attempts,omitempty"`
	OmittedAttempts int           `json:"omittedAttempts,omitempty"`
	// AmbiguousAttempts counts sends in this chat's runs, for its target
	// alias, that cannot be attributed because the alias named several chats.
	AmbiguousAttempts int `json:"ambiguousAttempts,omitempty"`
}

type runsEdge struct {
	Type       string          `json:"type"`
	To         string          `json:"to"`
	Target     string          `json:"target,omitempty"`
	Source     rungraph.Source `json:"source"`
	RecordedAt time.Time       `json:"recordedAt"`
	// Resolves is false when the referenced node is no longer retained.
	Resolves bool `json:"resolves"`
}

var runsLimitations = []string{
	"Everything here was recorded by an earlier check or control command; nothing is a live observation, and a session reference does not show that the conversation exists or can be resumed.",
	"A session link is unavailable: no provider link format is validated yet.",
	"A blocker is what the last check that observed child threads verified, and who owns the loop. It is not a live view of the other conversation, and showing it grants no control over it. Who holds the tick or daemon lock is not identified.",
	"The saved active target is what the last check recorded. Target moves are in keepgoing timeline, not here.",
}

func runsCommand(e env, args []string) int {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Never echo parser or config values.
	path := fs.String("c", "keepgoing.yaml", "configuration used only to locate state_dir")
	dir := fs.String("state-dir", "", "read this directory without loading configuration")
	registration := fs.String("registry-id", "", "select a registered loop by its loop_ reference")
	all := fs.Bool("all", false, "list every registered loop with its current run")
	directory := fs.String("directory", "", "private registry directory: required reading for --registry-id and --all, and with -c or --state-dir consulted only to name a blocking conversation's loop")
	ref := fs.String("id", "", "show one recorded reference and what it links to")
	limit := fs.Int("limit", runsDefaultLimit, "recent runs, edges or registered loops to print (1-256)")
	asJSON := fs.Bool("json", false, "print the versioned report")
	stale := fs.Duration("stale-after", defaultStaleAfter, "age at which the last check's child listing is too old to report as current; set above your scheduler interval")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("runs: invalid arguments; see runs --help")
		return exitUsage
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	selectors := 0
	for _, name := range []string{"c", "state-dir", "registry-id", "all"} {
		if set[name] {
			selectors++
		}
	}
	registered := set["registry-id"] || *all
	if fs.NArg() != 0 || selectors > 1 || *limit < 1 || *limit > runsMaxLimit || *stale <= 0 || (set["state-dir"] && *dir == "") ||
		(set["registry-id"] && !clientRegistrationID.MatchString(*registration)) ||
		(*all && set["id"]) || (set["id"] && !runsRefPattern.MatchString(*ref)) {
		e.errf("runs: select one of -c, --state-dir, --registry-id or --all; --id needs one loop; limit 1-256; a positive stale-after; no positional arguments")
		return exitUsage
	}
	r := runsReport{SchemaVersion: runsSchemaVersion, ReadAt: runsNow(), View: "loop", Loops: []runsLoop{}, Limitations: runsLimitations}
	failed := false
	switch {
	case registered:
		regDir, err := registryDirectory(*directory)
		if err != nil {
			e.errf("runs: registry unavailable; inspect the private registry")
			return exitFailed
		}
		entries, ok := runsEntries(regDir, *registration, *all)
		if !ok {
			e.errf("runs: registry or registration unavailable; list the registry before retrying")
			return exitFailed
		}
		// Every entry of this registry may be consulted to see whether a
		// blocking conversation is itself a registered loop.
		allEntries, allKnown := runsEntries(regDir, "", true)
		if *all {
			r.View = "registry"
		}
		for i, entry := range entries {
			if i >= *limit {
				r.Omitted = len(entries) - *limit
				break
			}
			limitRuns := *limit
			if *all {
				limitRuns = 0 // The registry view shows each loop's current run only.
			}
			// Revalidate the binding as registered status does. If the
			// configuration now names another loop or runtime directory, the
			// registered directory's runs are not printed under this name.
			cfg, bound := clientConfig(entry)
			if !bound {
				failed = true
				r.Loops = append(r.Loops, runsLoop{RegistrationID: entry.ID, Loop: entry.Loop, Binding: recordUnavailable, Registration: recordUnavailable,
					Graph: runsGraph{Availability: recordUnavailable}, Control: runsControl{Availability: recordUnavailable}, ActiveTarget: runsTarget{Availability: recordUnavailable},
					Recent: []runsRun{}, Blocker: runsBlocker{Availability: blockerUnknown, Reason: "binding_unavailable", Children: []runsChild{},
						Owner: runsOwner{Availability: blockerUnknown, Reason: "binding_unavailable", Supervised: runsSupervised{Availability: "registry_not_consulted"}},
						Lock:  runsUnavailable{Availability: recordUnavailable, Reason: "lock_holder_not_identified"}}})
				if set["id"] {
					r.View, r.Node = "node", &runsNode{Ref: *ref, Status: recordUnavailable, Edges: []runsEdge{}}
				}
				continue
			}
			c := runsContext{dir: entry.StateDir, cfg: cfg, limit: *limit, staleAfter: *stale, registry: allEntries, registryKnown: allKnown, viaRegistry: true}
			loop, bad := c.readLoop(limitRuns)
			loop.RegistrationID, loop.Loop, loop.Binding = entry.ID, entry.Loop, recordAvailable
			loop.Registration = recordUnavailable
			if loop.Graph.Availability == rungraph.Available || loop.Graph.Availability == rungraph.Missing {
				loop.Registration = recordNotRecorded
				if n, status := c.g.Lookup(entry.ID); status == rungraph.Found && n.Type == rungraph.RegistrationNode {
					loop.Registration = recordRecorded
				}
			}
			failed = failed || bad
			r.Loops = append(r.Loops, loop)
			if set["id"] {
				r.View, r.Node = "node", c.readNode(*ref)
			}
		}
	default:
		c := runsContext{dir: *dir, limit: *limit, staleAfter: *stale}
		if set["directory"] {
			// An explicitly named registry is consulted only to see whether a
			// blocking conversation is itself a registered loop.
			regDir, err := registryDirectory(*directory)
			if err != nil {
				e.errf("runs: registry unavailable; inspect the private registry")
				return exitFailed
			}
			if c.registry, c.registryKnown = runsEntries(regDir, "", true); !c.registryKnown {
				e.errf("runs: registry unavailable; inspect the private registry")
				return exitFailed
			}
		}
		name := ""
		if *dir == "" {
			cfg, err := config.Load(*path)
			if err != nil {
				e.errf("runs: configuration unavailable; repair it or select --state-dir to read private records directly")
				return exitUsage
			}
			c.dir, c.cfg, name = cfg.Loop.StateDir, cfg, cfg.Loop.Name
		}
		loop, bad := c.readLoop(*limit)
		loop.Loop, failed = name, bad
		r.Loops = append(r.Loops, loop)
		if set["id"] {
			r.View, r.Node = "node", c.readNode(*ref)
		}
	}
	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if enc.Encode(r) != nil {
			return exitFailed
		}
	} else {
		printRuns(e.stdout, r)
	}
	// A missing graph, a hold or an unknown reference is a valid answer.
	// Records that exist but cannot be read are a partial result.
	if failed {
		return exitFailed
	}
	return exitOK
}

func runsEntries(dir, id string, all bool) ([]registry.Entry, bool) {
	if !all {
		entry, ok := clientEntry(dir, id)
		return []registry.Entry{entry}, ok
	}
	f, err := registry.Load(dir)
	return f.Entries, err == nil
}

// runsContext is one loop's read. cfg is nil with --state-dir: then nothing
// can be compared with configuration.
type runsContext struct {
	dir       string
	cfg       *config.Config
	limit     int
	g         rungraph.Graph
	available rungraph.Availability
	active    string
	// registry is consulted only to resolve a blocking conversation to a
	// registered loop. viaRegistry selects the registered-view display rules.
	registry      []registry.Entry
	registryKnown bool
	viaRegistry   bool
	staleAfter    time.Duration
	// candidateList is the registry's validated BB loops, built once.
	candidateList    []runsCandidate
	candidatesLoaded bool
	// waiting is the one blocker answer every view of this loop uses.
	waiting runsBlocker
}

// absent reports whether a record file does not exist, without creating or
// following anything.
func absent(dir, name string) bool {
	_, err := os.Lstat(filepath.Join(dir, name))
	return errors.Is(err, os.ErrNotExist)
}

// readLoop reads one state directory. Each file is an independent snapshot;
// none is locked and none is created. A file that does not exist is reported
// as not_recorded, never as the default it would be read as.
func (c *runsContext) readLoop(limitRuns int) (runsLoop, bool) {
	loop := runsLoop{Recent: []runsRun{}}
	c.g, c.available = rungraph.Load(c.dir)
	failed := c.available == rungraph.Unsupported || c.available == rungraph.Corrupt
	loop.Graph = runsGraph{Availability: c.available}
	st, err := state.Load(c.dir)
	stateKnown := false
	switch {
	case absent(c.dir, state.FileName):
		loop.ActiveTarget = runsTarget{Availability: recordNotRecorded}
	case err != nil:
		loop.ActiveTarget, failed = runsTarget{Availability: recordUnavailable}, true
	default:
		c.active, stateKnown = st.Target(), true
		loop.ActiveTarget = runsTarget{Availability: recordRecorded, Alias: c.active}
	}
	ctl, err := control.Load(c.dir)
	controlKnown := false
	switch {
	case absent(c.dir, control.FileName):
		loop.Control = runsControl{Availability: recordNotRecorded}
	case err != nil:
		loop.Control, failed = runsControl{Availability: recordUnavailable}, true
	default:
		loop.Control, controlKnown = runsControl{Availability: recordAvailable, State: ctl.Summary()}, true
	}
	c.waiting = c.blocker(st, stateKnown, ctl, controlKnown)
	loop.Blocker = c.waiting
	if c.available != rungraph.Available {
		return loop, failed
	}
	g := &c.g
	updated, dropped := g.UpdatedAt, g.Dropped
	loop.Graph.SchemaVersion, loop.Graph.LoopID, loop.Graph.UpdatedAt, loop.Graph.Dropped = g.SchemaVersion, g.LoopID, &updated, &dropped
	if controlKnown {
		loop.Conflict = g.Conflict(rungraph.GateOf(ctl))
	}
	for i := len(g.Runs) - 1; i >= 0; i-- {
		id := g.Runs[i].ID
		if id == g.CurrentRun {
			run := c.projectRun(id)
			loop.Current = &run
			continue
		}
		if len(loop.Recent) >= limitRuns {
			loop.OmittedRuns++
			continue
		}
		loop.Recent = append(loop.Recent, c.projectRun(id))
	}
	return loop, failed
}

// current reports whether a session link still names the chat its target
// alias resolves to now. A headless link is compared with the runner's saved
// resume ID on disk. A configured link is compared with the loaded
// configuration; without one (--state-dir) it counts only when it is the
// alias's single chat in the run, because nothing says which of several is
// in use.
func (c *runsContext) current(run rungraph.Node, e rungraph.Edge) bool {
	_, id, _ := strings.Cut(strings.TrimPrefix(e.To, "session:"), "/")
	if e.Source == rungraph.SourceRunnerFile {
		saved, err := runner.Dir(filepath.Join(c.dir, "targets", e.Target)).InspectSession()
		return err == nil && saved != "" && saved == id
	}
	if c.cfg != nil {
		agent := c.cfg.Loop.Agent
		if e.Target != state.Primary {
			candidate, ok := c.cfg.FallbackSet.Find(e.Target)
			if !ok {
				return false
			}
			agent = candidate.Agent
		}
		return agent.BB != nil && agent.BB.Thread == id
	}
	for _, other := range run.Edges {
		if other.Type == rungraph.EdgeSession && other.Target == e.Target && other.To != e.To {
			return false
		}
	}
	return true
}

func (c *runsContext) projectRun(id string) runsRun {
	g := &c.g
	n, _ := g.Lookup(id)
	r := *n.Run
	out := runsRun{ID: r.ID, State: "open", Origin: r.Origin, RecordedAt: r.RecordedAt, Source: r.Source, Reopens: r.Reopens,
		SessionEvidence: evidenceUnavailable, Sessions: []runsSession{}, GoalRevisions: []runsGoal{}, Unrecorded: []runsUnrecorded{}, Triggers: []runsTrigger{}, Attempts: runsAttempts{Latest: []runsAttempt{}}}
	closed := !r.ClosedAt.IsZero()
	if closed {
		at := r.ClosedAt
		out.State, out.ClosedAt, out.ClosedSource = "completion_declared", &at, r.ClosedSource
	}
	rank := map[string]int{evidenceUnavailable: 0, evidenceHistorical: 1, evidenceConfigured: 2, evidenceObserved: 2}
	var attempts []string
	for _, e := range n.Edges {
		switch e.Type {
		case rungraph.EdgeSession:
			evidence := evidenceHistorical
			if !closed && c.active != "" && e.Target == c.active && c.current(n, e) {
				evidence = evidenceConfigured
				if e.Source == rungraph.SourceRunnerFile {
					evidence = evidenceObserved
				}
			}
			// The strongest label covers every session, shown or not.
			if rank[evidence] > rank[out.SessionEvidence] {
				out.SessionEvidence = evidence
			}
			if len(out.Sessions) >= c.limit {
				out.OmittedSessions++
				continue
			}
			provider, session, _ := strings.Cut(strings.TrimPrefix(e.To, "session:"), "/")
			out.Sessions = append(out.Sessions, runsSession{Ref: e.To, Provider: provider, ID: session, Target: e.Target, Evidence: evidence, Source: e.Source, RecordedAt: e.RecordedAt,
				Link: runsUnavailable{Availability: recordUnavailable, Reason: "no_validated_link_format"}})
		case rungraph.EdgeGoalRevision:
			if len(out.GoalRevisions) >= c.limit {
				out.OmittedGoalRevisions++
				continue
			}
			out.GoalRevisions = append(out.GoalRevisions, runsGoal{Ref: e.To, Source: e.Source, RecordedAt: e.RecordedAt})
		case rungraph.EdgeAttempt:
			attempts = append(attempts, e.To)
		case rungraph.EdgeInvocation:
			// The run's own opening invocation is always its first such edge.
			if r.Routine != nil && out.Invocation == "" {
				out.Invocation = e.To
				continue
			}
			if len(out.Triggers) >= c.limit {
				out.OmittedTriggers++
				continue
			}
			out.Triggers = append(out.Triggers, runsTrigger{Ref: e.To, Source: e.Source, RecordedAt: e.RecordedAt})
		}
	}
	for _, u := range n.Unrecorded {
		out.Unrecorded = append(out.Unrecorded, runsUnrecorded{Target: u.Target, Reason: u.Reason})
	}
	out.Attempts.Total = len(attempts)
	for i := len(attempts) - 1; i >= 0; i-- {
		a, _ := g.Lookup(attempts[i])
		if a.Attempt.Outcome == rd.Intent {
			out.Attempts.Unconfirmed++
		}
		if len(out.Attempts.Latest) < runsLatestSends {
			out.Attempts.Latest = append(out.Attempts.Latest, projectAttempt(g, *a.Attempt))
		}
	}
	return out
}

func projectAttempt(g *rungraph.Graph, a rungraph.Attempt) runsAttempt {
	out := runsAttempt{ID: a.ID, Run: a.Run, Target: a.Target, Action: a.Action, Mode: a.Mode, Outcome: a.Outcome, RecordedAt: a.RecordedAt,
		Session: runsAttemptSession{Availability: recordUnavailable}}
	if !a.ReturnedAt.IsZero() {
		at := a.ReturnedAt
		out.ReturnedAt = &at
	}
	if a.GoalID != "" {
		out.GoalRef = rungraph.GoalRevisionRef(a.GoalID, a.GoalRevision)
	}
	run, _ := g.Lookup(a.Run)
	for _, e := range run.Edges {
		if e.Type != rungraph.EdgeSession || e.Target != a.Target {
			continue
		}
		if out.Session.Ref != "" && out.Session.Ref != e.To {
			// The alias named more than one chat during this run, so the
			// recorded fields cannot say which one this send went to.
			out.Session = runsAttemptSession{Availability: "ambiguous"}
			break
		}
		out.Session = runsAttemptSession{Availability: recordRecorded, Ref: e.To}
	}
	return out
}

// readNode resolves one exact reference. Nothing is searched or inferred. A
// graph that exists but cannot be read answers unavailable, because the
// reference may be in it; only a readable or absent graph can answer
// not_recorded.
func (c *runsContext) readNode(ref string) *runsNode {
	out := &runsNode{Ref: ref, Edges: []runsEdge{}}
	if c.available == rungraph.Unsupported || c.available == rungraph.Corrupt {
		out.Status = recordUnavailable
		return out
	}
	g := &c.g
	if g.LoopID == "" {
		out.Status = rungraph.NotRecorded
		return out
	}
	n, status := g.Lookup(ref)
	out.Status = status
	if status != rungraph.Found {
		return out
	}
	out.Type, out.Source = n.Type, n.Source
	if !n.RecordedAt.IsZero() {
		at := n.RecordedAt
		out.RecordedAt = &at
	}
	// Child edges follow the loop view's answer. A listing that is not
	// current there is not followable here, and a child that is listed but
	// does not hold the run is not presented as blocking it.
	show, holds := c.waiting.childEdges()
	if n.Type == rungraph.ChildNode && !show {
		return &runsNode{Ref: ref, Status: recordUnavailable, Reason: c.waiting.Reason, Edges: []runsEdge{}}
	}
	for _, e := range n.Edges {
		if e.Type == rungraph.EdgeBlockedBy || e.Type == rungraph.EdgeBlocks || e.Type == rungraph.EdgeChild {
			if !show {
				continue
			}
			if !holds {
				e.Type = rungraph.EdgeChild
			}
		}
		if len(out.Edges) >= c.limit {
			out.OmittedEdges++
			continue
		}
		_, found := g.Lookup(e.To)
		out.Edges = append(out.Edges, runsEdge{Type: e.Type, To: e.To, Target: e.Target, Source: e.Source, RecordedAt: e.RecordedAt, Resolves: found == rungraph.Found})
	}
	switch n.Type {
	case rungraph.RunNode:
		run := c.projectRun(n.Ref)
		out.Run = &run
	case rungraph.AttemptNode:
		a := projectAttempt(g, *n.Attempt)
		out.Attempt = &a
	case rungraph.SessionNode:
		// Back from a chat to the sends recorded for it. A send belongs here
		// when this is the only chat its run recorded for its target alias.
		// Sends for an alias that named several chats are counted, not guessed.
		linked := map[[2]string]bool{}
		for _, e := range n.Edges {
			if e.Type == rungraph.EdgeRun {
				linked[[2]string{e.To, e.Target}] = true
			}
		}
		for i := len(g.Attempts) - 1; i >= 0; i-- {
			a := projectAttempt(g, g.Attempts[i])
			switch {
			case a.Session.Ref == ref:
				if len(out.Attempts) >= c.limit {
					out.OmittedAttempts++
					continue
				}
				out.Attempts = append(out.Attempts, a)
			case a.Session.Availability == "ambiguous" && linked[[2]string{a.Run, a.Target}]:
				out.AmbiguousAttempts++
			}
		}
	}
	return out
}

// runsSafe keeps recorded strings from forging terminal output. Recorded
// references are already printable; target aliases come from configuration
// and a hand-edited record could hold anything.
func runsSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x21 || r > 0x7e {
			return '?'
		}
		return r
	}, s)
}

func printRuns(w io.Writer, r runsReport) {
	fmt.Fprintf(w, "Recorded run references (read %s). Nothing here is a live observation.\n", r.ReadAt.Format(time.RFC3339))
	for _, l := range r.Loops {
		fmt.Fprintln(w)
		if l.RegistrationID != "" {
			fmt.Fprintf(w, "registration: %s (%s) loop: %s\n", l.RegistrationID, l.Registration, runsSafe(l.Loop))
			if l.Binding != recordAvailable {
				fmt.Fprintln(w, "binding: unavailable; the registered configuration is missing or now names another loop or runtime directory, so nothing was read")
				continue
			}
		} else if l.Loop != "" {
			fmt.Fprintf(w, "loop: %s\n", runsSafe(l.Loop))
		}
		fmt.Fprintf(w, "graph: %s", l.Graph.Availability)
		if l.Graph.LoopID != "" {
			fmt.Fprintf(w, "  durable loop: %s  updated: %s", l.Graph.LoopID, l.Graph.UpdatedAt.Format(time.RFC3339))
		}
		fmt.Fprintf(w, "\ncontrol: %s", l.Control.Availability)
		if l.Control.State != "" {
			fmt.Fprintf(w, " (%s)", l.Control.State)
		}
		fmt.Fprintf(w, "  saved active target: %s", l.ActiveTarget.Availability)
		if l.ActiveTarget.Alias != "" {
			fmt.Fprintf(w, " (%s)", runsSafe(l.ActiveTarget.Alias))
		}
		fmt.Fprintln(w)
		if l.Conflict != "" {
			fmt.Fprintf(w, "conflict: %s; the next check records the missing boundary\n", l.Conflict)
		}
		if l.Current == nil {
			fmt.Fprintln(w, "current run: not recorded")
		} else {
			printRun(w, "current run", *l.Current)
		}
		if r.View != "registry" {
			for _, run := range l.Recent {
				printRun(w, "earlier run", run)
			}
			if l.OmittedRuns > 0 {
				fmt.Fprintf(w, "%d earlier runs not shown; raise --limit\n", l.OmittedRuns)
			}
			if d := l.Graph.Dropped; d != nil && *d != (rungraph.Dropped{}) {
				fmt.Fprintf(w, "retention dropped: %d runs, %d attempts, %d links, %d triggers, %d registry references\n", d.Runs, d.Attempts, d.Links, d.Triggers, d.Registrations)
			}
		}
		printBlocker(w, l.Blocker)
	}
	if r.Omitted > 0 {
		fmt.Fprintf(w, "\n%d registered loops not shown; raise --limit\n", r.Omitted)
	}
	if n := r.Node; n != nil {
		fmt.Fprintf(w, "\nreference %s: %s", runsSafe(n.Ref), n.Status)
		if n.Status != rungraph.Found {
			if n.Status == recordUnavailable && n.Reason != "" {
				fmt.Fprintf(w, " (%s; a child listing that is not current is not followed)", n.Reason)
			} else if n.Status == recordUnavailable {
				fmt.Fprint(w, " (the graph or binding could not be read, so the reference may exist)")
			}
			fmt.Fprintln(w)
			return
		}
		fmt.Fprintf(w, " (%s", n.Type)
		if n.RecordedAt != nil {
			fmt.Fprintf(w, ", recorded %s from %s", n.RecordedAt.Format(time.RFC3339), n.Source)
		}
		fmt.Fprintln(w, ")")
		if n.Attempt != nil {
			printAttempt(w, *n.Attempt)
		}
		for _, e := range n.Edges {
			fmt.Fprintf(w, "  %s -> %s", e.Type, runsSafe(e.To))
			if e.Target != "" {
				fmt.Fprintf(w, " [target %s]", runsSafe(e.Target))
			}
			if !e.Resolves {
				fmt.Fprint(w, " (not retained)")
			}
			fmt.Fprintf(w, " (%s, %s)\n", e.Source, e.RecordedAt.Format(time.RFC3339))
		}
		if n.OmittedEdges > 0 {
			fmt.Fprintf(w, "  %d more references not shown; raise --limit\n", n.OmittedEdges)
		}
		for _, a := range n.Attempts {
			printAttempt(w, a)
		}
		if n.OmittedAttempts > 0 {
			fmt.Fprintf(w, "  %d more sends not shown; raise --limit\n", n.OmittedAttempts)
		}
		if n.AmbiguousAttempts > 0 {
			fmt.Fprintf(w, "  %d sends not attributed: their target named more than one chat in the run\n", n.AmbiguousAttempts)
		}
	}
}

func printRun(w io.Writer, label string, run runsRun) {
	fmt.Fprintf(w, "%s: %s  %s  origin %s  recorded %s", label, run.ID, run.State, run.Origin, run.RecordedAt.Format(time.RFC3339))
	if run.ClosedAt != nil {
		fmt.Fprintf(w, "  closed %s", run.ClosedAt.Format(time.RFC3339))
	}
	fmt.Fprintln(w)
	if run.Invocation != "" {
		fmt.Fprintf(w, "  opened by invocation: %s\n", runsSafe(run.Invocation))
	}
	for _, tr := range run.Triggers {
		fmt.Fprintf(w, "  trigger while open: %s\n", runsSafe(tr.Ref))
	}
	if run.OmittedTriggers > 0 {
		fmt.Fprintf(w, "  %d more triggers not shown; raise --limit\n", run.OmittedTriggers)
	}
	if len(run.Sessions) == 0 {
		fmt.Fprintln(w, "  chat/session: unavailable (none recorded)")
	}
	for _, s := range run.Sessions {
		fmt.Fprintf(w, "  chat/session: %s  target %s  %s (%s)\n", runsSafe(s.Ref), runsSafe(s.Target), s.Evidence, s.Source)
	}
	if run.OmittedSessions > 0 {
		fmt.Fprintf(w, "  %d more chat/session references not shown; raise --limit\n", run.OmittedSessions)
	}
	for _, u := range run.Unrecorded {
		fmt.Fprintf(w, "  chat/session for target %s: unavailable (%s)\n", runsSafe(u.Target), runsSafe(u.Reason))
	}
	for _, g := range run.GoalRevisions {
		fmt.Fprintf(w, "  goal revision: %s\n", runsSafe(g.Ref))
	}
	if run.OmittedGoalRevisions > 0 {
		fmt.Fprintf(w, "  %d more goal revisions not shown; raise --limit\n", run.OmittedGoalRevisions)
	}
	fmt.Fprintf(w, "  sends: %d recorded, %d unconfirmed\n", run.Attempts.Total, run.Attempts.Unconfirmed)
	for _, a := range run.Attempts.Latest {
		printAttempt(w, a)
	}
}

func printAttempt(w io.Writer, a runsAttempt) {
	chat := "chat/session " + a.Session.Availability
	switch a.Session.Availability {
	case recordRecorded:
		chat = "chat/session " + runsSafe(a.Session.Ref)
	case "ambiguous":
		chat = "chat/session ambiguous (its target named more than one chat in the run)"
	}
	fmt.Fprintf(w, "    %s  %s to %s  %s  %s  run %s  %s\n", a.ID, runsSafe(a.Action), runsSafe(a.Target), a.Outcome, a.RecordedAt.Format(time.RFC3339), a.Run, chat)
}

func printSupervised(w io.Writer, s runsSupervised) {
	switch s.Availability {
	case recordRecorded:
		fmt.Fprintf(w, "      a registered loop: %s (%s)", s.RegistrationID, runsSafe(s.Loop))
		if s.Run != "" {
			fmt.Fprintf(w, "  run %s", s.Run)
		} else {
			fmt.Fprintf(w, "  run unavailable (%s)", s.RunReason)
		}
		fmt.Fprintf(w, "\n      follow it with: keepgoing runs --registry-id %s\n", s.RegistrationID)
	case "registry_not_consulted":
		fmt.Fprintln(w, "      its loop: unavailable (no registry consulted; pass --directory or a registry selector)")
	default:
		fmt.Fprintf(w, "      its loop: unavailable (%s)\n", s.Availability)
	}
}

func printBlocker(w io.Writer, b runsBlocker) {
	fmt.Fprintf(w, "waiting on: %s", b.Availability)
	if b.Reason != "" {
		fmt.Fprintf(w, " (%s)", b.Reason)
	}
	if b.CheckedAt != nil {
		fmt.Fprintf(w, "  child listing from the check at %s, %s ago", b.CheckedAt.Format(time.RFC3339), (time.Duration(*b.AgeSeconds) * time.Second).String())
		if b.Stale {
			fmt.Fprint(w, " (stale)")
		}
	}
	if b.LastCheckCause != "" {
		fmt.Fprintf(w, "  last check recorded: %s", runsSafe(b.LastCheckCause))
	}
	fmt.Fprintln(w)
	for _, child := range b.Children {
		holds := "held the run at that check"
		if !child.Holds {
			holds = "does not hold the run"
		}
		fmt.Fprintf(w, "  child %s  %s  %s\n", child.Ref, child.Status, holds)
		printSupervised(w, child.Supervised)
	}
	if b.OmittedChildren > 0 {
		fmt.Fprintf(w, "  %d more children not shown; raise --limit\n", b.OmittedChildren)
	}
	switch o := b.Owner; o.Availability {
	case recordRecorded:
		fmt.Fprintf(w, "owner: %s (generation %d)", runsSafe(o.Ref), o.Generation)
		if o.BlocksCaller {
			fmt.Fprint(w, "  this conversation is not the owner: its control commands are refused")
		}
		fmt.Fprintln(w)
		printSupervised(w, o.Supervised)
	case recordNotRecorded:
		fmt.Fprintln(w, "owner: not recorded")
	default:
		fmt.Fprintf(w, "owner: unknown (%s)\n", o.Reason)
	}
	fmt.Fprintf(w, "lock holder: %s (%s)\n", b.Lock.Availability, b.Lock.Reason)
}
