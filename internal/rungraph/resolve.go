package rungraph

import (
	"strconv"
	"time"

	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
)

// Node types.
const (
	LoopNode         = "loop"
	RegistrationNode = "registration"
	RunNode          = "run"
	AttemptNode      = "attempt"
	SessionNode      = "session"
	GoalRevisionNode = "goal_revision"
	InvocationNode   = "invocation"
	RoutineNode      = "routine"
)

// Edge types. Every edge is a recorded relationship, not a live observation.
const (
	EdgeCurrentRun   = "current_run"
	EdgeRun          = "run"
	EdgeLoop         = "loop"
	EdgeRegistration = "registration"
	EdgeReopens      = "reopens"
	EdgeAttempt      = "attempt"
	EdgeSession      = "session"
	EdgeGoalRevision = "goal_revision"
	EdgeInvocation   = "invocation"
	EdgeRoutine      = "routine"
)

// Status of a lookup.
type Status string

const (
	Found       Status = "found"
	NotRecorded Status = "not_recorded"
	// NotRecordedOrExpired: retention has dropped records, so the reference
	// may have existed. Nothing is recreated to answer.
	NotRecordedOrExpired Status = "not_recorded_or_expired"
)

// Edge points at another node by its stable reference. To may name a node
// that is no longer retained, such as a dropped reopen predecessor.
type Edge struct {
	Type       string
	To         string
	Target     string
	Source     Source
	RecordedAt time.Time
}

// Node is one identity with its recorded adjacent references. RecordedAt and
// Source say when and from what the node was first recorded; they are the only
// freshness the graph has. Exactly one of the record pointers is set for runs,
// attempts and registrations.
type Node struct {
	Ref          string
	Type         string
	RecordedAt   time.Time
	Source       Source
	Run          *Run
	Attempt      *Attempt
	Registration *Registration
	Edges        []Edge
	// Unrecorded lists evidence that existed for a run but could not be stored.
	Unrecorded []Unrecorded
}

// SessionRef is the stable reference of a provider chat/session node. The
// parts are joined with "/", which no recorded token can contain, so two
// different pairs never share a reference.
func SessionRef(provider, id string) string { return "session:" + provider + "/" + id }

// GoalRevisionRef is the stable reference of a goal revision node.
func GoalRevisionRef(id string, revision uint64) string {
	return id + "@" + strconv.FormatUint(revision, 10)
}

// RoutineNodeRef is the stable reference of an external routine: every
// invocation of it, and every run those invocations opened or triggered.
func RoutineNodeRef(r rd.RoutineRef) string { return "routine:" + r.Source + "/" + r.Routine }

// InvocationRef is the stable reference of a routine invocation node. It
// always has three parts, so it never equals a routine reference; an
// invocation with no ID ends in "/".
func InvocationRef(r rd.RoutineRef) string {
	return "routine:" + r.Source + "/" + r.Routine + "/" + r.Invocation
}

func (g *Graph) build() {
	g.index = map[string]*Node{}
	node := func(ref, typ string, at time.Time, source Source) *Node {
		if n := g.index[ref]; n != nil {
			return n
		}
		n := &Node{Ref: ref, Type: typ, RecordedAt: at, Source: source}
		g.index[ref] = n
		return n
	}
	edge := func(n *Node, typ, to, target string, source Source, at time.Time) {
		n.Edges = append(n.Edges, Edge{Type: typ, To: to, Target: target, Source: source, RecordedAt: at})
	}
	// invocation records one trigger of a routine for a run, and keeps the
	// routine node's edges to each invocation and run listed once.
	invocation := func(run *Node, r rd.RoutineRef, source Source, at time.Time) {
		ref, routine := InvocationRef(r), RoutineNodeRef(r)
		edge(run, EdgeInvocation, ref, "", source, at)
		in := node(ref, InvocationNode, at, source)
		edge(in, EdgeRun, run.Ref, "", source, at)
		rn := node(routine, RoutineNode, at, source)
		seenInvocation, seenRun := false, false
		for _, e := range rn.Edges {
			seenInvocation = seenInvocation || (e.Type == EdgeInvocation && e.To == ref)
			seenRun = seenRun || (e.Type == EdgeRun && e.To == run.Ref)
		}
		if !seenInvocation {
			edge(in, EdgeRoutine, routine, "", source, at)
			edge(rn, EdgeInvocation, ref, "", source, at)
		}
		if !seenRun {
			edge(rn, EdgeRun, run.Ref, "", source, at)
		}
	}
	if g.LoopID == "" {
		return
	}
	loop := node(g.LoopID, LoopNode, time.Time{}, "")
	for i := range g.Registrations {
		r := &g.Registrations[i]
		n := node(r.ID, RegistrationNode, r.RecordedAt, r.Source)
		n.Registration = r
		edge(n, EdgeLoop, g.LoopID, "", r.Source, r.RecordedAt)
		edge(loop, EdgeRegistration, r.ID, "", r.Source, r.RecordedAt)
	}
	for i := range g.Runs {
		r := &g.Runs[i]
		n := node(r.ID, RunNode, r.RecordedAt, r.Source)
		n.Run = r
		edge(n, EdgeLoop, g.LoopID, "", r.Source, r.RecordedAt)
		typ := EdgeRun
		if r.ID == g.CurrentRun {
			typ = EdgeCurrentRun
		}
		edge(loop, typ, r.ID, "", r.Source, r.RecordedAt)
		if loop.RecordedAt.IsZero() {
			loop.RecordedAt, loop.Source = r.RecordedAt, r.Source
		}
		if r.Reopens != "" {
			edge(n, EdgeReopens, r.Reopens, "", r.Source, r.RecordedAt)
		}
		if r.Routine != nil {
			invocation(n, routineRef(r.Routine), r.Source, r.RecordedAt)
		}
	}
	for i := range g.Attempts {
		a := &g.Attempts[i]
		n := node(a.ID, AttemptNode, a.RecordedAt, a.Source)
		n.Attempt = a
		edge(n, EdgeRun, a.Run, a.Target, a.Source, a.RecordedAt)
		edge(g.index[a.Run], EdgeAttempt, a.ID, a.Target, a.Source, a.RecordedAt)
		if a.GoalID != "" {
			edge(n, EdgeGoalRevision, GoalRevisionRef(a.GoalID, a.GoalRevision), "", a.Source, a.RecordedAt)
		}
	}
	for _, l := range g.Links {
		if l.Kind == rd.InvocationLink {
			invocation(g.index[l.Run], routineRef(l.Routine), l.Source, l.RecordedAt)
			continue
		}
		var ref, typ, edgeType string
		switch l.Kind {
		case rd.SessionLink:
			ref, typ, edgeType = SessionRef(l.Provider, l.Session), SessionNode, EdgeSession
		case rd.GoalLink:
			ref, typ, edgeType = GoalRevisionRef(l.GoalID, l.GoalRevision), GoalRevisionNode, EdgeGoalRevision
		}
		edge(g.index[l.Run], edgeType, ref, l.Target, l.Source, l.RecordedAt)
		edge(node(ref, typ, l.RecordedAt, l.Source), EdgeRun, l.Run, l.Target, l.Source, l.RecordedAt)
	}
	for _, u := range g.Unrecorded {
		g.index[u.Run].Unrecorded = append(g.index[u.Run].Unrecorded, u)
	}
}

// Lookup returns a node and its adjacent references by exact reference, from
// an index built once per loaded graph. It reads nothing else: no history
// scan, provider query, filesystem search or timestamp comparison.
func (g *Graph) Lookup(ref string) (Node, Status) {
	if g.index == nil {
		g.build()
	}
	if n := g.index[ref]; n != nil {
		return *n, Found
	}
	if g.Dropped != (Dropped{}) {
		return Node{}, NotRecordedOrExpired
	}
	return Node{}, NotRecorded
}

// Current resolves loop → current run. The run may be closed: check
// Run.ClosedAt. An open run is not a claim that anything is active.
func (g *Graph) Current() (Node, Status) {
	if g.CurrentRun == "" {
		return Node{}, NotRecorded
	}
	return g.Lookup(g.CurrentRun)
}

// Follow returns the nodes one edge type leads to from a node, in recorded
// order. An edge to a node that is not retained is skipped.
func (g *Graph) Follow(n Node, edgeType string) []Node {
	var out []Node
	for _, e := range n.Edges {
		if e.Type != edgeType {
			continue
		}
		if next, status := g.Lookup(e.To); status == Found {
			out = append(out, next)
		}
	}
	return out
}

// Conflict codes.
const (
	ConflictCompletionNotRecorded = "completion_not_recorded"
	ConflictReopenNotRecorded     = "reopen_not_recorded"
)

// Conflict compares the recorded run with saved control state without
// changing either. A non-empty code means a boundary was not recorded yet, so
// the current run must not be presented as settled.
func (g *Graph) Conflict(gate Gate) string {
	switch {
	case g.LoopID == "":
		return ""
	case gate.Completed && !g.Completed:
		return ConflictCompletionNotRecorded
	case !gate.Completed && g.Completed:
		return ConflictReopenNotRecorded
	}
	return ""
}
