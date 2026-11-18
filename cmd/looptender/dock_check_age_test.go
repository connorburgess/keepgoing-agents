package main

import (
	"strings"
	"testing"
	"time"
)

// The "Loops and lanes" row put an orange "Stale" badge beside lines such as
// "Checked; sent nothing · 45 min ago" (design pass 2, a finding left after
// the usage-age change). As for usage readings, a check's age is said in
// plain words with no badge; what needs a person keeps its own state: a loop
// that is on and not checked for longer than --stale-after is "Not checked
// recently", and a paused loop's old check is not a warning.
func TestDockCheckAgeHasNoStaleBadge(t *testing.T) {
	old := func(st *statusOut) {
		at := dockNow.Add(-45 * time.Minute)
		st.State.CheckedAt, st.State.LastCheck.At = at, at
	}
	on := dockEvidence{controls: &clientControls{Enabled: true}, status: old}.input()
	paused := dockEvidence{controls: &clientControls{Enabled: true, Paused: true}, status: old}.input()
	paused.Entry.ID, paused.Entry.Loop = "loop_0123456789abcdef0123456789abcde3", "example-paused"
	paused.Client.Snapshot.Loop = "example-paused"
	r := buildDockReport([]dockInput{on, paused}, dockNow, 15*time.Second, 30*time.Minute, false, true, 0, true)

	l := dockLoopNamed(t, r, "example-loop")
	if l.State != "not_checked_recently" || l.Phrase != "Not checked recently" || l.LastCheck.Text != "Checked; sent nothing · 45 min ago" {
		t.Errorf("on: %q %q %q", l.State, l.Phrase, l.LastCheck.Text)
	}
	p := dockLoopNamed(t, r, "example-paused")
	if p.State != "paused" || p.LastCheck.Availability != "stale" || p.LastCheck.Text != "Checked; sent nothing · 45 min ago" {
		t.Errorf("paused: %q %+v", p.State, p.LastCheck)
	}
	for _, w := range r.Worth {
		if w.Loop == "example-paused" {
			t.Errorf("an old check of a paused loop is not worth a line: %+v", w)
		}
	}

	// The page draws no badge beside a check's age, on the row or the card.
	js := dockUIText(t, "dock.js")
	if strings.Contains(js, `tag("stale", "Stale")`) {
		t.Error(`dock.js still draws a "Stale" badge beside a check's age`)
	}
	if !strings.Contains(js, `summary.appendChild(el("span", "sub work-age", (l.lastCheck && l.lastCheck.text) || "Last check unknown"));`) {
		t.Error("the row's age must be plain text")
	}
}
