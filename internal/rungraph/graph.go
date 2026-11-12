// Package rungraph persists a loop's run references in its state directory.
// It stores what internal/rundiscovery defines: the durable loop identity,
// runs, send attempts and the recorded links from a run to provider
// chats/sessions, goal revisions and routine invocations. It is diagnostic
// history. It never authorizes a send, changes a control or decides anything,
// and every caller must treat a failed write as a missing record only.
package rungraph

import (
	"time"

	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
)

// FileName of the graph record inside the state directory.
const FileName = "graph.json"

// SchemaVersion written by this build. Older builds ignore the file.
const SchemaVersion = 1

// Bounds. The oldest closed runs go first, with their attempts and links.
const (
	MaxBytes         = 256 * 1024
	MaxRuns          = 32
	MaxAttempts      = 128
	MaxLinks         = 256
	MaxRegistrations = 16
	maxTarget        = 128
)

// Source says which structured input produced a record. None is a transcript.
type Source string

const (
	SourceControl    Source = "control_command"     // an accepted control operation
	SourceReconciled Source = "control_state"       // saved control state seen by a later check
	SourceAdapter    Source = "adapter_call"        // a send intent and its returned call
	SourceConfigured Source = "configured_target"   // the target's configured chat ID
	SourceRunnerFile Source = "runner_session_file" // a headless runner's saved resume ID
	SourceGoal       Source = "goal_record"         // the loop's goal.json
	SourceRegistered Source = "registered_control"  // a control operation made through a registry reference
	SourceExternal   Source = "external"            // supplied by a structured integration
)

func (s Source) valid() bool {
	switch s {
	case SourceControl, SourceReconciled, SourceAdapter, SourceConfigured, SourceRunnerFile, SourceGoal, SourceRegistered, SourceExternal:
		return true
	}
	return false
}

// Graph is the whole record. Slices are ordered oldest first.
type Graph struct {
	SchemaVersion int            `json:"schemaVersion"`
	LoopID        string         `json:"loopId"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	Completed     bool           `json:"completed,omitempty"`
	CurrentRun    string         `json:"currentRun,omitempty"`
	Registrations []Registration `json:"registrations"`
	Runs          []Run          `json:"runs"`
	Attempts      []Attempt      `json:"attempts"`
	Links         []Link         `json:"links"`
	Dropped       Dropped        `json:"dropped"`

	index map[string]*Node
}

// Dropped counts records removed by the retention bounds.
type Dropped struct {
	Runs     uint64 `json:"runs"`
	Attempts uint64 `json:"attempts"`
	Links    uint64 `json:"links"`
}

// Registration records a registry loop_ reference that was used to control
// this durable loop.
type Registration struct {
	ID         string    `json:"id"`
	RecordedAt time.Time `json:"recordedAt"`
	Source     Source    `json:"source"`
}

type Routine struct {
	Source     string `json:"source"`
	Routine    string `json:"routine"`
	Invocation string `json:"invocation,omitempty"`
}

type Run struct {
	ID           string    `json:"id"`
	Origin       rd.Origin `json:"origin"`
	RecordedAt   time.Time `json:"recordedAt"`
	Source       Source    `json:"source"`
	ClosedAt     time.Time `json:"closedAt,omitzero"`
	ClosedSource Source    `json:"closedSource,omitempty"`
	Reopens      string    `json:"reopens,omitempty"`
	Routine      *Routine  `json:"routine,omitempty"`
}

// Attempt with outcome "intent" was recorded before a send whose return was
// never recorded. It is unconfirmed, not evidence that anything was sent.
type Attempt struct {
	ID           string     `json:"id"`
	Run          string     `json:"run"`
	Target       string     `json:"target"`
	Action       string     `json:"action"`
	Mode         string     `json:"mode"`
	GoalID       string     `json:"goalId,omitempty"`
	GoalRevision uint64     `json:"goalRevision,omitempty"`
	RecordedAt   time.Time  `json:"recordedAt"`
	Outcome      rd.Outcome `json:"outcome"`
	ReturnedAt   time.Time  `json:"returnedAt,omitzero"`
	Source       Source     `json:"source"`
}

// Link is one run-to-identity relationship. Target is the configured target
// alias a session was recorded for; it does not say that target is current.
type Link struct {
	Run          string      `json:"run"`
	Kind         rd.LinkKind `json:"kind"`
	Target       string      `json:"target,omitempty"`
	Provider     string      `json:"provider,omitempty"`
	Session      string      `json:"session,omitempty"`
	GoalID       string      `json:"goalId,omitempty"`
	GoalRevision uint64      `json:"goalRevision,omitempty"`
	Routine      *Routine    `json:"routine,omitempty"`
	Attempt      string      `json:"attempt,omitempty"`
	Source       Source      `json:"source"`
	RecordedAt   time.Time   `json:"recordedAt"`
}

func routineRef(r *Routine) rd.RoutineRef {
	if r == nil {
		return rd.RoutineRef{}
	}
	return rd.RoutineRef{Source: r.Source, Routine: r.Routine, Invocation: r.Invocation}
}

func routineOf(r rd.RoutineRef) *Routine {
	if r == (rd.RoutineRef{}) {
		return nil
	}
	return &Routine{Source: r.Source, Routine: r.Routine, Invocation: r.Invocation}
}

func (r Run) episode() rd.Episode {
	return rd.Episode{ID: r.ID, Origin: r.Origin, RecordedAt: r.RecordedAt, ClosedAt: r.ClosedAt, Reopens: r.Reopens, Routine: routineRef(r.Routine)}
}

func (a Attempt) contract() rd.Attempt {
	return rd.Attempt{ID: a.ID, RunID: a.Run, Action: a.Action, Mode: a.Mode, Goal: rd.GoalRef{ID: a.GoalID, Revision: a.GoalRevision},
		RecordedAt: a.RecordedAt, Outcome: a.Outcome, ReturnedAt: a.ReturnedAt}
}

func (l Link) contract() rd.Link {
	return rd.Link{RunID: l.Run, Kind: l.Kind, Session: rd.SessionRef{Provider: l.Provider, ID: l.Session},
		Goal: rd.GoalRef{ID: l.GoalID, Revision: l.GoalRevision}, Routine: routineRef(l.Routine), AttemptID: l.Attempt, RecordedAt: l.RecordedAt}
}

func (g *Graph) run(id string) *Run {
	for i := range g.Runs {
		if g.Runs[i].ID == id {
			return &g.Runs[i]
		}
	}
	return nil
}

// state rebuilds the contract's lifecycle value from the stored record.
func (g *Graph) state() rd.State {
	s := rd.State{LoopID: g.LoopID, Completed: g.Completed}
	if r := g.run(g.CurrentRun); r != nil {
		s.Current = r.episode()
	}
	return s
}

// valid checks the whole record against the contract. A record that fails is
// reported as corrupt and never rewritten.
func (g *Graph) valid() bool {
	if g.SchemaVersion != SchemaVersion || g.LoopID == "" || g.UpdatedAt.IsZero() ||
		len(g.Runs) > MaxRuns || len(g.Attempts) > MaxAttempts || len(g.Links) > MaxLinks || len(g.Registrations) > MaxRegistrations {
		return false
	}
	// The current run is always the newest record; trim relies on that order.
	if (g.CurrentRun != "" && (len(g.Runs) == 0 || g.Runs[len(g.Runs)-1].ID != g.CurrentRun)) || !g.state().Valid() {
		return false
	}
	seen := map[string]bool{}
	for _, r := range g.Runs {
		closed := !r.ClosedAt.IsZero()
		if seen[r.ID] || !r.Source.valid() || closed != (r.ClosedSource != "") || (closed && !r.ClosedSource.valid()) ||
			(!closed && r.ID != g.CurrentRun) || !(rd.State{LoopID: g.LoopID, Current: r.episode(), Completed: closed}).Valid() {
			return false
		}
		seen[r.ID] = true
	}
	for _, a := range g.Attempts {
		if seen[a.ID] || !seen[a.Run] || !a.contract().Valid() || a.Source != SourceAdapter || a.Target == "" || len(a.Target) > maxTarget {
			return false
		}
		seen[a.ID] = true
	}
	for _, l := range g.Links {
		if !seen[l.Run] || !l.contract().Valid() || !l.Source.valid() || len(l.Target) > maxTarget || (l.Kind != rd.SessionLink && l.Target != "") {
			return false
		}
	}
	for _, r := range g.Registrations {
		if _, err := rd.Register(rd.State{LoopID: g.LoopID}, r.ID, r.RecordedAt); err != nil || seen[r.ID] || !r.Source.valid() {
			return false
		}
		seen[r.ID] = true
	}
	return true
}

// trim applies the retention bounds. A run leaves with its attempts and links,
// so no retained record refers to a dropped run except a reopen predecessor.
func (g *Graph) trim() {
	for len(g.Runs) > MaxRuns || len(g.Attempts) > MaxAttempts || len(g.Links) > MaxLinks {
		if len(g.Runs) > 1 && g.Runs[0].ID != g.CurrentRun {
			id := g.Runs[0].ID
			g.Runs = g.Runs[1:]
			g.Dropped.Runs++
			attempts := g.Attempts[:0]
			for _, a := range g.Attempts {
				if a.Run == id {
					g.Dropped.Attempts++
				} else {
					attempts = append(attempts, a)
				}
			}
			g.Attempts = attempts
			links := g.Links[:0]
			for _, l := range g.Links {
				if l.Run == id {
					g.Dropped.Links++
				} else {
					links = append(links, l)
				}
			}
			g.Links = links
			continue
		}
		// Only the current run is left: drop its oldest attempts and links.
		if len(g.Attempts) > MaxAttempts {
			g.Attempts = g.Attempts[1:]
			g.Dropped.Attempts++
		}
		if len(g.Links) > MaxLinks {
			g.Links = g.Links[1:]
			g.Dropped.Links++
		}
	}
}
