package window

import (
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

func TestInside(t *testing.T) {
	w := &config.Window{TZ: "UTC", Days: map[string][]string{
		"Mon": {"09:00", "17:00"},
		"Fri": {"22:00", "02:00"}, // runs past midnight into Saturday
	}}
	cases := []struct {
		when string
		want bool
	}{
		{"2026-11-09T08:59:00Z", false}, // Monday before start
		{"2026-11-09T09:00:00Z", true},
		{"2026-11-09T16:59:00Z", true},
		{"2026-11-09T17:00:00Z", false}, // end is exclusive
		{"2026-11-10T12:00:00Z", false}, // Tuesday not listed
		{"2026-11-13T23:30:00Z", true},  // Friday night
		{"2026-11-14T01:30:00Z", true},  // Saturday small hours, Friday's span
		{"2026-11-14T02:30:00Z", false},
	}
	for _, tc := range cases {
		now, _ := time.Parse(time.RFC3339, tc.when)
		got, _, err := Inside(w, now)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s: got %v want %v", tc.when, got, tc.want)
		}
	}
	if ok, _, _ := Inside(nil, time.Now()); !ok {
		t.Error("nil window should be always inside")
	}
}

func TestTimezone(t *testing.T) {
	w := &config.Window{TZ: "America/Los_Angeles", Days: map[string][]string{"Mon": {"09:00", "17:00"}}}
	now, _ := time.Parse(time.RFC3339, "2026-11-09T18:00:00Z") // 10:00 in Los Angeles
	if ok, _, _ := Inside(w, now); !ok {
		t.Error("expected inside in local time")
	}
}

// The two shapes a review found an hour off on a day the clocks change.
func TestDaylightSavingDays(t *testing.T) {
	for _, c := range []struct {
		tz, from, to, at string
		want             bool
	}{
		// New York sets clocks forward on Sun 10 Mar 2030; 22:30 EDT is inside 22:00-06:00.
		{"America/New_York", "22:00", "06:00", "2030-03-10T22:30:00-04:00", true},
		{"America/New_York", "22:00", "06:00", "2030-03-10T21:59:00-04:00", false},
		// London sets clocks forward on Sun 31 Mar 2030; 17:30 BST is past 09:00-17:00.
		{"Europe/London", "09:00", "17:00", "2030-03-31T17:30:00+01:00", false},
		{"Europe/London", "09:00", "17:00", "2030-03-31T16:59:00+01:00", true},
		{"Europe/London", "09:00", "17:00", "2030-03-31T08:59:00+01:00", false},
	} {
		w := &config.Window{TZ: c.tz, Days: everyDay(c.from, c.to)}
		now, _ := time.Parse(time.RFC3339, c.at)
		if got, _, err := Inside(w, now); err != nil || got != c.want {
			t.Errorf("%s %s-%s at %s: got %v (%v), want %v", c.tz, c.from, c.to, c.at, got, err, c.want)
		}
	}
}

// An edge in a skipped hour opens or closes the window when the clock jumps
// past it; an edge in a repeated hour means its first occurrence.
func TestEdgesInSkippedAndRepeatedHours(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	at := func(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }
	for _, c := range []struct {
		name     string
		from, to string
		at       string
		want     bool
	}{
		// Sun 10 Mar 2030: 01:59 EST is followed by 03:00 EDT (07:00Z).
		{"start 02:30 is not open before the jump", "02:30", "05:00", "2030-03-10T06:59:00Z", false},
		{"start 02:30 opens at the jump", "02:30", "05:00", "2030-03-10T07:00:00Z", true},
		{"end 02:30 is open before the jump", "22:00", "02:30", "2030-03-10T06:59:00Z", true},
		{"end 02:30 closes at the jump", "22:00", "02:30", "2030-03-10T07:00:00Z", false},
		{"a span inside the skipped hour is empty that day", "02:15", "02:45", "2030-03-10T07:00:00Z", false},
		// Sun 3 Nov 2030: 01:00-01:59 is shown twice, first EDT (05:00Z-05:59Z), then EST (06:00Z-06:59Z).
		{"start 01:30 opens at its first occurrence", "01:30", "04:00", "2030-11-03T05:30:00Z", true},
		{"start 01:30 is still open in the repeated hour", "01:30", "04:00", "2030-11-03T06:15:00Z", true},
		{"start 01:30 is not open before its first occurrence", "01:30", "04:00", "2030-11-03T05:29:00Z", false},
		{"end 01:30 closes at its first occurrence", "22:00", "01:30", "2030-11-03T05:30:00Z", false},
		{"end 01:30 stays closed in the repeated hour", "22:00", "01:30", "2030-11-03T06:15:00Z", false},
		{"end 01:30 is open before its first occurrence", "22:00", "01:30", "2030-11-03T05:29:00Z", true},
	} {
		w := &config.Window{TZ: "America/New_York", Days: everyDay(c.from, c.to)}
		if got, _, err := Inside(w, at(c.at)); err != nil || got != c.want {
			t.Errorf("%s (%s at %s local): got %v (%v)", c.name, c.at, at(c.at).In(ny).Format("15:04 MST"), got, err)
		}
	}
}

// Inside agrees, minute by minute, with the local wall clock across both 2030
// clock changes in New York, London and Lord Howe (a 30-minute change), for
// overnight and daytime windows and for windows with an edge in the skipped
// or the repeated time. The reference reads only the local clock as it runs:
// a span opens the first time the clock shows its start or later on its day,
// and closes the first time it shows its end or later.
func TestInsideFollowsTheLocalClockAcrossChanges(t *testing.T) {
	for _, z := range []struct {
		tz              string
		forward, back   time.Time // a local date of each change
		skipped, repeat string    // a time the clock skips, and one it shows twice
	}{
		{"America/New_York", date(2030, 3, 10), date(2030, 11, 3), "02:30", "01:30"},
		{"Europe/London", date(2030, 3, 31), date(2030, 10, 27), "01:30", "01:30"},
		{"Australia/Lord_Howe", date(2030, 10, 6), date(2030, 4, 7), "02:15", "01:45"},
	} {
		loc, err := time.LoadLocation(z.tz)
		if err != nil {
			t.Fatalf("%s: %v", z.tz, err)
		}
		windows := map[string]map[string][]string{
			"overnight 22:00-06:00":   everyDay("22:00", "06:00"),
			"daytime 09:00-17:00":     everyDay("09:00", "17:00"),
			"weekdays 09:00-17:00":    weekdays("09:00", "17:00"),
			"starts in skipped time":  everyDay(z.skipped, "05:00"),
			"ends in skipped time":    everyDay("22:00", z.skipped),
			"starts in repeated time": everyDay(z.repeat, "05:00"),
			"ends in repeated time":   everyDay("22:00", z.repeat),
			"overnight across both":   everyDay("00:30", "00:15"),
		}
		for _, day := range []time.Time{z.forward, z.back} {
			from := time.Date(day.Year(), day.Month(), day.Day()-1, 12, 0, 0, 0, loc)
			to := time.Date(day.Year(), day.Month(), day.Day()+1, 12, 0, 0, 0, loc)
			for name, days := range windows {
				w := &config.Window{TZ: z.tz, Days: days}
				want := reference(t, days, loc, from, to)
				wrong := 0
				for u := from; u.Before(to); u = u.Add(time.Minute) {
					got, _, err := Inside(w, u)
					if err != nil {
						t.Fatal(err)
					}
					if got != want[u.Unix()] {
						if wrong++; wrong <= 3 {
							t.Errorf("%s %s, %s: Inside %v at %s (%s local), the clock says %v",
								z.tz, day.Format("2 Jan"), name, got, u.UTC().Format(time.RFC3339), u.In(loc).Format("15:04 MST"), want[u.Unix()])
						}
					}
				}
			}
		}
	}
}

// reference walks the local clock minute by minute from two days before from
// and records, for each minute from from to to, whether a span is open.
func reference(t *testing.T, days map[string][]string, loc *time.Location, from, to time.Time) map[int64]bool {
	t.Helper()
	type span struct {
		open, close   time.Time // wall-clock readings, as if in UTC
		opened, ended bool
	}
	var spans []*span
	for d := -3; d <= 3; d++ {
		day := time.Date(from.Year(), from.Month(), from.Day()+d, 12, 0, 0, 0, loc)
		s, ok := days[day.Format("Mon")]
		if !ok {
			continue
		}
		sh, sm, _ := clock(s[0])
		eh, em, _ := clock(s[1])
		endDay := day.Day()
		if eh*60+em <= sh*60+sm {
			endDay++
		}
		spans = append(spans, &span{
			open:  time.Date(day.Year(), day.Month(), day.Day(), sh, sm, 0, 0, time.UTC),
			close: time.Date(day.Year(), day.Month(), endDay, eh, em, 0, 0, time.UTC),
		})
	}
	out := map[int64]bool{}
	for u := from.Add(-48 * time.Hour); u.Before(to); u = u.Add(time.Minute) {
		now := wall(u.In(loc))
		inside := false
		for _, s := range spans {
			if !s.opened && !now.Before(s.open) {
				s.opened = true
			}
			if !s.ended && !now.Before(s.close) {
				s.ended = true
			}
			inside = inside || (s.opened && !s.ended)
		}
		if !u.Before(from) {
			out[u.Unix()] = inside
		}
	}
	return out
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func everyDay(from, to string) map[string][]string {
	out := map[string][]string{}
	for _, d := range []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"} {
		out[d] = []string{from, to}
	}
	return out
}

func weekdays(from, to string) map[string][]string {
	out := everyDay(from, to)
	delete(out, "Sat")
	delete(out, "Sun")
	return out
}
