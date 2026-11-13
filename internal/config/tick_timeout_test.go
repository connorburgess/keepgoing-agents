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
	for value, want := range map[string]time.Duration{"160s": 160 * time.Second, "3m15s": 195 * time.Second, "4m": 4 * time.Minute, "10m": 10 * time.Minute} {
		cfg, err := Parse([]byte(valid + "  tick_timeout: " + value + "\n"))
		if err != nil || cfg.Loop.TickTimeout.Duration != want {
			t.Fatalf("%s: %v", value, err)
		}
		// A check may take tick_timeout plus 90s; above 3m15s that no longer
		// leaves a margin under a five-minute scheduler timeout.
		if warned := cfg.Loop.TickTimeoutWarning() != ""; warned != (want > 195*time.Second) {
			t.Fatalf("%s: warning=%t", value, warned)
		}
	}
	for _, value := range []string{"159s", "30s", "-1m", "soon"} {
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
		{"160s", "", 0, false}, // valid by itself, too short beside a 90s probe
		{"160s", "70s", 160 * time.Second, true},
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

// The connectivity check is a mandatory step of a check, so it may use only
// what tick_timeout has beyond the minimum. A file with the smallest
// tick_timeout, valid before the check existed, still loads: the check then
// has no room and is not made, with a warning, instead of the file being
// rejected.
func TestSmallTickTimeoutStillLoadsAndShrinksTheNetworkCheck(t *testing.T) {
	for _, tc := range []struct {
		extra   string
		timeout time.Duration
		warn    bool
	}{
		{"", 8 * time.Second, false}, // default 3m
		{"  tick_timeout: 2m40s\n", 0, true},
		{"  tick_timeout: 164s\n", 4 * time.Second, true},
		{"  tick_timeout: 168s\n", 8 * time.Second, false},
		{"  tick_timeout: 2m40s\n  network: {check: \"off\"}\n", 0, false},
		{"  tick_timeout: 180s\n  network: {timeout: 30s}\n", 20 * time.Second, true},
		{"  tick_timeout: 190s\n  network: {timeout: 30s}\n", 30 * time.Second, false},
	} {
		cfg, err := Parse([]byte(valid + tc.extra))
		if err != nil {
			t.Fatalf("%q no longer loads: %v", tc.extra, err)
		}
		if got := cfg.Loop.NetworkCheckTimeout(); got != tc.timeout || (cfg.Loop.NetworkCheckWarning() != "") != tc.warn {
			t.Fatalf("%q: timeout %s warning %q", tc.extra, got, cfg.Loop.NetworkCheckWarning())
		}
	}
	for _, bad := range []string{
		"  network: {url: \"http://\"}\n", "  network: {url: \"https:///zen\"}\n", "  network: {url: \"example.com\"}\n",
		"  network: {command: [\"\"]}\n", "  network: {command: [\" \", \"x\"]}\n",
		"  network: {verify_after: 30s}\n",
	} {
		if _, err := Parse([]byte(valid + bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	cfg, err := Parse([]byte(valid + "  network: {url: \"https://example.com/health\", verify_after: 30m}\n"))
	if err != nil || cfg.Loop.Network.VerifyAfter.Duration != 30*time.Minute {
		t.Fatalf("%+v %v", cfg, err)
	}
}
