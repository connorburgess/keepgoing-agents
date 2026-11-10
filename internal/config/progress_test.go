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
