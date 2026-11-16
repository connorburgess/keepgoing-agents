package state

import (
	"testing"
	"time"
)

func i64(n int64) *int64 { return &n }

// A turn is counted once however many checks see it; each UTC day sums the
// turns recorded on it, with how many turns reported each count.
func TestNoteTurnUsageCountsEachTurnOnce(t *testing.T) {
	day := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	var f File
	a := TurnUsageReading{Turn: "turn-a", At: day, Input: i64(1000), CachedInput: i64(600), Output: i64(20)}
	if !f.NoteTurnUsage(Primary, a) {
		t.Fatal("the first turn was not recorded")
	}
	again := a
	again.At = day.Add(time.Hour) // a later check reading the same result file
	if f.NoteTurnUsage(Primary, again) {
		t.Fatal("the same turn was counted twice")
	}
	f.NoteTurnUsage(Primary, TurnUsageReading{Turn: "turn-b", At: day.Add(2 * time.Hour), Input: i64(500), Output: i64(5)})
	f.NoteTurnUsage(Primary, TurnUsageReading{Turn: "turn-c", At: day.Add(20 * time.Hour), Output: i64(7)})
	u := f.TurnUsage
	if u.Version != TurnUsageVersion || u.Target != Primary || u.Latest.Turn != "turn-c" || len(u.Days) != 2 {
		t.Fatalf("%+v", u)
	}
	d := u.Days[0]
	if d.Day != "2026-11-16" || d.Turns != 2 || d.Input != (TurnUsageSum{1500, 2}) || d.CachedInput != (TurnUsageSum{600, 1}) ||
		d.Output != (TurnUsageSum{25, 2}) || d.CacheWriteInput != (TurnUsageSum{}) || d.ReasoningOutput != (TurnUsageSum{}) {
		t.Fatalf("first day %+v", d)
	}
	if d := u.Days[1]; d.Day != "2026-11-17" || d.Turns != 1 || d.Output != (TurnUsageSum{7, 1}) || d.Input != (TurnUsageSum{}) {
		t.Fatalf("second day %+v", d)
	}
	if f.NoteTurnUsage(Primary, TurnUsageReading{At: day}) {
		t.Fatal("a reading without a turn identity was recorded")
	}
}

// At most TurnUsageDaysKept days are kept, oldest dropped first, and a
// record of another target is replaced, not merged.
func TestNoteTurnUsageIsBounded(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var f File
	for i := range 40 {
		f.NoteTurnUsage(Primary, TurnUsageReading{Turn: start.AddDate(0, 0, i).Format(time.RFC3339), At: start.AddDate(0, 0, i), Output: i64(1)})
	}
	if n := len(f.TurnUsage.Days); n != TurnUsageDaysKept || f.TurnUsage.Days[0].Day != "2026-10-10" {
		t.Fatalf("%d days, first %s", n, f.TurnUsage.Days[0].Day)
	}
	f.NoteTurnUsage("fallback-example", TurnUsageReading{Turn: "other", At: start, Output: i64(3)})
	if f.TurnUsage.Target != "fallback-example" || len(f.TurnUsage.Days) != 1 {
		t.Fatalf("another target's turn was merged: %+v", f.TurnUsage)
	}
}

// The record belongs to the target it was taken from: a change of target
// clears it with the other observations.
func TestTargetChangeClearsTurnUsage(t *testing.T) {
	var f File
	f.NoteTurnUsage(Primary, TurnUsageReading{Turn: "turn-a", At: time.Now(), Output: i64(1)})
	f.ClearChildErrors()
	if f.TurnUsage != nil {
		t.Fatal("the turn usage record survived a change of target")
	}
}
