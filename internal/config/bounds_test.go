package config

import (
	"reflect"
	"testing"
	"time"
)

func TestConfiguredLimits(t *testing.T) {
	timeout := Duration{Duration: 2 * time.Hour}
	cases := []struct {
		name   string
		limits Limits
		want   []string
	}{
		{"none", Limits{}, []string{}},
		{"zero values are off", Limits{MaxSends: 0, NoProgressTimeout: Duration{}, Deadline: ""}, []string{}},
		{"deadline", Limits{Deadline: "2030-01-01T18:00:00Z"}, []string{"deadline"}},
		// A past deadline is still a configured bound; reaching it is a separate fact.
		{"past deadline", Limits{Deadline: "2000-01-01T00:00:00Z"}, []string{"deadline"}},
		{"max_sends", Limits{MaxSends: 1}, []string{"max_sends"}},
		{"no_progress_timeout", Limits{NoProgressTimeout: timeout}, []string{"no_progress_timeout"}},
		{"all, in decision order", Limits{NoProgressTimeout: timeout, MaxSends: 20, Deadline: "2030-01-01T18:00:00Z"},
			[]string{"deadline", "max_sends", "no_progress_timeout"}},
	}
	for _, tc := range cases {
		if got := tc.limits.Configured(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
