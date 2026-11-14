package timing

import (
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// How the loop got to its route decides the failover gate: only a handoff
// (or a new conversation at one) counts, and state from before the record
// counts a recorded handoff.
func TestFromRecordsFailoverAndRetarget(t *testing.T) {
	tc := cfg()
	conf := &config.Config{Loop: config.Loop{Timing: &tc}}
	for _, c := range []struct {
		name      string
		st        state.File
		failover  bool
		retargets bool
	}{
		{"on primary", state.File{}, false, false},
		{"a handoff chose it", state.File{ActiveTarget: "claude-other", TargetSetBy: state.SetByHandoff}, true, false},
		{"a new conversation chose it", state.File{ActiveTarget: "claude-new", TargetSetBy: state.SetByNewThread}, true, false},
		{"timing chose it", state.File{ActiveTarget: "example-large", TargetSetBy: state.SetByTiming, LastHandoffAt: now.Add(-time.Hour)}, false, false},
		{"a person chose it while timing was off", state.File{ActiveTarget: "example-large", TargetSetBy: state.SetByRetarget}, false, true},
		{"state before the record, a handoff on record", state.File{ActiveTarget: "claude-other", LastHandoffAt: now.Add(-time.Hour)}, true, false},
		{"state before the record, no handoff", state.File{ActiveTarget: "example-large"}, false, true},
	} {
		in := FromRecords(conf, c.st, nil, now)
		if in.MovedByFailover != c.failover || (in.Retarget != nil) != c.retargets {
			t.Errorf("%s: failover %v retarget %+v", c.name, in.MovedByFailover, in.Retarget)
		}
	}
	// A retarget waiting on no known reset takes the first named window's
	// reset from a reading taken after it.
	in := FromRecords(conf, state.File{ActiveTarget: "example-large", TargetSetBy: state.SetByRetarget,
		Timing: &state.TimingRecord{Retarget: &state.TimingRetarget{At: h(-1)}}}, nil, now)
	in.Usage = reading(0.5, win{"seven_day", 0.4, 30})
	if at := RetargetReset(in); at == nil || !at.Equal(h(30)) {
		t.Fatalf("reset: %v", at)
	}
	in.Usage = reading(2, win{"seven_day", 0.4, 30})
	if at := RetargetReset(in); at != nil {
		t.Fatalf("a reading from before the retarget: %v", at)
	}
}
