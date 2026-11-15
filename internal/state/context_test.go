package state

import (
	"fmt"
	"testing"
	"time"
)

func reading(used int64, at time.Time) ContextReading {
	return ContextReading{UsedTokens: used, WindowTokens: 200_000, Estimated: true, At: at}
}

// One summary per UTC day: lowest, highest, last and count.
func TestNoteContextSummarizesEachUTCDay(t *testing.T) {
	day := time.Date(2026, 11, 16, 0, 30, 0, 0, time.UTC)
	var f File
	for i, used := range []int64{90_000, 30_000, 120_000, 60_000} {
		f.NoteContext(Primary, reading(used, day.Add(time.Duration(i)*time.Hour)))
	}
	f.NoteContext(Primary, ContextReading{UsedTokens: 10_000, WindowTokens: 100_000, At: day.Add(24 * time.Hour)})
	got := f.Context.Days
	want := []ContextDay{
		{Day: "2026-11-16", Lowest: 30_000, Highest: 120_000, Last: 60_000, WindowTokens: 200_000, Count: 4, Estimated: true},
		{Day: "2026-11-17", Lowest: 10_000, Highest: 10_000, Last: 10_000, WindowTokens: 100_000, Count: 1},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("days %+v\nwant %+v", got, want)
	}
	if f.Context.Latest.UsedTokens != 10_000 || f.Context.Version != ContextVersion || f.Context.Target != Primary {
		t.Fatalf("latest %+v", f.Context)
	}
	// A reading for a day already summarized, read late (a clock moved back),
	// joins that day.
	f.NoteContext(Primary, reading(5_000, day.Add(2*time.Hour)))
	if d := f.Context.Days[0]; d.Lowest != 5_000 || d.Count != 5 || len(f.Context.Days) != 2 {
		t.Fatalf("late reading: %+v", f.Context.Days)
	}
}

// The history is hard-capped: 31 days and 8 recent readings, oldest dropped.
func TestNoteContextIsBounded(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var f File
	for i := range 100 {
		f.NoteContext(Primary, reading(int64(i), start.Add(time.Duration(i)*24*time.Hour)))
	}
	c := f.Context
	if len(c.Days) != ContextDaysKept || len(c.Recent) != ContextRecentKept {
		t.Fatalf("kept %d days and %d readings", len(c.Days), len(c.Recent))
	}
	if c.Days[0].Day != start.Add(69*24*time.Hour).Format("2006-01-02") || c.Days[ContextDaysKept-1].Last != 99 || c.Recent[0].UsedTokens != 92 {
		t.Fatalf("kept the wrong end: first day %s, first recent %d", c.Days[0].Day, c.Recent[0].UsedTokens)
	}
	// A reading older than every kept day is dropped from the days at once.
	f.NoteContext(Primary, reading(7, start))
	if len(c.Days) != ContextDaysKept || f.Context.Days[0].Day == start.Format("2006-01-02") {
		t.Fatalf("an out-of-range day was kept: %+v", f.Context.Days[0])
	}
}

// Readings belong to one conversation: another target starts a new record,
// and a target change or transfer clears it.
func TestContextHistoryBelongsToOneTarget(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	var f File
	f.NoteContext(Primary, reading(100, at))
	f.NoteContext("claude-other", reading(5, at.Add(time.Minute)))
	if c := f.Context; c.Target != "claude-other" || len(c.Recent) != 1 || len(c.Days) != 1 || c.Days[0].Highest != 5 {
		t.Fatalf("another target's record: %+v", c)
	}
	f.ClearChildErrors()
	if f.Context != nil {
		t.Fatal("a target change kept the context history")
	}
}
