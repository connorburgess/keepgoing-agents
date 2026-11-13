package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

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
	runsSchemaVersion = 1
	runsDefaultLimit  = 10
	runsMaxLimit      = 256
	runsLatestSends   = 3
)

// Evidence labels for a chat/session reference.
const (
	evidenceConfigured  = "configured"  // the target's configured chat ID, for the open run's saved active target
	evidenceObserved    = "observed"    // a saved runner resume ID read from disk, for the open run's saved active target
	evidenceHistorical  = "historical"  // recorded for a closed run, or for a target that is not the saved active target
	evidenceUnavailable = "unavailable" // nothing recorded
)

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
	Registration string          `json:"registration,omitempty"`
	Loop         string          `json:"loop,omitempty"`
	Graph        runsGraph       `json:"graph"`
	Control      runsControl     `json:"control"`
	Conflict     string          `json:"conflict,omitempty"`
	ActiveTarget runsTarget      `json:"activeTarget"`
	Current      *runsRun        `json:"current,omitempty"`
	Recent       []runsRun       `json:"recent"`
	OmittedRuns  int             `json:"omittedRuns"`
	Blocker      runsUnavailable `json:"blocker"`
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
	Invocation   string          `json:"invocation,omitempty"`
	// SessionEvidence is unavailable when Sessions is empty, otherwise the
	// strongest label among them.
	SessionEvidence string           `json:"sessionEvidence"`
	Sessions        []runsSession    `json:"sessions"`
	GoalRevisions   []runsGoal       `json:"goalRevisions"`
	Attempts        runsAttempts     `json:"attempts"`
	Unrecorded      []runsUnrecorded `json:"unrecorded"`
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
	Ref          string          `json:"ref"`
	Status       rungraph.Status `json:"status"`
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
	"Blocker navigation is unavailable until a validated blocked-by relationship is recorded.",
	"The saved active target is what the last check recorded. Target moves are in keepgoing timeline, not here.",
}

func runsCommand(e env, args []string) int {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Never echo parser or config values.
	path := fs.String("c", "keepgoing.yaml", "configuration used only to locate state_dir")
	dir := fs.String("state-dir", "", "read this directory without loading configuration")
	registration := fs.String("registry-id", "", "select a registered loop by its loop_ reference")
	all := fs.Bool("all", false, "list every registered loop with its current run")
	directory := fs.String("directory", "", "private registry directory, with --registry-id or --all")
	ref := fs.String("id", "", "show one recorded reference and what it links to")
	limit := fs.Int("limit", runsDefaultLimit, "recent runs, edges or registered loops to print (1-256)")
	asJSON := fs.Bool("json", false, "print the versioned report")
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
	if fs.NArg() != 0 || selectors > 1 || *limit < 1 || *limit > runsMaxLimit || (set["state-dir"] && *dir == "") ||
		(set["registry-id"] && !clientRegistrationID.MatchString(*registration)) || (set["directory"] && !registered) ||
		(*all && set["id"]) || (set["id"] && (*ref == "" || len(*ref) > 512)) {
		e.errf("runs: select one of -c, --state-dir, --registry-id or --all; --directory needs a registry selector; --id needs one loop; limit 1-256; no positional arguments")
		return exitUsage
	}
	r := runsReport{SchemaVersion: runsSchemaVersion, ReadAt: time.Now().UTC(), View: "loop", Loops: []runsLoop{}, Limitations: runsLimitations}
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
			loop, g, bad := readRunsLoop(entry.StateDir, limitRuns)
			loop.RegistrationID, loop.Loop = entry.ID, entry.Loop
			loop.Registration = "unavailable"
			if loop.Graph.Availability == rungraph.Available || loop.Graph.Availability == rungraph.Missing {
				loop.Registration = "not_recorded"
				if n, status := g.Lookup(entry.ID); status == rungraph.Found && n.Type == rungraph.RegistrationNode {
					loop.Registration = "recorded"
				}
			}
			failed = failed || bad
			r.Loops = append(r.Loops, loop)
			if set["id"] {
				r.View, r.Node = "node", readRunsNode(&g, *ref, *limit, loop.ActiveTarget.Alias)
			}
		}
	default:
		name := ""
		if *dir == "" {
			cfg, err := config.Load(*path)
			if err != nil {
				e.errf("runs: configuration unavailable; repair it or select --state-dir to read private records directly")
				return exitUsage
			}
			*dir, name = cfg.Loop.StateDir, cfg.Loop.Name
		}
		loop, g, bad := readRunsLoop(*dir, *limit)
		loop.Loop, failed = name, bad
		r.Loops = append(r.Loops, loop)
		if set["id"] {
			r.View, r.Node = "node", readRunsNode(&g, *ref, *limit, loop.ActiveTarget.Alias)
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

// readRunsLoop reads one state directory. Each file is an independent
// snapshot; none is locked and none is created.
func readRunsLoop(dir string, limit int) (runsLoop, rungraph.Graph, bool) {
	loop := runsLoop{Recent: []runsRun{}, Blocker: runsUnavailable{Availability: "unavailable", Reason: "no_validated_blocker_relationship"}}
	g, availability := rungraph.Load(dir)
	failed := availability == rungraph.Unsupported || availability == rungraph.Corrupt
	loop.Graph = runsGraph{Availability: availability}
	loop.ActiveTarget = runsTarget{Availability: "unavailable"}
	active := ""
	if st, err := state.Load(dir); err == nil {
		active = st.Target()
		loop.ActiveTarget = runsTarget{Availability: "recorded", Alias: active}
	} else {
		failed = true
	}
	loop.Control = runsControl{Availability: "unavailable"}
	ctl, err := control.Load(dir)
	if err == nil {
		loop.Control = runsControl{Availability: "available", State: ctl.Summary()}
	} else {
		failed = true
	}
	if availability != rungraph.Available {
		return loop, g, failed
	}
	updated, dropped := g.UpdatedAt, g.Dropped
	loop.Graph.SchemaVersion, loop.Graph.LoopID, loop.Graph.UpdatedAt, loop.Graph.Dropped = g.SchemaVersion, g.LoopID, &updated, &dropped
	if err == nil {
		loop.Conflict = g.Conflict(rungraph.GateOf(ctl))
	}
	for i := len(g.Runs) - 1; i >= 0; i-- {
		id := g.Runs[i].ID
		if id == g.CurrentRun {
			run := projectRun(&g, id, active)
			loop.Current = &run
			continue
		}
		if len(loop.Recent) >= limit {
			loop.OmittedRuns++
			continue
		}
		loop.Recent = append(loop.Recent, projectRun(&g, id, active))
	}
	return loop, g, failed
}

func projectRun(g *rungraph.Graph, id, active string) runsRun {
	n, _ := g.Lookup(id)
	r := *n.Run
	out := runsRun{ID: r.ID, State: "open", Origin: r.Origin, RecordedAt: r.RecordedAt, Source: r.Source, Reopens: r.Reopens,
		SessionEvidence: evidenceUnavailable, Sessions: []runsSession{}, GoalRevisions: []runsGoal{}, Unrecorded: []runsUnrecorded{}, Attempts: runsAttempts{Latest: []runsAttempt{}}}
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
			if !closed && active != "" && e.Target == active {
				evidence = evidenceConfigured
				if e.Source == rungraph.SourceRunnerFile {
					evidence = evidenceObserved
				}
			}
			provider, session, _ := strings.Cut(strings.TrimPrefix(e.To, "session:"), "/")
			out.Sessions = append(out.Sessions, runsSession{Ref: e.To, Provider: provider, ID: session, Target: e.Target, Evidence: evidence, Source: e.Source, RecordedAt: e.RecordedAt,
				Link: runsUnavailable{Availability: "unavailable", Reason: "no_validated_link_format"}})
			if rank[evidence] > rank[out.SessionEvidence] {
				out.SessionEvidence = evidence
			}
		case rungraph.EdgeGoalRevision:
			out.GoalRevisions = append(out.GoalRevisions, runsGoal{Ref: e.To, Source: e.Source, RecordedAt: e.RecordedAt})
		case rungraph.EdgeAttempt:
			attempts = append(attempts, e.To)
		case rungraph.EdgeInvocation:
			if r.Routine != nil && out.Invocation == "" {
				out.Invocation = e.To
			}
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
		Session: runsAttemptSession{Availability: "unavailable"}}
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
		out.Session = runsAttemptSession{Availability: "recorded", Ref: e.To}
	}
	return out
}

// readRunsNode resolves one exact reference. Nothing is searched or inferred:
// an unknown reference reports the graph's own not-recorded status.
func readRunsNode(g *rungraph.Graph, ref string, limit int, active string) *runsNode {
	out := &runsNode{Ref: ref, Edges: []runsEdge{}}
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
	for _, e := range n.Edges {
		if len(out.Edges) >= limit {
			out.OmittedEdges++
			continue
		}
		_, found := g.Lookup(e.To)
		out.Edges = append(out.Edges, runsEdge{Type: e.Type, To: e.To, Target: e.Target, Source: e.Source, RecordedAt: e.RecordedAt, Resolves: found == rungraph.Found})
	}
	switch n.Type {
	case rungraph.RunNode:
		run := projectRun(g, n.Ref, active)
		out.Run = &run
	case rungraph.AttemptNode:
		a := projectAttempt(g, *n.Attempt)
		out.Attempt = &a
	case rungraph.SessionNode:
		// Back from a chat to the sends recorded for it: every attempt in a
		// run that links this chat, for the alias that link was recorded for.
		for i := len(g.Attempts) - 1; i >= 0; i-- {
			a := projectAttempt(g, g.Attempts[i])
			if a.Session.Ref != ref {
				continue
			}
			if len(out.Attempts) >= limit {
				out.OmittedAttempts++
				continue
			}
			out.Attempts = append(out.Attempts, a)
		}
	}
	return out
}

func printRuns(w io.Writer, r runsReport) {
	fmt.Fprintf(w, "Recorded run references (read %s). Nothing here is a live observation.\n", r.ReadAt.Format(time.RFC3339))
	for _, l := range r.Loops {
		fmt.Fprintln(w)
		if l.RegistrationID != "" {
			fmt.Fprintf(w, "registration: %s (%s) loop: %s\n", l.RegistrationID, l.Registration, l.Loop)
		} else if l.Loop != "" {
			fmt.Fprintf(w, "loop: %s\n", l.Loop)
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
			fmt.Fprintf(w, " (%s)", l.ActiveTarget.Alias)
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
				fmt.Fprintf(w, "retention dropped: %d runs, %d attempts, %d links, %d registry references\n", d.Runs, d.Attempts, d.Links, d.Registrations)
			}
		}
		fmt.Fprintf(w, "blocker: %s (%s)\n", l.Blocker.Availability, l.Blocker.Reason)
	}
	if r.Omitted > 0 {
		fmt.Fprintf(w, "\n%d registered loops not shown; raise --limit\n", r.Omitted)
	}
	if n := r.Node; n != nil {
		fmt.Fprintf(w, "\nreference %s: %s", n.Ref, n.Status)
		if n.Status != rungraph.Found {
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
			fmt.Fprintf(w, "  %s -> %s", e.Type, e.To)
			if e.Target != "" {
				fmt.Fprintf(w, " [target %s]", e.Target)
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
	}
}

func printRun(w io.Writer, label string, run runsRun) {
	fmt.Fprintf(w, "%s: %s  %s  origin %s  recorded %s", label, run.ID, run.State, run.Origin, run.RecordedAt.Format(time.RFC3339))
	if run.ClosedAt != nil {
		fmt.Fprintf(w, "  closed %s", run.ClosedAt.Format(time.RFC3339))
	}
	fmt.Fprintln(w)
	if len(run.Sessions) == 0 {
		fmt.Fprintln(w, "  chat/session: unavailable (none recorded)")
	}
	for _, s := range run.Sessions {
		fmt.Fprintf(w, "  chat/session: %s  target %s  %s (%s)\n", s.Ref, s.Target, s.Evidence, s.Source)
	}
	for _, u := range run.Unrecorded {
		fmt.Fprintf(w, "  chat/session for target %s: unavailable (%s)\n", u.Target, u.Reason)
	}
	for _, g := range run.GoalRevisions {
		fmt.Fprintf(w, "  goal revision: %s\n", g.Ref)
	}
	fmt.Fprintf(w, "  sends: %d recorded, %d unconfirmed\n", run.Attempts.Total, run.Attempts.Unconfirmed)
	for _, a := range run.Attempts.Latest {
		printAttempt(w, a)
	}
}

func printAttempt(w io.Writer, a runsAttempt) {
	fmt.Fprintf(w, "    %s  %s to %s  %s  %s\n", a.ID, a.Action, a.Target, a.Outcome, a.RecordedAt.Format(time.RFC3339))
}
