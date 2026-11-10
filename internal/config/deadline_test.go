package config

import (
	"strings"
	"testing"
	"time"
)

func TestDeadlineConfig(t *testing.T) {
	for _, value := range []string{"2030-01-01T00:00:00Z", "2030-01-01T07:00:00+07:00", "0001-01-01T00:00:00Z"} {
		cfg, err := Parse([]byte(valid + "  limits: {deadline: \"" + value + "\"}\n"))
		if err != nil {
			t.Fatal(err)
		}
		at, _ := time.Parse(time.RFC3339, value)
		if cfg.Loop.Limits.DeadlineReached(at.Add(-time.Nanosecond)) || !cfg.Loop.Limits.DeadlineReached(at) || !cfg.Loop.Limits.DeadlineReached(at.Add(time.Second)) {
			t.Fatalf("boundary: %s", value)
		}
	}
	for _, value := range []string{"tomorrow", "2030-01-01", "2030-01-01T00:00:00", "2030-99-01T00:00:00Z"} {
		_, err := Parse([]byte(valid + "  limits: {deadline: \"" + value + "\"}\n"))
		if err == nil || !strings.Contains(err.Error(), "loop.limits.deadline") {
			t.Fatalf("accepted %q: %v", value, err)
		}
	}
	if (Limits{}).DeadlineReached(time.Now()) {
		t.Fatal("absent deadline reached")
	}
}
