package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

func TestGeneratedBrief(t *testing.T) {
	loop := config.Loop{Name: "example-loop", Prompt: "Ship the fictional feature.", Progress: config.Progress{Repositories: []string{"/fictional/example-app"}}}
	got, err := Brief(loop, Facts{Loop: "example-loop", At: time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC), From: "primary", To: "codex-main", Reason: "rate_limit"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"example-loop" loop`, "primary stopped with a rate_limit error at 2026-11-09 03:00 UTC", "(codex-main)", "- /fictional/example-app", "Ship the fictional feature."} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q:\n%s", want, got)
		}
	}
}

func TestHandoffFileWins(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "HANDOFF.md")
	loop := config.Loop{Prompt: "goal", HandoffFile: p}
	if got, _ := Brief(loop, Facts{}); !strings.Contains(got, "goal") {
		t.Error("missing handoff file should fall back to the generated brief")
	}
	os.WriteFile(p, []byte("Custom brief for the fictional loop.\n"), 0o600)
	if got, _ := Brief(loop, Facts{}); got != "Custom brief for the fictional loop." {
		t.Errorf("got %q", got)
	}
}
