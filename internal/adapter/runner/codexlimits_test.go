package runner

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLimitsFrom(t *testing.T) {
	parse := func(s string) rateLimitsResponse {
		var r rateLimitsResponse
		if err := json.Unmarshal([]byte(s), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	week := time.Unix(1794393286, 0).UTC()
	cases := []struct {
		name    string
		json    string
		blocked bool
		reset   time.Time
	}{
		{"allowed", `{"ordinaryUsageAllowed":true,"rateLimits":{"primary":{"usedPercent":40,"resetsAt":1794393286}}}`, false, time.Time{}},
		{"backend says not allowed", `{"ordinaryUsageAllowed":false,"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1794393286}}}`, true, week},
		{"reached type without the flag", `{"rateLimits":{"rateLimitReachedType":"workspace_member_usage_limit_reached","primary":{"usedPercent":20,"resetsAt":1794000000},"secondary":{"usedPercent":100,"resetsAt":1794393286}}}`, true, week},
		{"blocked without a full window uses the latest reset", `{"ordinaryUsageAllowed":false,"rateLimits":{"primary":{"usedPercent":90,"resetsAt":1794000000},"secondary":{"usedPercent":95,"resetsAt":1794393286}}}`, true, week},
		{"null flag and no reached type is not blocked", `{"ordinaryUsageAllowed":null,"rateLimits":{"primary":{"usedPercent":100,"resetsAt":1794393286}}}`, false, time.Time{}},
	}
	for _, tc := range cases {
		l := limitsFrom(parse(tc.json))
		if l.Blocked != tc.blocked || !l.ResetsAt.Equal(tc.reset) {
			t.Errorf("%s: %+v", tc.name, l)
		}
	}
}

func TestLimitsFromReportsWindowsWhetherOrNotBlocked(t *testing.T) {
	parse := func(s string) CodexLimits {
		var r rateLimitsResponse
		if err := json.Unmarshal([]byte(s), &r); err != nil {
			t.Fatal(err)
		}
		return limitsFrom(r)
	}
	reset := time.Unix(1900000000, 0).UTC()
	l := parse(`{"ordinaryUsageAllowed":true,"rateLimits":{"primary":{"usedPercent":40,"resetsAt":1900000000},"secondary":{"usedPercent":0}}}`)
	if l.Blocked || len(l.Windows) != 2 || l.Windows[0] != (CodexWindow{Name: "primary", UsedPercent: 40, ResetsAt: reset}) || l.Windows[1] != (CodexWindow{Name: "secondary"}) {
		t.Errorf("allowed: %+v", l)
	}
	// A window with no usage figure is not a reading, and is still usable for the reset.
	l = parse(`{"ordinaryUsageAllowed":false,"rateLimits":{"primary":{"resetsAt":1900000000},"secondary":null}}`)
	if !l.Blocked || len(l.Windows) != 0 || !l.ResetsAt.Equal(reset) {
		t.Errorf("no figure: %+v", l)
	}
	if l := parse(`{"ordinaryUsageAllowed":true}`); len(l.Windows) != 0 {
		t.Errorf("no rateLimits: %+v", l)
	}
}
