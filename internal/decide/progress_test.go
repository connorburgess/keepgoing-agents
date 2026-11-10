package decide

import "testing"

func TestNoProgressPrecedence(t *testing.T) {
	for _, state := range []State{Idle, Active, Pending, Error, Unknown, Stopping} {
		t.Run(string(state), func(t *testing.T) {
			in := Input{Control: Control{Enabled: true}, Obs: Observation{State: state}, NoProgressReached: true}
			if got := Decide(in); got.Action != AwaitUser {
				t.Fatalf("no progress: %+v", got)
			}
			for _, hold := range []Control{{}, {Enabled: true, Paused: true}, {Enabled: true, Completed: true}, {Enabled: true, AwaitingUser: true}, {Enabled: true, Stop: true}} {
				in.Control = hold
				got := Decide(in)
				want := None
				if hold.Stop && (state == Active || state == Pending) {
					want = Stop
				}
				if got.Action != want {
					t.Fatalf("hold %+v: %+v", hold, got)
				}
			}
		})
	}
}
