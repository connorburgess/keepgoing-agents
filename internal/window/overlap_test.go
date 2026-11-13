package window

import (
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

func TestOverlapCountsOnlyTimeInsideTheWindow(t *testing.T) {
	weekdays := &config.Window{TZ: "UTC", Days: map[string][]string{"Mon": {"09:00", "17:00"}, "Tue": {"09:00", "17:00"}, "Wed": {"22:00", "02:00"}}}
	monday := time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC) // a Monday
	for _, tc := range []struct {
		name     string
		w        *config.Window
		from, to time.Time
		want     time.Duration
	}{
		{"no window: all of it", nil, monday, monday.Add(5 * time.Hour), 5 * time.Hour},
		{"inside one span", weekdays, monday.Add(10 * time.Hour), monday.Add(12 * time.Hour), 2 * time.Hour},
		{"overnight between two spans", weekdays, monday.Add(16 * time.Hour), monday.Add(24*time.Hour + 10*time.Hour), 2 * time.Hour},
		{"a span past midnight", weekdays, monday.Add(2*24*time.Hour + 21*time.Hour), monday.Add(3*24*time.Hour + 3*time.Hour), 4 * time.Hour},
		{"a weekend with no span", weekdays, monday.Add(-2 * 24 * time.Hour), monday.Add(9 * time.Hour), 0},
		{"backwards", weekdays, monday.Add(time.Hour), monday, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Overlap(tc.w, tc.from, tc.to)
			if err != nil || got != tc.want {
				t.Fatalf("%s, %v; want %s", got, err, tc.want)
			}
		})
	}
}
