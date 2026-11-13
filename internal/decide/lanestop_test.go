package decide

import "testing"

func TestLaneStopFor(t *testing.T) {
	ok := func(status string, queued int) LaneNode {
		return LaneNode{Verified: true, Status: status, Queued: queued}
	}
	for name, tc := range map[string]struct {
		node    LaneNode
		action  LaneStopAction
		descend bool
	}{
		"working":                          {ok("active", 0), LaneStopRunning, true},
		"starting":                         {ok("starting", 0), LaneStopRunning, true},
		"pending start":                    {ok("pending", 0), LaneStopRunning, true},
		"working with messages queued":     {ok("active", 2), LaneStopRunning, true},
		"idle":                             {ok("idle", 0), LaneStopIdle, true},
		"idle with a message queued":       {ok("idle", 1), LaneStopIdle, true},
		"already stopping":                 {ok("stopping", 0), LaneStopIdle, true},
		"in error, nothing queued":         {ok("error", 0), LaneStopIdle, true},
		"in error with a retry queued":     {ok("error", 1), LaneStopQueued, true},
		"a status this build cannot read":  {ok("paused-ish", 0), LaneStopUnknown, true},
		"no status":                        {ok("", 0), LaneStopUnknown, true},
		"not verified, though active":      {LaneNode{Status: "active"}, LaneStopUnverified, false},
		"another loop's, active":           {LaneNode{Verified: true, Status: "active", OtherLoop: true}, LaneStopOtherLoop, false},
		"another loop's, idle":             {LaneNode{Verified: true, Status: "idle", OtherLoop: true}, LaneStopOtherLoop, false},
		"recorded stop, now idle":          {LaneNode{Verified: true, Status: "idle", StopRecorded: true}, LaneStopStoppedEarlier, true},
		"recorded stop, now stopping":      {LaneNode{Verified: true, Status: "stopping", StopRecorded: true}, LaneStopStoppedEarlier, true},
		"recorded stop, still active":      {LaneNode{Verified: true, Status: "active", StopRecorded: true}, LaneStopRunning, true},
		"recorded stop, retry still waits": {LaneNode{Verified: true, Status: "error", Queued: 1, StopRecorded: true}, LaneStopQueued, true},
	} {
		got := LaneStopFor(tc.node)
		if got != tc.action || LaneStopDescend(got) != tc.descend {
			t.Errorf("%s: %s (descend %v), want %s (descend %v)", name, got, LaneStopDescend(got), tc.action, tc.descend)
		}
	}
}

func TestLaneStopsDue(t *testing.T) {
	stop := Control{Enabled: true, Stop: true}
	for name, tc := range map[string]struct {
		c                    Control
		lanes, done, dry, ok bool
	}{
		"stop with lanes":                    {stop, true, false, false, true},
		"plain stop":                         {stop, false, false, false, false},
		"already done":                       {stop, true, true, false, false},
		"dry run":                            {stop, true, false, true, false},
		"lanes asked for, stop since lifted": {Control{Enabled: true}, true, false, false, false},
		"paused and held: stop still wins":   {Control{Stop: true, Paused: true, AwaitingUser: true}, true, false, false, true},
		"disabled: stop still wins":          {Control{Stop: true}, true, false, false, true},
	} {
		if got := LaneStopsDue(tc.c, tc.lanes, tc.done, tc.dry); got != tc.ok {
			t.Errorf("%s: %v", name, got)
		}
	}
}
