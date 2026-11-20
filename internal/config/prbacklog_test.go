package config

import (
	"strings"
	"testing"
	"time"
)

func TestPRBacklogConfigOptInAndBounds(t *testing.T) {
	cfg, err := Parse([]byte(valid))
	if err != nil || cfg.Loop.PRBacklog != nil {
		t.Fatal(err)
	}
	cfg, err = Parse([]byte(valid + "  pr_backlog: {repo: example/project, max_open: 4}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.PRBacklog.RefreshInterval.Duration != 15*time.Minute || cfg.Loop.PRBacklog.MaxAge.Duration != time.Hour {
		t.Fatal(cfg.Loop.PRBacklog)
	}
	for _, extra := range []string{"repo: example/project", "repo: https://example.com/project, max_open: 4", "repo: example/project, max_open: 0", "repo: example/project, max_open: 5001", "repo: example/project, max_open: 4, refresh_interval: 1s", "repo: example/project, max_open: 4, refresh_interval: 2h, max_age: 1h", "repo: example/project, max_open: 4, held_label: x, superseded_label: x"} {
		if _, err := Parse([]byte(valid + "  pr_backlog: {" + extra + "}\n")); err == nil {
			t.Fatal("accepted", extra)
		} else if !strings.Contains(err.Error(), "pr_backlog") {
			t.Fatal(err)
		}
	}
}
