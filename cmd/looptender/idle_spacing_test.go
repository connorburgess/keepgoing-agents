//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/state"
)

func (f *fixture) tickAt(at string) map[string]any {
	f.t.Helper()
	code, out, errs := f.run("tick", "--now", at)
	if code != exitOK {
		f.t.Fatalf("tick exit %d: %s %s", code, out, errs)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		f.t.Fatalf("tick output: %v\n%s", err, out)
	}
	return v
}

// A loop that never sets loop.progress.idle_spacing is woken at every check,
// exactly as before, and keeps no spacing record.
func TestIdleSpacingOffWakesEveryCheck(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	for _, at := range []string{"2026-11-16T12:00:00Z", "2026-11-16T12:05:00Z", "2026-11-16T12:10:00Z"} {
		if r := f.tickAt(at); r["action"] != "wake" || r["reason"] != "idle" {
			t.Fatalf("%s: %v", at, r)
		}
	}
	if n := strings.Count(f.read("thr_primary.told"), "Continue the fictional loop."); n != 3 {
		t.Fatalf("told %d times", n)
	}
	cfg, _ := config.Load(f.cfg)
	st, _ := state.Load(cfg.Loop.StateDir)
	if st.IdleSpacing != nil || len(st.IdleSpacedDays) != 0 {
		t.Fatalf("record kept while off: %+v %+v", st.IdleSpacing, st.IdleSpacedDays)
	}
}

// The case from a live trial: a coordinator idle and waiting on CI, with no
// lanes running. Its previous wake's turn ended with nothing to do and no
// structured fact changed, so the next idle wake is spaced, the wait grows,
// status says when the next is due and why, a control change or a person's
// message wakes it at once, and the counts survive in state.json between
// checks. The fake thread's log records each wake's request and turn, as BB
// does; only typed events are read.
func TestIdleSpacingSpacesUnchangedIdleWakes(t *testing.T) {
	f := newFixture(t)
	raw, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.cfg, string(raw)+"  progress: {idle_spacing: {first: 10m, max: 2h}}\n", 0o600)
	if code, _, errs := f.run("enable"); code != exitOK {
		t.Fatal(errs)
	}
	var events []string
	turn := func(initiator string) {
		n := len(events)
		events = append(events,
			fmt.Sprintf(`{"seq":%d,"type":"client/turn/requested","data":{"initiator":%q}}`, n+1, initiator),
			fmt.Sprintf(`{"seq":%d,"type":"turn/started","data":{}}`, n+2),
			fmt.Sprintf(`{"seq":%d,"type":"turn/completed","data":{"status":"completed"}}`, n+3))
		write(t, filepath.Join(f.bbDir, "thr_primary.log"), "["+strings.Join(events, ",")+"]", 0o600)
	}
	steps := []struct{ at, action string }{
		{"2026-11-16T12:00:00Z", "wake"}, // nothing on record: not spaced
		{"2026-11-16T12:05:00Z", "none"}, // one unchanged wake: waits 10m
		{"2026-11-16T12:10:00Z", "wake"},
		{"2026-11-16T12:20:00Z", "none"}, // two in a row: waits 20m
		{"2026-11-16T12:29:00Z", "none"},
	}
	for _, s := range steps {
		r := f.tickAt(s.at)
		if r["action"] != s.action {
			t.Fatalf("%s: %v", s.at, r)
		}
		if s.action == "wake" {
			turn("user") // the wake's own request and its turn, which ends
		}
		if s.action == "none" && !strings.HasPrefix(r["reason"].(string), "idle; previous idle wakes changed nothing") {
			t.Fatalf("%s: %v", s.at, r)
		}
	}
	if n := strings.Count(f.read("thr_primary.told"), "Continue the fictional loop."); n != 2 {
		t.Fatalf("told %d times", n)
	}
	code, out, errs := f.run("status", "--json", "--now", "2026-11-16T12:29:30Z")
	var status statusOut
	if err := json.Unmarshal([]byte(out), &status); err != nil || code != exitOK {
		t.Fatalf("status: %d %s %s", code, out, errs)
	}
	s := status.IdleSpacing
	want := time.Date(2026, 11, 16, 12, 30, 0, 0, time.UTC)
	if !s.Enabled || s.Wait != "idle_unchanged" || s.NextIdleWakeAt == nil || !s.NextIdleWakeAt.Equal(want) || s.SpacedTotal != 3 || s.Unchanged != 1 ||
		len(s.Days) != 1 || s.Days[0].Day != "2026-11-16" || s.Days[0].Counts["idle_unchanged"] != 3 {
		t.Fatalf("status: %+v", s)
	}
	// The line is judged at the status read time, not the wall clock.
	if code, out, _ := f.run("status", "--now", "2026-11-16T12:29:30Z"); code != exitOK || !strings.Contains(out, "next idle wake due 2026-11-16T12:30:00Z (idle_unchanged") || strings.Contains(out, "now passed") {
		t.Fatalf("status text: %s", out)
	}
	if code, out, _ := f.run("status", "--now", "2026-11-16T12:30:30Z"); code != exitOK || !strings.Contains(out, "now passed, the next check decides") {
		t.Fatalf("status text after the due time: %s", out)
	}
	// Pause and resume are control changes: the next check wakes at once.
	f.run("pause")
	f.run("resume")
	if r := f.tickAt("2026-11-16T12:31:00Z"); r["action"] != "wake" {
		t.Fatalf("after resume: %v", r)
	}
	turn("user")
	cfg, _ := config.Load(f.cfg)
	st, _ := state.Load(cfg.Loop.StateDir)
	if st.IdleSpacing == nil || st.IdleSpacing.Unchanged != 0 || st.IdleSpacing.Wait != "" || st.IdleSpacedTotal() != 3 {
		t.Fatalf("after resume: %+v %+v", st.IdleSpacing, st.IdleSpacedDays)
	}
	// A spaced check sends nothing, so it never spends max_sends.
	if st.SendsRecorded != 3 {
		t.Fatalf("sends recorded %d", st.SendsRecorded)
	}
	if r := f.tickAt("2026-11-16T12:36:00Z"); r["action"] != "none" {
		t.Fatalf("after the resumed wake's turn: %v", r)
	}
	// A person's message, which looptender did not send, starts the spacing
	// over: the next check wakes at once.
	turn("user")
	if r := f.tickAt("2026-11-16T12:37:00Z"); r["action"] != "wake" {
		t.Fatalf("after a person's message: %v", r)
	}
}
