package config

import (
	"fmt"
	"testing"
)

func TestNoProgressConfig(t *testing.T) {
	for _, tc := range []struct {
		duration string
		repos    bool
		valid    bool
	}{{"0s", false, true}, {"1h", true, true}, {"1m", true, true}, {"1h", false, false}, {"-1h", true, false}, {"1s", true, false}} {
		text := valid + fmt.Sprintf("  limits: {no_progress_timeout: %s}\n", tc.duration)
		if tc.repos {
			text += "  progress: {repositories: [/example/repo]}\n"
		}
		_, err := Parse([]byte(text))
		if (err == nil) != tc.valid {
			t.Fatalf("%+v %v", tc, err)
		}
	}
}

func TestIdleSpacingConfig(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if first, max := c.Loop.Progress.IdleSpacingWaits(); first != 0 || max != 0 {
		t.Fatalf("default is not off: %v %v", first, max)
	}
	for _, tc := range []struct {
		block      string
		valid      bool
		first, max string
	}{
		{"{}", true, "10m0s", "2h0m0s"},
		{"{first: 5m}", true, "5m0s", "2h0m0s"},
		{"{first: 3h}", true, "3h0m0s", "3h0m0s"},
		{"{first: 15m, max: 1h}", true, "15m0s", "1h0m0s"},
		{"{first: 30s}", false, "", ""},
		{"{first: 1h, max: 30m}", false, "", ""},
		{"{max: 25h}", false, "", ""},
		{"{every: 5m}", false, "", ""},
	} {
		c, err := Parse([]byte(valid + "  progress: {idle_spacing: " + tc.block + "}\n"))
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.block, err)
		}
		if err != nil {
			continue
		}
		first, max := c.Loop.Progress.IdleSpacingWaits()
		if first.String() != tc.first || max.String() != tc.max {
			t.Fatalf("%s: %v %v", tc.block, first, max)
		}
	}
}
