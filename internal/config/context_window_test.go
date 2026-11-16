package config

import (
	"strings"
	"testing"
)

// The context-threshold reset has no default: a loop without the key has
// none, and a set key is read as written.
func TestContextWindowOffByDefault(t *testing.T) {
	cfg, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.ContextWindowResetAt() != nil {
		t.Fatal("a threshold is set without being configured")
	}
	cfg, err = Parse([]byte(valid + "  context_window:\n    reset_at: 0.8\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v := cfg.Loop.ContextWindowResetAt(); v == nil || *v != 0.8 {
		t.Fatalf("reset_at = %v", v)
	}
}

// It refuses an out-of-range value, an empty block, and a loop whose agent
// reports no reading, naming the rule.
func TestContextWindowRejects(t *testing.T) {
	headless := strings.Replace(strings.Replace(valid, "adapter: bb", "adapter: claude", 1), "  bb:\n    thread: thr_example123\n", "  claude: {workdir: /fictional/example-app, model: sonnet}\n", 1)
	for name, c := range map[string]struct{ yaml, want string }{
		"empty block":   {valid + "  context_window: {}\n", "loop.context_window.reset_at is required"},
		"too low":       {valid + "  context_window:\n    reset_at: 0.4\n", "must be between 0.50 and 0.95"},
		"too high":      {valid + "  context_window:\n    reset_at: 0.96\n", "must be between 0.50 and 0.95"},
		"a percentage":  {valid + "  context_window:\n    reset_at: 80\n", "must be between 0.50 and 0.95"},
		"not a number":  {valid + "  context_window:\n    reset_at: .nan\n", "must be between 0.50 and 0.95"},
		"unknown field": {valid + "  context_window:\n    reset_at: 0.8\n    compact: true\n", "compact"},
		"not BB":        {headless + "  context_window:\n    reset_at: 0.8\n", "needs a loop on a BB thread (rule: context_window_reading)"},
	} {
		_, err := Parse([]byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, v := range []string{"0.5", "0.95"} {
		if _, err := Parse([]byte(valid + "  context_window:\n    reset_at: " + v + "\n")); err != nil {
			t.Errorf("reset_at %s: %v", v, err)
		}
	}
}
