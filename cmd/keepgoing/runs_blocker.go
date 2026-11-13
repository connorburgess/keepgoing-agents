package main

import (
	"os"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A blocker is reported only from structured records: the child listing a
// check verified, the reason that same check recorded, and the ownership
// claim in control.json. Nothing here reads a PID or matches names, and
// nothing contacts the other conversation. Showing a blocker grants no control
// over it, and another conversation's thread ID is never printed for it.

// Blocker availability.
const (
	// blockerBlocked: the last check verified every child and recorded child
	// work or child input as its reason.
	blockerBlocked = "blocked"
	// blockerHeld: the loop is on an awaiting-user hold that began when a
	// check found a child waiting on a person. Later checks do not observe,
	// so the listing is the last verified one, shown with its age.
	blockerHeld = "held"
	// blockerListed: the last check verified every child but recorded another
	// reason, so no child is said to hold the run.
	blockerListed = "listed"
	// blockerNone: the last check verified every child and none could hold the run.
	blockerNone = "none"
	// blockerUnknown: the evidence is missing, incomplete, old, or not from
	// the last check.
	blockerUnknown = "unknown"
)

// runsNow is the reader's clock, replaced in tests.
var runsNow = func() time.Time { return time.Now().UTC() }

type runsBlocker struct {
	Availability string     `json:"availability"`
	Reason       string     `json:"reason,omitempty"`
	CheckedAt    *time.Time `json:"checkedAt,omitempty"`
	// AgeSeconds is how long ago that check ran. Stale is true when it is
	// older than --stale-after; a stale listing is never reported as blocked
	// or none.
	AgeSeconds *int64 `json:"ageSeconds,omitempty"`
	Stale      bool   `json:"stale"`
	Target     string `json:"target,omitempty"`
	// LastCheckCause is the fixed-vocabulary cause of the last recorded
	// check, such as child_busy, child_input, stop or awaiting_user.
	LastCheckCause string `json:"lastCheckCause,omitempty"`
	// Children are the target's direct child threads at that check, by key.
	// Holds is true only when the child's status holds a parent and the last
	// check recorded child work or child input as its reason.
	Children        []runsChild `json:"children"`
	OmittedChildren int         `json:"omittedChildren"`
	// Owner is the conversation whose claim refuses control commands from
	// other conversations. It is independent of the child listing.
	Owner runsOwner `json:"owner"`
	// Lock says that who holds the tick or daemon lock is not identified.
	Lock runsUnavailable `json:"lock"`
}

type runsChild struct {
	Ref    string `json:"ref"`
	Status string `json:"status"` // active, awaiting_input or error
	Holds  bool   `json:"holds"`
	// Supervised says whether this child is itself a registered loop. Its
	// conversation is not printed here: follow the registry reference.
	Supervised runsSupervised `json:"supervised"`
}

type runsSupervised struct {
	// recorded, not_registered, ambiguous, or registry_not_consulted.
	Availability   string `json:"availability"`
	RegistrationID string `json:"registrationId,omitempty"`
	Loop           string `json:"loop,omitempty"`
	Run            string `json:"run,omitempty"`
	// RunReason says why no run is named for a registered loop.
	RunReason string `json:"runReason,omitempty"`
}

type runsOwner struct {
	// recorded, not_recorded (no claim or no control record), or unknown.
	Availability string `json:"availability"`
	Reason       string `json:"reason,omitempty"`
	// Ref is session:bb/<thread> with a configuration selector, where status
	// already prints the owner. Through a registry selector, where registered
	// status withholds it, it is owner:<key>.
	Ref        string `json:"ref,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	// BlocksCaller is true when this command ran from a different
	// conversation, whose control commands the claim refuses.
	BlocksCaller bool           `json:"blocksCaller"`
	Supervised   runsSupervised `json:"supervised"`
}

func childKeyOf(thread string) string { return decide.ChildHashes([]string{thread})[0] }

// runsCandidate is one registered loop that supervises a BB conversation and
// whose configuration binding validated.
type runsCandidate struct {
	entry  registry.Entry
	thread string
}

// candidates validates every registry entry once per invocation.
func (c *runsContext) candidates() []runsCandidate {
	if c.candidatesLoaded {
		return c.candidateList
	}
	c.candidatesLoaded = true
	own, err := canonicalFuturePath(c.dir)
	if err != nil {
		own = c.dir
	}
	for _, entry := range c.registry {
		if entry.StateDir == own {
			continue // The loop itself is never its own blocker.
		}
		cfg, ok := clientConfig(entry)
		if !ok || cfg.Loop.Agent.Adapter != "bb" || cfg.Loop.Agent.BB == nil {
			continue
		}
		c.candidateList = append(c.candidateList, runsCandidate{entry: entry, thread: cfg.Loop.Agent.BB.Thread})
	}
	return c.candidateList
}

// supervised finds the one registered loop whose validated configuration
// names thread (or the thread behind key) as its primary BB conversation, and
// the open run of that loop that recorded it. The match is on the configured
// thread only; a loop that merely shares a name is never a match. The
// conversation's thread ID is not returned.
func (c *runsContext) supervised(thread, key string) runsSupervised {
	if !c.registryKnown {
		return runsSupervised{Availability: "registry_not_consulted"}
	}
	var found []runsSupervised
	for _, candidate := range c.candidates() {
		if (thread != "" && candidate.thread != thread) || (thread == "" && childKeyOf(candidate.thread) != key) {
			continue
		}
		s := runsSupervised{Availability: recordRecorded, RegistrationID: candidate.entry.ID, Loop: candidate.entry.Loop, RunReason: "no_open_run_recorded_for_this_conversation"}
		if g, availability := rungraph.Load(candidate.entry.StateDir); availability == rungraph.Available && !g.Completed {
			if run, status := g.Current(); status == rungraph.Found {
				for _, e := range run.Edges {
					if e.Type == rungraph.EdgeSession && e.To == rungraph.SessionRef("bb", candidate.thread) {
						s.Run, s.RunReason = run.Ref, ""
					}
				}
			}
		}
		found = append(found, s)
	}
	switch len(found) {
	case 0:
		return runsSupervised{Availability: "not_registered"}
	case 1:
		return found[0]
	}
	return runsSupervised{Availability: "ambiguous"}
}

// blocker projects what the open run is waiting on. Every view uses this one
// answer, so a listing that is not shown here is not followable by ID either.
func (c *runsContext) blocker(st state.File, stateKnown bool, ctl control.File, controlKnown bool) runsBlocker {
	b := runsBlocker{Availability: blockerUnknown, Children: []runsChild{}, Owner: c.owner(ctl, controlKnown),
		Lock: runsUnavailable{Availability: recordUnavailable, Reason: "lock_holder_not_identified"}}
	w := c.g.Waiting
	switch {
	case c.available == rungraph.Unsupported || c.available == rungraph.Corrupt:
		b.Reason = "graph_unavailable"
		return b
	case c.g.CurrentRun == "" || c.g.Completed:
		b.Reason = "no_open_run"
		return b
	case w == nil:
		b.Reason = "no_child_evidence_recorded"
		return b
	}
	at := w.CheckedAt
	age := int64(runsNow().Sub(at) / time.Second)
	b.CheckedAt, b.AgeSeconds, b.Target, b.Stale = &at, &age, w.Target, runsNow().Sub(at) > c.staleAfter
	if !stateKnown || st.LastCheck == nil {
		b.Reason = "last_check_not_recorded"
		return b
	}
	b.LastCheckCause = incident.Cause(st.LastCheck.Reason)
	sameRun := w.Run == c.g.CurrentRun && w.Target == c.active
	current := sameRun && st.LastCheck.At.Equal(w.CheckedAt)
	holding := false
	for _, child := range w.Children {
		holding = holding || rungraph.Holds(child.Status)
	}
	stuck := false
	for _, child := range w.Children {
		stuck = stuck || child.Status == rungraph.ChildAwaiting
	}
	list := func(holds bool) {
		for _, child := range w.Children {
			if len(b.Children) >= c.limit {
				b.OmittedChildren++
				continue
			}
			b.Children = append(b.Children, runsChild{Ref: rungraph.ChildRef(child.Key), Status: child.Status, Holds: holds && rungraph.Holds(child.Status), Supervised: c.supervised("", child.Key)})
		}
	}
	switch {
	case !current && sameRun && w.Listing == rungraph.ListingComplete && stuck && controlKnown && ctl.AwaitingUser && b.LastCheckCause == "awaiting_user":
		// A stuck child put the loop on hold, and held checks do not observe.
		// Keep the last verified listing, with its age, and say why.
		b.Availability, b.Reason = blockerHeld, "awaiting_user_hold; children last verified at that check"
		list(true)
	case !current:
		b.Reason = "not_refreshed_by_the_last_check"
	case w.Listing != rungraph.ListingComplete:
		b.Reason = "child_listing_incomplete"
	case b.Stale:
		b.Reason = "last_check_older_than_stale_after"
	case !holding:
		b.Availability = blockerNone
		list(false)
	case b.LastCheckCause == "child_busy" || b.LastCheckCause == "child_input":
		b.Availability = blockerBlocked
		list(true)
	default:
		// Children were verified, but the check recorded something else, such
		// as a stop request: they are not what holds the run.
		b.Availability, b.Reason = blockerListed, "last_check_recorded_another_reason"
		list(false)
	}
	return b
}

func (c *runsContext) owner(ctl control.File, controlKnown bool) runsOwner {
	o := runsOwner{Availability: blockerUnknown, Supervised: runsSupervised{Availability: "registry_not_consulted"}}
	switch {
	case !controlKnown:
		o.Reason = "control_not_readable_or_not_recorded"
	case ctl.PendingTransfer != nil:
		o.Reason = "transfer_pending"
	case ctl.Ownership == nil:
		o.Availability = recordNotRecorded
	case control.CheckOwner(ctl, "", false) != nil:
		o.Reason = "invalid_ownership_record"
	default:
		thread := ctl.Ownership.ThreadID
		o.Availability, o.Generation = recordRecorded, ctl.Ownership.Generation
		o.Ref = rungraph.SessionRef("bb", thread)
		if c.viaRegistry {
			o.Ref = "owner:" + childKeyOf(thread)
		}
		// The caller's own identity is compared, never printed.
		caller := os.Getenv("BB_THREAD_ID")
		o.BlocksCaller = caller != "" && caller != thread && control.CheckOwner(ctl, caller, false) != nil
		o.Supervised = c.supervised(thread, "")
	}
	return o
}

// childEdges says how a view may present the graph's child edges: as holding
// (blocked or held), as listed without holding, or not at all.
func (b runsBlocker) childEdges() (show, holds bool) {
	switch b.Availability {
	case blockerBlocked, blockerHeld:
		return true, true
	case blockerListed, blockerNone:
		return true, false
	}
	return false, false
}
