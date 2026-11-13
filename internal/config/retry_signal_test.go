package config

import (
	"strings"
	"testing"
	"time"
)

func TestBBRetrySignalTTL(t *testing.T) {
	cfg, err := Parse([]byte(valid))
	if err != nil || cfg.Loop.BB.RetrySignalTTL.Duration != 0 {
		t.Fatalf("unset must stay zero so the adapter default applies: %+v %v", cfg, err)
	}
	with := func(v string) string {
		return strings.Replace(valid, "    thread: thr_example123\n", "    thread: thr_example123\n    retry_signal_ttl: "+v+"\n", 1)
	}
	for value, want := range map[string]time.Duration{"1m": time.Minute, "45m": 45 * time.Minute, "2h": 2 * time.Hour} {
		cfg, err := Parse([]byte(with(value)))
		if err != nil || cfg.Loop.BB.RetrySignalTTL.Duration != want {
			t.Fatalf("%s: %+v %v", value, cfg, err)
		}
	}
	for _, value := range []string{"59s", "-5m", "soon"} {
		if _, err := Parse([]byte(with(value))); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
