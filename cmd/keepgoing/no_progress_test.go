package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func npConfig(sends, errors *int) *config.Config {
	cfg := &config.Config{}
	cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
	cfg.Loop.Progress.FlagAfterSends, cfg.Loop.Progress.FlagAfterErrors = sends, errors
	return cfg
}

func npInt(n int) *int { return &n }

var npSince = dockNow.Add(-3 * time.Hour)

func npState() state.File { return state.File{Fingerprint: "fixture", LastProgressAt: npSince} }

func npTick(at time.Time, action, outcome, observed string) incident.Record {
	on := &incident.Controls{Enabled: true}
	return incident.Record{Kind: "tick", CheckStartedAt: at, Action: action, Outcome: outcome, Observation: incident.Observation{State: observed}, ControlBefore: on, ControlAfter: on}
}

func npSends(n int, from time.Time) []incident.Record {
	out := []incident.Record{}
	for i := range n {
		out = append(out, npTick(from.Add(time.Duration(i+1)*10*time.Minute), "wake", "succeeded", "idle"))
	}
	return out
}

// Each record the flag can be given, mapped to the exact words it shows.
func TestNoProgressFollowsTheRecords(t *testing.T) {
	ctl := control.File{Enabled: true}
	for _, c := range []struct {
		name          string
		cfg           *config.Config
		st            state.File
		j             *incident.Journal
		ctl           control.File
		availability  string
		text          string
		sends, errors string // "count/atLeast/raised"
	}{
		{"no progress source", &config.Config{}, npState(), &incident.Journal{}, ctl, "not_available", "Not available: no progress source is configured.", "", ""},
		{"no check has fingerprinted yet", npConfig(nil, nil), state.File{}, &incident.Journal{}, ctl, "not_available", "Not available: no check has recorded its progress repositories yet.", "", ""},
		{"history unreadable", npConfig(nil, nil), npState(), nil, ctl, "not_available", "Not available: its history could not be read.", "", ""},
		{"below the default thresholds", npConfig(nil, nil), npState(), &incident.Journal{Records: npSends(5, npSince)}, ctl, "clear",
			"Clear: 5 sends and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "5/false/false", "0/false/false"},
		{"six sends raise it by default", npConfig(nil, nil), npState(), &incident.Journal{Records: npSends(6, npSince)}, ctl, "flagged",
			"No progress: 6 sends with no recorded change to its repositories in 3 h.", "6/false/true", "0/false/false"},
		// Twenty sends ten minutes apart, the last three after the change.
		{"sends before the last change do not count", npConfig(nil, nil), npState(), &incident.Journal{Records: npSends(20, npSince.Add(-170*time.Minute))}, ctl, "clear",
			"Clear: 3 sends and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "3/false/false", "0/false/false"},
		{"failed sends and dry runs are not sends", npConfig(npInt(1), nil), npState(), &incident.Journal{Records: []incident.Record{
			npTick(npSince.Add(time.Minute), "wake", "failed", "idle"), npTick(npSince.Add(2*time.Minute), "nudge", "dry_run", "idle")}}, ctl, "clear",
			"Clear: 0 sends and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "0/false/false", "0/false/false"},
		{"a dry run recorded as succeeded is neither a send nor an error", npConfig(npInt(1), npInt(1)), npState(), &incident.Journal{Records: []incident.Record{func() incident.Record {
			r := npTick(npSince.Add(time.Minute), "reprompt", "succeeded", "error")
			r.DryRun = true
			return r
		}()}}, ctl, "clear",
			"Clear: 0 sends and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "0/false/false", "0/false/false"},
		{"errors and re-prompts raise it", npConfig(nil, nil), npState(), &incident.Journal{Records: []incident.Record{
			npTick(npSince.Add(time.Minute), "none", "succeeded", "error"), npTick(npSince.Add(2*time.Minute), "reprompt", "succeeded", "error"),
			npTick(npSince.Add(3*time.Minute), "none", "succeeded", "error"), npTick(npSince.Add(4*time.Minute), "reprompt", "succeeded", "error")}}, ctl, "flagged",
			"No progress: 4 checks that found an error or retried the turn with no recorded change to its repositories in 3 h.", "2/false/false", "4/false/true"},
		{"both raise it", npConfig(npInt(2), npInt(2)), npState(), &incident.Journal{Records: []incident.Record{
			npTick(npSince.Add(time.Minute), "reprompt", "succeeded", "error"), npTick(npSince.Add(2*time.Minute), "reprompt", "succeeded", "error")}}, ctl, "flagged",
			"No progress: 2 sends and 2 checks that found an error or retried the turn with no recorded change to its repositories in 3 h.", "2/false/true", "2/false/true"},
		{"both thresholds off", npConfig(npInt(0), npInt(0)), npState(), &incident.Journal{Records: npSends(50, npSince)}, ctl, "off",
			"Off: both of its no-progress thresholds are 0.", "50/false/false", "0/false/false"},
		{"sends pruned after the last change: a lower bound", npConfig(nil, nil), npState(), &incident.Journal{Records: npSends(6, npSince),
			Pruned: incident.Pruned{Activity: incident.TierPruned{Count: 3, NewestAt: dockPtr(npSince.Add(time.Minute))}}}, ctl, "flagged",
			"No progress: at least 6 sends with no recorded change to its repositories in 3 h.", "6/true/true", "0/true/false"},
		{"routine checks pruned: sends are still exact", npConfig(nil, nil), npState(), &incident.Journal{Records: npSends(2, npSince),
			Pruned: incident.Pruned{Routine: incident.TierPruned{Count: 50, NewestAt: dockPtr(npSince.Add(time.Minute))}}}, ctl, "clear",
			"Clear: 2 sends and at least 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "2/false/false", "0/true/false"},
		{"pruned only before the last change: exact", npConfig(nil, nil), npState(), &incident.Journal{Records: npSends(2, npSince),
			Pruned: incident.Pruned{Activity: incident.TierPruned{Count: 3, NewestAt: dockPtr(npSince.Add(-time.Minute))}}}, ctl, "clear",
			"Clear: 2 sends and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "2/false/false", "0/false/false"},
		{"a folded run after the last change counts each check", npConfig(nil, nil), npState(), &incident.Journal{Records: []incident.Record{func() incident.Record {
			r := npTick(npSince.Add(time.Hour), "none", "succeeded", "error")
			r.Collapsed = &incident.Collapsed{Count: 3, FirstAt: npSince.Add(10 * time.Minute), LastAt: npSince.Add(50 * time.Minute)}
			return r
		}()}}, ctl, "flagged",
			"No progress: 4 checks that found an error or retried the turn with no recorded change to its repositories in 3 h.", "0/false/false", "4/false/true"},
		{"a folded run that began before the last change: a lower bound", npConfig(nil, nil), npState(), &incident.Journal{Records: []incident.Record{func() incident.Record {
			r := npTick(npSince.Add(time.Hour), "none", "succeeded", "error")
			r.Collapsed = &incident.Collapsed{Count: 3, FirstAt: npSince.Add(-10 * time.Minute), LastAt: npSince.Add(50 * time.Minute)}
			return r
		}()}}, ctl, "clear",
			"Clear: 0 sends and at least 1 check with an error or retry in 3 h with no recorded change to its repositories.", "0/false/false", "1/true/false"},
	} {
		v := noProgressOf(c.cfg, c.st, c.ctl, c.j, dockNow)
		if v.Availability != c.availability || v.Text != c.text {
			t.Errorf("%s:\n got  %s %q\n want %s %q", c.name, v.Availability, v.Text, c.availability, c.text)
		}
		fact := func(f *noProgressFact) string {
			if f == nil {
				return ""
			}
			return strings.Join([]string{itoa(f.Count), btoa(f.AtLeast), btoa(f.Raised)}, "/")
		}
		if fact(v.Sends) != c.sends || fact(v.Errors) != c.errors {
			t.Errorf("%s: sends %s errors %s", c.name, fact(v.Sends), fact(v.Errors))
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func btoa(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// The facts, one sentence each, and the time held for a person, exact from
// the hold changes in the history and the control record now.
func TestNoProgressLinesAndHeldTime(t *testing.T) {
	off, held := &incident.Controls{Enabled: true}, &incident.Controls{Enabled: true, AwaitingUser: true}
	change := func(at time.Time, before, after *incident.Controls) incident.Record {
		return incident.Record{Kind: "control", CheckStartedAt: at, ControlBefore: before, ControlAfter: after, ControlChange: &incident.ControlChange{Operation: "await-user", SaveObservedAt: at}}
	}
	records := append(npSends(6, npSince), change(npSince.Add(30*time.Minute), off, held), change(npSince.Add(2*time.Hour+40*time.Minute), held, off))
	v := noProgressOf(npConfig(nil, nil), npState(), control.File{Enabled: true}, &incident.Journal{Records: records}, dockNow)
	want := []string{
		"No change to its progress repositories is recorded in the last 3 h.",
		"6 sends in that time (wakes, nudges and re-prompts); the flag is raised at 6.",
		"0 checks in that time found an error or retried the turn; the flag is raised at 4.",
		"Held for a person for 2 h 10 min of the last 3 h.",
	}
	if strings.Join(v.Lines, "\n") != strings.Join(want, "\n") || v.Held.Seconds != int64((2*time.Hour+10*time.Minute)/time.Second) || v.Held.AtLeast {
		t.Errorf("%q %+v", v.Lines, v.Held)
	}
	for _, c := range []struct {
		name    string
		records []incident.Record
		now     control.File
		pruned  incident.Pruned
		held    time.Duration
		atLeast bool
		line    string
	}{
		{"never held", nil, control.File{}, incident.Pruned{}, 0, false, "Held for a person for no time of the last 3 h."},
		{"held the whole time, no change recorded", nil, control.File{AwaitingUser: true}, incident.Pruned{}, 3 * time.Hour, false, "Held for a person for 3 h of the last 3 h."},
		{"held since before the last change, released", []incident.Record{change(npSince.Add(time.Hour), held, off)}, control.File{}, incident.Pruned{}, time.Hour, false, "Held for a person for 1 h of the last 3 h."},
		{"held now, since an hour ago", []incident.Record{change(npSince.Add(2*time.Hour), off, held)}, control.File{AwaitingUser: true}, incident.Pruned{}, time.Hour, false, "Held for a person for 1 h of the last 3 h."},
		{"hold changes pruned: a lower bound", []incident.Record{change(npSince.Add(2*time.Hour), off, held)}, control.File{AwaitingUser: true},
			incident.Pruned{Key: incident.TierPruned{Count: 1, NewestAt: dockPtr(npSince.Add(time.Hour))}}, time.Hour, true, "Held for a person for at least 1 h of the last 3 h."},
		{"an operation that did not change the hold", []incident.Record{change(npSince.Add(time.Hour), off, off)}, control.File{}, incident.Pruned{}, 0, false, "Held for a person for no time of the last 3 h."},
	} {
		v := noProgressOf(npConfig(nil, nil), npState(), c.now, &incident.Journal{Records: c.records, Pruned: c.pruned}, dockNow)
		if v.Held == nil || v.Held.Seconds != int64(c.held/time.Second) || v.Held.AtLeast != c.atLeast || v.Lines[3] != c.line || v.Held.WindowSeconds != int64(3*time.Hour/time.Second) {
			t.Errorf("%s: %+v %q", c.name, v.Held, v.Lines)
		}
	}
	// A threshold of 0 says it does not raise the flag.
	v = noProgressOf(npConfig(npInt(0), nil), npState(), control.File{}, &incident.Journal{}, dockNow)
	if v.Lines[1] != "0 sends in that time (wakes, nudges and re-prompts); this does not raise the flag." {
		t.Errorf("%q", v.Lines[1])
	}
}

// status and the panel show the same value, from the same function, and the
// flag is never a need: a flagged loop leaves "nothing needs you" standing.
func TestNoProgressIsTheSameEverywhereAndNeverANeed(t *testing.T) {
	d, root := dockFixtureReader(t)
	r := d.read(context.Background(), false)
	nightly := dockLoopNamed(t, r, "nightly-dependency-bumps")
	np := nightly.NoProgress
	if np == nil || np.Availability != "flagged" || np.Text != "No progress: 8 sends with no recorded change to its repositories in 30 h." ||
		!strings.HasSuffix(nightly.PickerState, " \u00B7 no progress") {
		t.Fatalf("%+v %q", np, nightly.PickerState)
	}
	if docs := dockLoopNamed(t, r, "docs-site-refresh"); docs.NoProgress == nil || docs.NoProgress.Availability != "clear" {
		t.Errorf("%+v", docs.NoProgress)
	}
	if pay := dockLoopNamed(t, r, "payments-reconciler"); pay.NoProgress == nil || pay.NoProgress.Text != "Not available: no progress source is configured." {
		t.Errorf("%+v", pay.NoProgress)
	}
	worth := ""
	for _, w := range r.Worth {
		if w.Loop == "nightly-dependency-bumps" {
			worth = w.Text.render(dockNow)
		}
	}
	if !strings.HasSuffix(worth, np.Text) {
		t.Errorf("worth knowing: %q", worth)
	}
	for _, n := range r.NeedsYou {
		if n.Loop == "nightly-dependency-bumps" {
			t.Error("a flagged loop is listed as needing a person")
		}
	}
	// status --json and the status text, for the same loop, read through
	// the same function.
	f, _ := registry.Load(filepath.Join(root, "registry"))
	var entry registry.Entry
	for _, e := range f.Entries {
		if e.Loop == "nightly-dependency-bumps" {
			entry = e
		}
	}
	cfg, ok := clientConfig(entry)
	if !ok {
		t.Fatal("config")
	}
	out, err := readStatus(context.Background(), cfg, dockNow, defaultStaleAfter, d.query)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(out.NoProgress)
	b, _ := json.Marshal(*np)
	if !bytes.Equal(a, b) {
		t.Errorf("status and the panel disagree:\n%s\n%s", a, b)
	}
	var w bytes.Buffer
	if code := status(env{context.Background(), &w, &w}, cfg, dockNow, false, defaultStaleAfter); code != exitOK || !strings.Contains(w.String(), "no progress: "+np.Text+"\n") {
		t.Errorf("%d %s", code, w.String())
	}
	w.Reset()
	if status(env{context.Background(), &w, &w}, cfg, dockNow, true, defaultStaleAfter); !strings.Contains(w.String(), `"noProgress": {`) {
		t.Errorf("%s", w.String())
	}
	// panel list carries the sentence for the menu, and no need.
	list := buildPanelList(r)
	for _, l := range list.Loops {
		if l.Name == "nightly-dependency-bumps" && (l.NoProgress != np.Text || l.NeedsYou) {
			t.Errorf("%+v", l)
		}
		if l.Name == "docs-site-refresh" && l.NoProgress != "" {
			t.Errorf("a clear flag in panel list: %+v", l)
		}
	}
	// A report where the only thing of note is a flagged loop is still all clear.
	in := dockEvidence{controls: &clientControls{Enabled: true}, status: func(s *statusOut) {
		s.NoProgress = noProgressView{Availability: "flagged", Text: "No progress: 6 sends with no recorded change to its repositories in 3 h.", Lines: []string{}}
	}}.input()
	rep := buildDockReport([]dockInput{in}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true)
	if !rep.AllClear || len(rep.NeedsYou) != 0 || rep.NeedsLine != "Nothing needs you" || len(rep.Worth) != 1 {
		t.Errorf("allClear %t needs %d %q worth %d", rep.AllClear, len(rep.NeedsYou), rep.NeedsLine, len(rep.Worth))
	}
}

// The thresholds' configuration: defaults, 0 for off, and the bounds.
func TestNoProgressThresholdsAreConfigured(t *testing.T) {
	var p config.Progress
	if p.SendsFlag() != 6 || p.ErrorsFlag() != 4 {
		t.Errorf("defaults %d %d", p.SendsFlag(), p.ErrorsFlag())
	}
	p.FlagAfterSends, p.FlagAfterErrors = npInt(0), npInt(10)
	if p.SendsFlag() != 0 || p.ErrorsFlag() != 10 {
		t.Errorf("%d %d", p.SendsFlag(), p.ErrorsFlag())
	}
}
