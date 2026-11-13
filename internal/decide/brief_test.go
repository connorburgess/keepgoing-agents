package decide

import (
	"testing"
	"time"
)

func TestBriefGate(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	m := time.Minute
	cases := []struct {
		name    string
		a       BriefAttempts
		max     int
		backoff time.Duration
		want    BriefVerdict
	}{
		{"never rejected", BriefAttempts{}, 3, 10 * m, BriefAllowed},
		{"rejected just now", BriefAttempts{Failed: 1, LastAt: now}, 3, 10 * m, BriefBackingOff},
		{"rejected inside the backoff", BriefAttempts{Failed: 1, LastAt: now.Add(-9 * m)}, 3, 10 * m, BriefBackingOff},
		{"backoff boundary is allowed", BriefAttempts{Failed: 1, LastAt: now.Add(-10 * m)}, 3, 10 * m, BriefAllowed},
		{"last attempt below the cap", BriefAttempts{Failed: 2, LastAt: now.Add(-time.Hour)}, 3, 10 * m, BriefAllowed},
		{"at the cap", BriefAttempts{Failed: 3, LastAt: now.Add(-time.Hour)}, 3, 10 * m, BriefCapped},
		{"cap wins over an elapsed backoff", BriefAttempts{Failed: 5, LastAt: now.Add(-24 * time.Hour)}, 3, 10 * m, BriefCapped},
		{"cap wins inside the backoff", BriefAttempts{Failed: 3, LastAt: now}, 3, 10 * m, BriefCapped},
		{"no backoff configured", BriefAttempts{Failed: 1, LastAt: now}, 3, 0, BriefAllowed},
		{"a record from a later clock keeps backing off", BriefAttempts{Failed: 1, LastAt: now.Add(time.Hour)}, 3, 10 * m, BriefBackingOff},
		{"cap of one allows a single brief", BriefAttempts{Failed: 1, LastAt: now.Add(-time.Hour)}, 1, 10 * m, BriefCapped},
	}
	for _, tc := range cases {
		if got := BriefGate(tc.a, now, tc.max, tc.backoff); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
	if got := BriefRejected(BriefAttempts{Failed: 1, LastAt: now.Add(-time.Hour)}, now); got != (BriefAttempts{Failed: 2, LastAt: now}) {
		t.Errorf("rejected: %+v", got)
	}
}

// Each target has its own count and spacing. One-minute checks over an hour:
// a fallback that always rejects is offered the brief at 0m and 10m and never
// again, whatever happens to the other fallback.
func TestBriefGateIsPerTarget(t *testing.T) {
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const max, backoff = 2, 10 * time.Minute
	cases := []struct {
		name    string
		rejects map[string]func(minute int) bool
		want    map[string][]int // minutes at which each target is offered a brief
	}{
		{"both always reject", map[string]func(int) bool{
			"fallback-a": func(int) bool { return true }, "fallback-b": func(int) bool { return true },
		}, map[string][]int{"fallback-a": {0, 10}, "fallback-b": {0, 10}}},
		{"the second accepts on its second offer", map[string]func(int) bool{
			"fallback-a": func(int) bool { return true }, "fallback-b": func(m int) bool { return m < 10 },
		}, map[string][]int{"fallback-a": {0, 10}, "fallback-b": {0, 10}}},
		{"the second accepts at once", map[string]func(int) bool{
			"fallback-a": func(int) bool { return true }, "fallback-b": func(int) bool { return false },
		}, map[string][]int{"fallback-a": {0}, "fallback-b": {0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempts := map[string]BriefAttempts{}
			offered := map[string][]int{}
			accepted := false
			for minute := 0; minute < 60 && !accepted; minute++ {
				now := start.Add(time.Duration(minute) * time.Minute)
				for _, id := range []string{"fallback-a", "fallback-b"} {
					if BriefGate(attempts[id], now, max, backoff) != BriefAllowed {
						continue
					}
					offered[id] = append(offered[id], minute)
					if !tc.rejects[id](minute) {
						accepted = true
						break
					}
					attempts[id] = BriefRejected(attempts[id], now)
				}
			}
			for id, want := range tc.want {
				got := offered[id]
				if len(got) != len(want) {
					t.Fatalf("%s offered at %v, want %v", id, got, want)
				}
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("%s offered at %v, want %v", id, got, want)
					}
				}
			}
		})
	}
}

func TestNewThreadGate(t *testing.T) {
	const hour = time.Hour
	for _, tc := range []struct {
		name          string
		attempts, max int
		sinceLast     time.Duration
		any           bool
		want          NewThreadVerdict
	}{
		{"first thread", 0, 3, 0, false, NewThreadAllowed},
		{"below the cap, an hour after the last", 2, 3, hour, true, NewThreadAllowed},
		{"below the cap, just under an hour after the last", 2, 3, hour - time.Second, true, NewThreadTooSoon},
		{"below the cap, the last asked for by a later clock", 1, 3, -time.Minute, true, NewThreadTooSoon},
		{"at the cap", 3, 3, 24 * hour, true, NewThreadCapped},
		{"past the cap", 4, 3, 24 * hour, true, NewThreadCapped},
		{"the cap wins over spacing", 3, 3, 0, true, NewThreadCapped},
		{"cap of one, unused", 0, 1, 0, false, NewThreadAllowed},
		{"cap of one, used by an attempt of unknown outcome", 1, 1, 24 * hour, true, NewThreadCapped},
		{"no cap configured means no thread", 0, 0, 24 * hour, false, NewThreadCapped},
		{"negative cap means no thread", 0, -1, 24 * hour, false, NewThreadCapped},
	} {
		if got := NewThreadGate(tc.attempts, tc.max, tc.sinceLast, hour, tc.any); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}
