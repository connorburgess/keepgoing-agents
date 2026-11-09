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
