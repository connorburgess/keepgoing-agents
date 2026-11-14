package decide

// Stopping a loop's lanes. A person can ask for a stop of the loop and its
// lanes (keepgoing stop --lanes). The check that carries out the stop then
// walks the supervised conversation's verified descendants and stops those
// that are running. These are the pure rules; the supervisor walks the tree
// and makes the stops.

// Lane-stop bounds: how deep below the supervised conversation, and how many
// threads in all, one stop looks at. Past them nothing is stopped and the
// threads not looked at are counted.
const (
	LaneStopMaxDepth   = 3
	LaneStopMaxThreads = 50
)

// LaneNode is what one check established about a thread below the
// supervised conversation.
type LaneNode struct {
	// Verified: re-read on its own, it names itself and the expected parent
	// and is neither archived nor deleted.
	Verified bool
	// Status is BB's thread status: active, starting, pending, idle, error,
	// stopping, or anything else this build does not know. active and
	// starting have a session running or starting, and are stopped. pending
	// has no session yet: no message has cleared dispatch, and `bb thread
	// stop` has no transition from it. It is left alone, its queued messages
	// kept, unless the request explicitly asks for them to be deleted
	// (DropQueued). (Child discovery counts neither starting nor pending as
	// active work; the supervised thread's own stop does stop a pending one.)
	Status string
	// Queued counts messages waiting to run in the thread. For a thread in
	// error that is a retry waiting its turn: BB's provider retry, or one
	// asked for by keepgoing or a person.
	Queued int
	// OtherLoop: another keepgoing loop supervises this thread or holds its
	// session claim. That loop's own stop is the way to stop it.
	OtherLoop bool
	// OwnershipUnknown: whether another loop supervises it could not be
	// established (its session record could not be read). Nothing is done
	// to it or below it.
	OwnershipUnknown bool
	// DropQueued: the request explicitly asks for the messages queued in
	// lanes that have not started, or are idle, to be deleted. Their text is
	// not kept, so this is never implied by a stop with lanes.
	DropQueued bool
	// StopRecorded: an earlier check recorded a stop of this thread and
	// could not save whether it was made.
	StopRecorded bool
}

// LaneStopAction is what a stop with lanes does to one thread.
type LaneStopAction string

const (
	// LaneStopRunning: stop it. It is working, or about to.
	LaneStopRunning LaneStopAction = "stop"
	// LaneStopQueued: it is in error with a retry waiting. Cancel the
	// provider's retry where the provider can, then stop it.
	LaneStopQueued LaneStopAction = "stop_queued"
	// LaneStopDequeue: it has not started (pending), or it is idle with a
	// message queued, and the request asks for queued messages to be
	// deleted. Nothing runs to stop; delete what is queued, so it does not
	// start.
	LaneStopDequeue LaneStopAction = "dequeue"
	// LaneStopKeepQueued: the same lane, when the request does not ask for
	// that. It is left alone, and its queued messages are kept; BB may run
	// them.
	LaneStopKeepQueued LaneStopAction = "not_started"
	// LaneStopIdle: nothing is running; nothing is done to it.
	LaneStopIdle LaneStopAction = "already_idle"
	// LaneStopStoppedEarlier: an earlier check recorded its stop and it is no
	// longer running, so that stop was made. It is not stopped again.
	LaneStopStoppedEarlier LaneStopAction = "stopped_earlier"
	// LaneStopOtherLoop: another loop's lane; left to that loop.
	LaneStopOtherLoop LaneStopAction = "other_loop"
	// LaneStopOwnershipUnknown: whether it is another loop's could not be
	// established; nothing is done to it or below it.
	LaneStopOwnershipUnknown LaneStopAction = "ownership_unconfirmed"
	// LaneStopUnverified: it could not be verified; nothing is done to it.
	LaneStopUnverified LaneStopAction = "unverified"
	// LaneStopUnknown: its status is one this build does not know; nothing
	// is done to it.
	LaneStopUnknown LaneStopAction = "unknown_status"
)

// LaneStopFor decides what a stop with lanes does to one thread. Only a
// verified thread of this loop's that is running, or in error with a retry
// waiting, is stopped; nothing is ever sent to a thread but a stop.
func LaneStopFor(n LaneNode) LaneStopAction {
	switch {
	case !n.Verified:
		return LaneStopUnverified
	case n.OwnershipUnknown:
		return LaneStopOwnershipUnknown
	case n.OtherLoop:
		return LaneStopOtherLoop
	}
	running, queued := false, false
	switch n.Status {
	case "active", "starting":
		running = true
	case "error":
		running = n.Queued > 0
	case "pending":
		queued = true
	case "idle":
		queued = n.Queued > 0
	case "stopping":
	default:
		return LaneStopUnknown
	}
	if queued {
		if !n.DropQueued {
			return LaneStopKeepQueued
		}
		// A killed check's record does not change this: what is still
		// queued is deleted.
		return LaneStopDequeue
	}
	switch {
	case !running && n.StopRecorded:
		return LaneStopStoppedEarlier
	case !running:
		return LaneStopIdle
	case n.Status == "error":
		return LaneStopQueued
	}
	return LaneStopRunning
}

// LaneStopDescend says whether the walk looks below a thread: below every
// verified thread of this loop's, idle ones included, since an idle lane can
// have a running child. Not below another loop's lane, which that loop
// stops, nor below one that could not be verified.
func LaneStopDescend(a LaneStopAction) bool {
	switch a {
	case LaneStopOtherLoop, LaneStopOwnershipUnknown, LaneStopUnverified:
		return false
	}
	return true
}

// LaneStopsDue says whether a check should walk the lanes: a stop with lanes
// is requested and an earlier check has not finished it. A dry run does not.
func LaneStopsDue(c Control, lanesRequested, done, dryRun bool) bool {
	return c.Stop && lanesRequested && !done && !dryRun
}
