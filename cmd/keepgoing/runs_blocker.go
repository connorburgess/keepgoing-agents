package main

import (
	"os"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A blocker is reported only from structured records: the child listing a
// check verified, and the ownership claim in control.json. Nothing here reads
// a PID, compares ages or matches names, and nothing contacts the other
// conversation. Showing a blocker grants no control over it.

// Blocker availability.
const (
	blockerBlocked = "blocked" // at least one child held the run at the last check
	blockerNone    = "none"    // the last check verified every child and none held the run
	blockerUnknown = "unknown" // the evidence is missing, incomplete or older than the last check
)

type runsBlocker struct {
	Availability string     `json:"availability"`
	Reason       string     `json:"reason,omitempty"`
	CheckedAt    *time.Time `json:"checkedAt,omitempty"`
	Target       string     `json:"target,omitempty"`
	// Children are the target's direct child threads at that check, by key.
	// Holds is true for a working or stuck child; an errored child is listed
	// but does not hold the run.
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
	// Supervised says whether this child is itself a registered loop, and if
	// so which run of that loop recorded the same conversation.
	Supervised runsSupervised `json:"supervised"`
}

type runsSupervised struct {
	// recorded, not_registered, ambiguous, or registry_not_consulted.
	Availability   string `json:"availability"`
	RegistrationID string `json:"registrationId,omitempty"`
	Loop           string `json:"loop,omitempty"`
	Session        string `json:"session,omitempty"`
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

// supervised finds the one registered loop whose validated configuration
// names thread (or the thread behind key) as its primary BB conversation, and
// the open run of that loop that recorded it. The match is on the configured
// thread only. Another loop that merely shares a name is never a match.
func (c *runsContext) supervised(thread, key string) runsSupervised {
	if !c.registryKnown {
		return runsSupervised{Availability: "registry_not_consulted"}
	}
	var found []runsSupervised
	for _, entry := range c.registry {
		if entry.StateDir == c.dir {
			continue
		}
		cfg, ok := clientConfig(entry)
		if !ok || cfg.Loop.Agent.Adapter != "bb" || cfg.Loop.Agent.BB == nil {
			continue
		}
		id := cfg.Loop.Agent.BB.Thread
		if (thread != "" && id != thread) || (thread == "" && childKeyOf(id) != key) {
			continue
		}
		s := runsSupervised{Availability: recordRecorded, RegistrationID: entry.ID, Loop: entry.Loop, Session: rungraph.SessionRef("bb", id), RunReason: "no_open_run_recorded_for_this_conversation"}
		if g, availability := rungraph.Load(entry.StateDir); availability == rungraph.Available && !g.Completed {
			if run, status := g.Current(); status == rungraph.Found {
				for _, e := range run.Edges {
					if e.Type == rungraph.EdgeSession && e.To == s.Session {
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

// blocker projects what the open run is waiting on. The waiting record counts
// only when the last recorded check is the one that wrote it.
func (c *runsContext) blocker(st state.File, stateKnown bool, ctl control.File, controlKnown bool) runsBlocker {
	b := runsBlocker{Availability: blockerUnknown, Children: []runsChild{}, Owner: c.owner(ctl, controlKnown),
		Lock: runsUnavailable{Availability: recordUnavailable, Reason: "lock_holder_not_identified"}}
	w := c.g.Waiting
	switch {
	case c.available == rungraph.Unsupported || c.available == rungraph.Corrupt:
		b.Reason = "graph_unavailable"
	case c.g.CurrentRun == "" || c.g.Completed:
		b.Reason = "no_open_run"
	case w == nil:
		b.Reason = "no_child_evidence_recorded"
	case !stateKnown || st.LastCheck == nil:
		b.Reason = "last_check_not_recorded"
	case !st.LastCheck.At.Equal(w.CheckedAt) || w.Run != c.g.CurrentRun || w.Target != c.active:
		b.Reason = "not_refreshed_by_the_last_check"
	case w.Listing != rungraph.ListingComplete:
		b.Reason = "child_listing_incomplete"
	default:
		b.Availability = blockerNone
		for _, child := range w.Children {
			if rungraph.Holds(child.Status) {
				b.Availability = blockerBlocked
			}
			if len(b.Children) >= c.limit {
				b.OmittedChildren++
				continue
			}
			b.Children = append(b.Children, runsChild{Ref: rungraph.ChildRef(child.Key), Status: child.Status, Holds: rungraph.Holds(child.Status), Supervised: c.supervised("", child.Key)})
		}
	}
	if w != nil && b.Reason != "graph_unavailable" && b.Reason != "no_open_run" {
		at := w.CheckedAt
		b.CheckedAt, b.Target = &at, w.Target
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
		if c.viaRegistry {
			o.Supervised.Session = ""
		}
	}
	return o
}
