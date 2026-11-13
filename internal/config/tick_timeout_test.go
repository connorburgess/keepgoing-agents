package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTickTimeoutDefaultsAndBounds(t *testing.T) {
	cfg, err := Parse([]byte(valid))
	if err != nil || cfg.Loop.TickTimeout.Duration != 3*time.Minute || cfg.Loop.TickTimeoutWarning() != "" {
		t.Fatalf("%+v %v", cfg, err)
	}
	for value, want := range map[string]time.Duration{"30s": 30 * time.Second, "4m": 4 * time.Minute, "10m": 10 * time.Minute} {
		cfg, err := Parse([]byte(valid + "  tick_timeout: " + value + "\n"))
		if err != nil || cfg.Loop.TickTimeout.Duration != want {
			t.Fatalf("%s: %v", value, err)
		}
		if warned := cfg.Loop.TickTimeoutWarning() != ""; warned != (want > 4*time.Minute) {
			t.Fatalf("%s: warning=%t", value, warned)
		}
	}
	for _, value := range []string{"29s", "-1m", "soon"} {
		if _, err := Parse([]byte(valid + "  tick_timeout: " + value + "\n")); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

// tick_timeout must leave room for one whole scheduled probe. An omitted
// value grows to fit a long probe timeout, so existing files keep loading;
// an explicit value that is too short is rejected with both numbers named.
func TestTickTimeoutMustFitOneScheduledProbe(t *testing.T) {
	load := func(tick, probe string) (*Config, error) {
		dir := t.TempDir()
		main := valid + "  fallbacks: fallbacks.yaml\n"
		if tick != "" {
			main += "  tick_timeout: " + tick + "\n"
		}
		fb := validFallbacks
		if probe != "" {
			fb += "probe: {timeout: " + probe + "}\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "keepgoing.yaml"), []byte(main), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fallbacks.yaml"), []byte(fb), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(filepath.Join(dir, "keepgoing.yaml"))
	}
	for _, tc := range []struct {
		tick, probe string
		want        time.Duration
		ok          bool
	}{
		{"", "", 3 * time.Minute, true},                  // defaults: 90s probe + 90s
		{"", "3m", 4*time.Minute + 30*time.Second, true}, // grows to fit
		{"", "20s", 3 * time.Minute, true},
		{"3m", "90s", 3 * time.Minute, true},
		{"179s", "90s", 0, false},
		{"30s", "", 0, false},
		{"5m", "3m", 5 * time.Minute, true},
		{"4m", "3m", 0, false},
	} {
		cfg, err := load(tc.tick, tc.probe)
		if (err == nil) != tc.ok {
			t.Fatalf("tick=%q probe=%q: %v", tc.tick, tc.probe, err)
		}
		if err != nil {
			if !strings.Contains(err.Error(), "loop.tick_timeout") || !strings.Contains(err.Error(), "probe.timeout") {
				t.Fatalf("unhelpful error: %v", err)
			}
			continue
		}
		if cfg.Loop.TickTimeout.Duration != tc.want {
			t.Fatalf("tick=%q probe=%q: %s", tc.tick, tc.probe, cfg.Loop.TickTimeout.Duration)
		}
	}
}
