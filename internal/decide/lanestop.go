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
	// stopping, or anything else this build does not know.
	Status string
	// Queued counts messages waiting to run in the thread. For a thread in
	// error that is a retry waiting its turn: BB's provider retry, or one
	// asked for by keepgoing or a person.
	Queued int
	// OtherLoop: another keepgoing loop supervises this thread or holds its
	// session claim. That loop's own stop is the way to stop it.
	OtherLoop bool
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
	// LaneStopIdle: nothing is running; nothing is done to it.
	LaneStopIdle LaneStopAction = "already_idle"
	// LaneStopStoppedEarlier: an earlier check recorded its stop and it is no
	// longer running, so that stop was made. It is not stopped again.
	LaneStopStoppedEarlier LaneStopAction = "stopped_earlier"
	// LaneStopOtherLoop: another loop's lane; left to that loop.
	LaneStopOtherLoop LaneStopAction = "other_loop"
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
	case n.OtherLoop:
		return LaneStopOtherLoop
	}
	running := false
	switch n.Status {
	case "active", "starting", "pending":
		running = true
	case "error":
		running = n.Queued > 0
	case "idle", "stopping":
	default:
		return LaneStopUnknown
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
	case LaneStopOtherLoop, LaneStopUnverified:
		return false
	}
	return true
}

// LaneStopsDue says whether a check should walk the lanes: a stop with lanes
// is requested and an earlier check has not finished it. A dry run does not.
func LaneStopsDue(c Control, lanesRequested, done, dryRun bool) bool {
	return c.Stop && lanesRequested && !done && !dryRun
}
