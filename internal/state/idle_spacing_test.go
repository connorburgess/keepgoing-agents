package state

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCountIdleSpacedIsBoundedPerDay(t *testing.T) {
	var f File
	start := time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC)
	for d := 0; d < 40; d++ {
		at := start.AddDate(0, 0, d)
		f.CountIdleSpaced("idle_unchanged", at)
		f.CountIdleSpaced("idle_unchanged", at.Add(90*time.Minute)) // the next UTC day
	}
	if len(f.IdleSpacedDays) != IdleSpacedDaysKept {
		t.Fatalf("kept %d days", len(f.IdleSpacedDays))
	}
	if last := f.IdleSpacedDays[len(f.IdleSpacedDays)-1]; last.Day != "2026-12-11" || last.Counts["idle_unchanged"] != 1 {
		t.Fatalf("newest %+v", last)
	}
	// A clock that moved back counts into the newest day.
	f.CountIdleSpaced("idle_unchanged", start)
	if len(f.IdleSpacedDays) != IdleSpacedDaysKept || f.IdleSpacedDays[IdleSpacedDaysKept-1].Counts["idle_unchanged"] != 2 {
		t.Fatalf("clock back: %+v", f.IdleSpacedDays[IdleSpacedDaysKept-1])
	}
	total := f.IdleSpacedTotal()
	b, _ := json.Marshal(f)
	var back File
	if err := json.Unmarshal(b, &back); err != nil || back.IdleSpacedTotal() != total {
		t.Fatalf("round trip: %v %d %d", err, back.IdleSpacedTotal(), total)
	}
}
