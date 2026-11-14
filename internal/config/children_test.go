package config

import (
	"strings"
	"testing"
)

// Lane recovery is off unless bb.children.retry_errored says otherwise, and
// its limits have defaults and bounds.
func TestBBChildrenLaneRecovery(t *testing.T) {
	cfg, err := Parse([]byte(valid))
	if err != nil || cfg.Loop.BB.Children != nil || cfg.Loop.BB.RetryErroredLanes() {
		t.Fatalf("lane recovery must be off by default: %+v %v", cfg.Loop.BB, err)
	}
	var none *BB
	if none.RetryErroredLanes() || none.LanesPerCheck() != DefaultLanesPerCheck || none.LanesPerEpisode() != DefaultLanesPerEpisode {
		t.Fatal("a target without BB settings must read as off, with the defaults")
	}
	with := func(children string) string {
		return strings.Replace(valid, "    thread: thr_example123\n", "    thread: thr_example123\n    children: "+children+"\n", 1)
	}
	for children, want := range map[string][3]int{
		"{}":                     {0, DefaultLanesPerCheck, DefaultLanesPerEpisode},
		"{max_per_check: 9}":     {0, 9, DefaultLanesPerEpisode}, // limits alone do not switch it on
		"{retry_errored: false}": {0, DefaultLanesPerCheck, DefaultLanesPerEpisode},
		"{retry_errored: true}":  {1, 5, 20},
		"{retry_errored: true, max_per_check: 0, max_per_episode: 0}":    {1, 5, 20},
		"{retry_errored: true, max_per_check: 1, max_per_episode: 1}":    {1, 1, 1},
		"{retry_errored: true, max_per_check: 20, max_per_episode: 100}": {1, 20, 100},
	} {
		cfg, err := Parse([]byte(with(children)))
		if err != nil {
			t.Fatalf("%s: %v", children, err)
		}
		on := 0
		if cfg.Loop.BB.RetryErroredLanes() {
			on = 1
		}
		if got := [3]int{on, cfg.Loop.BB.LanesPerCheck(), cfg.Loop.BB.LanesPerEpisode()}; got != want {
			t.Errorf("%s: %v, want %v", children, got, want)
		}
	}
	for _, children := range []string{
		"{retry_errored: true, max_per_check: 21}", "{retry_errored: true, max_per_check: -1}",
		"{retry_errored: true, max_per_episode: 101}", "{retry_errored: true, max_per_episode: -1}",
		"{retry_errored: yes please}", "{retry: true}", "true",
	} {
		if _, err := Parse([]byte(with(children))); err == nil {
			t.Errorf("accepted children: %s", children)
		}
	}
}
