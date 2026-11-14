//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The real BB adapter cannot undo a thread update by sending through an old
// adapter: tell carries no model/effort. A lost update response must leave the
// wake withheld until a later successful repair restores the recorded route.
func TestTimingUncertainBBUpdateWaitsForRepair(t *testing.T) {
	f := newFixture(t)
	bin := filepath.Join(f.dir, "bin")
	script := strings.Replace(fakeBB, `"thread tell")`, `"thread update")
shift 3
model=""; effort=""
while [ "$#" -gt 0 ]; do
 case "$1" in
 --model) model="$2"; shift ;;
 --reasoning-level) effort="$2"; shift ;;
 esac
 shift
done
printf '%s/%s\n' "$model" "$effort" > "$d/thread-route"
if [ -f "$d/update-fails" ]; then echo '{"error":{"code":"response_lost"}}'; exit 1; fi
echo '{"ok":true}' ;;
"thread tell") cat "$d/thread-route" >> "$d/told-route";`, 1)
	write(t, filepath.Join(bin, "bb"), script, 0700)
	raw, _ := os.ReadFile(f.cfg)
	body := strings.Replace(string(raw), "  bb: {thread: thr_primary, ", "  bb: {thread: thr_primary, model: example-medium, reasoning: low, ", 1)
	write(t, f.cfg, body+"  network: {check: off}\n"+timingYAML+"    steps: [primary, example-large]\n", 0600)
	fb, _ := os.ReadFile(filepath.Join(f.dir, "fallbacks.yaml"))
	write(t, filepath.Join(f.dir, "fallbacks.yaml"), string(fb)+`  - id: example-large
    adapter: bb
    provider: claude-code
    group: claude-account-a
    bb: {thread: thr_primary, model: example-large, reasoning: high, cli: `+bin+`/bb}
`, 0600)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := probe.UsageIdentity(cfg.Loop.PrimaryCandidate())
	if err := os.MkdirAll(cfg.Loop.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cfg.Loop.StateDir, "health.json"), `{"candidates":{"primary":{"lastScheduled":"2026-11-09T12:00:00Z","usage":{"at":"2026-11-09T11:40:00Z","source":"claude_rate_limit_event","identity":"`+id+`","windows":[{"key":"seven_day","used":0.6,"resetsAt":"2026-11-09T14:00:00Z"}]}},"claude-other-account":{"lastScheduled":"2026-11-09T12:00:00Z"},"codex-main":{"lastScheduled":"2026-11-09T12:00:00Z"}}}`, 0600)
	write(t, filepath.Join(f.bbDir, "update-fails"), "yes", 0600)
	if code, _, errs := f.run("enable"); code != exitOK {
		t.Fatal(errs)
	}
	tick := func(at string) {
		t.Helper()
		code, out, errs := f.run("tick", "--now", at)
		if code != exitOK {
			t.Fatalf("%d %s %s", code, out, errs)
		}
	}
	tick("2026-11-09T12:00:00Z")
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if f.read("thread-route") != "example-large/high\n" || f.read("thr_primary.told") != "" || st.Target() != state.Primary || st.Timing == nil || st.Timing.Pending == nil || st.SendsRecorded != 0 {
		t.Fatalf("uncertain update sent a wake: actual=%q told=%q state=%+v", f.read("thread-route"), f.read("thr_primary.told"), st)
	}
	code, out, errs := f.run("status", "--json", "--now", "2026-11-09T12:01:00Z")
	var status statusOut
	if err := json.Unmarshal([]byte(out), &status); err != nil || code != exitOK || status.TimingWould != nil {
		t.Fatalf("uncertain route preview: %d %s %s", code, out, errs)
	}
	_, out, _ = f.run("status", "--now", "2026-11-09T12:01:00Z")
	if !strings.Contains(out, "wake waits for successful repair") {
		t.Fatalf("missing pending authority evidence: %s", out)
	}
	// Disable fresh steps so the later check tests repair and its ordinary wake.
	write(t, f.cfg, strings.Replace(body, "  network: {check: off}\n", "", 1)+"  network: {check: off}\n", 0600)
	tick("2026-11-09T12:10:00Z")
	st, _ = state.Load(cfg.Loop.StateDir)
	if f.read("thr_primary.told") != "" || st.Timing.Pending == nil || st.SendsRecorded != 0 {
		t.Fatal("failed repair released wake")
	}
	if err := os.Remove(filepath.Join(f.bbDir, "update-fails")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.bbDir, "calls"), "", 0600)
	tick("2026-11-09T12:20:00Z")
	st, _ = state.Load(cfg.Loop.StateDir)
	calls := f.read("calls")
	update := strings.Index(calls, "thread update thr_primary --json --model example-medium --reasoning-level low")
	tell := strings.Index(calls, "thread tell thr_primary")
	if update < 0 || tell <= update || f.read("told-route") != "example-medium/low\n" || st.Timing.Pending != nil || st.SendsRecorded != 1 {
		t.Fatalf("repair/wake ordering: %s state=%+v", calls, st)
	}
}
