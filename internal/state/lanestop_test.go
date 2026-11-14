package state

import (
	"testing"
	"time"
)

// A resume that starts a new episode counts the sends it settles as the
// agent's, so it moves the time of the last send. A lane stop is not one:
// neither it nor the stop report moves it.
func TestSettleIntoEpisodeDoesNotCountLaneStops(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)
	for _, a := range []string{"stop:lane", "report:stop_lanes", "stop"} {
		if _, err := Reserve(dir, 0, 0, SendIntent{At: at, Action: a, Target: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	var f File
	if err := SettleIntoEpisode(dir, &f); err != nil {
		t.Fatal(err)
	}
	if !f.LastWakeAt.IsZero() || f.SendsRecorded != 3 {
		t.Fatalf("%v %d", f.LastWakeAt, f.SendsRecorded)
	}
}

// The lane key is made once, kept private, and read back the same.
func TestLaneKeyIsMadeOnceAndReadBack(t *testing.T) {
	dir := t.TempDir()
	k1, err := LaneKey(dir)
	if err != nil || len(k1) != 32 {
		t.Fatal(err)
	}
	k2, err := LaneKey(dir)
	if err != nil || string(k1) != string(k2) {
		t.Fatal("the lane key changed")
	}
	if LaneHash(k1, "thr_lane_a") == LaneHash(make([]byte, 32), "thr_lane_a") || len(LaneHash(k1, "thr_lane_a")) != 24 {
		t.Fatal("the lane hash does not depend on the key")
	}
}
