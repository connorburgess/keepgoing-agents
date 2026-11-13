package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func npConfig(sends, errors *config.FlagCount) *config.Config {
	cfg := &config.Config{}
	cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
	cfg.Loop.Progress.FlagAfterSends, cfg.Loop.Progress.FlagAfterErrors = sends, errors
	return cfg
}

func npInt(n int) *config.FlagCount { c := config.FlagCount(n); return &c }

var npSince = dockNow.Add(-3 * time.Hour)

func npState() state.File {
	return state.File{Fingerprint: "fixture", LastProgressAt: npSince, ProgressChangedAt: npSince}
}

var npOn = control.File{Enabled: true}

func npTick(at time.Time, action, outcome, observed string) incident.Record {
	on := &incident.Controls{Enabled: true}
	return incident.Record{Kind: "tick", CheckStartedAt: at, Action: action, Outcome: outcome, Observation: incident.Observation{State: observed}, TurnRetry: "none", ControlBefore: on, ControlAfter: on}
}

func npRetry(at time.Time, result string) incident.Record {
	r := npTick(at, "reprompt", "succeeded", "error")
	r.TurnRetry = result
	return r
}

// npGraph is a run graph whose attempts are wakes at the given times, each
// with the given outcome.
func npGraph(outcome rd.Outcome, times ...time.Time) *rungraph.Graph {
	g := &rungraph.Graph{}
	for i, at := range times {
		g.Attempts = append(g.Attempts, rungraph.Attempt{ID: fmt.Sprintf("attempt_%d", i), Action: "wake", RecordedAt: at, Outcome: outcome})
	}
	return g
}

// npRetryGraph is what the real writer leaves for turn retries: one
// attempt per retry, mode turn_retry, marked returned whatever the
// provider answered.
func npRetryGraph(times ...time.Time) *rungraph.Graph {
	g := npGraph(rd.AdapterReturned, times...)
	for i := range g.Attempts {
		g.Attempts[i].Action, g.Attempts[i].Mode = "reprompt", "turn_retry"
	}
	return g
}

func npEvery(n int, from time.Time) []time.Time {
	out := []time.Time{}
	for i := range n {
		out = append(out, from.Add(time.Duration(i+1)*10*time.Minute))
	}
	return out
}

func npMerge(gs ...*rungraph.Graph) *rungraph.Graph {
	out := &rungraph.Graph{}
	for _, g := range gs {
		out.Attempts = append(out.Attempts, g.Attempts...)
		out.Dropped.Attempts += g.Dropped.Attempts
	}
	return out
}

// Each record the flag can be given, mapped to the exact words it shows.
func TestNoProgressFollowsTheRecords(t *testing.T) {
	empty := &incident.Journal{}
	none := &rungraph.Graph{}
	baseline := npState()
	baseline.ProgressBaseline = true
	ahead := npState()
	ahead.ProgressChangedAt = dockNow.Add(2 * time.Hour)
	dropped := npGraph(rd.AdapterReturned, npEvery(2, dockNow.Add(-70*time.Minute))...)
	dropped.Dropped.Attempts = 30
	for _, c := range []struct {
		name             string
		cfg              *config.Config
		st               state.File
		ctl              control.File
		j                *incident.Journal
		g                *rungraph.Graph
		availability     string
		text             string
		messages, errors string // "count/atLeast/raised"
	}{
		{"no progress source", &config.Config{}, npState(), npOn, empty, none, "not_available", "Not available: no progress source is configured.", "", ""},
		{"a finished loop", npConfig(nil, nil), npState(), control.File{Enabled: true, Completed: true}, empty, npGraph(rd.AdapterReturned, npEvery(9, npSince)...), "not_shown", "Not shown: the loop is finished.", "", ""},
		{"a stopped loop", npConfig(nil, nil), npState(), control.File{Enabled: true, Stop: true}, empty, none, "not_shown", "Not shown: the loop is stopped.", "", ""},
		{"a loop that is off", npConfig(nil, nil), npState(), control.File{}, empty, none, "not_shown", "Not shown: the loop is off.", "", ""},
		{"no check has fingerprinted yet", npConfig(nil, nil), state.File{}, npOn, empty, none, "not_available", "Not available: no check has recorded its progress repositories yet.", "", ""},
		{"an older binary's state, with only the credited time", npConfig(nil, nil), state.File{Fingerprint: "x", LastProgressAt: npSince}, npOn, empty, none, "not_available", "Not available: no check has recorded its progress repositories yet.", "", ""},
		{"the progress time ahead of the clock", npConfig(nil, nil), ahead, npOn, empty, npGraph(rd.AdapterReturned, npEvery(6, dockNow.Add(-time.Hour))...), "not_available", "Not available: the progress time is ahead of this computer's clock.", "", ""},
		// A change at 10:00, sends from 10:10 to 11:30, the clock stepped back
		// to 11:00: nothing measured to now can be exact.
		{"sends dated after now (a clock stepped back)", npConfig(nil, nil), npState(), npOn, empty, npGraph(rd.AdapterReturned, npEvery(9, dockNow.Add(-40*time.Minute))...), "not_available",
			"Not available: some of its records are dated after this computer's clock.", "", ""},
		{"a pruned record dated after now", npConfig(nil, nil), npState(), npOn,
			&incident.Journal{Pruned: incident.Pruned{Routine: incident.TierPruned{Count: 5, NewestAt: dockPtr(dockNow.Add(2 * time.Hour))}}}, none, "not_available",
			"Not available: some of its records are dated after this computer's clock.", "", ""},
		{"history unreadable", npConfig(nil, nil), npState(), npOn, nil, none, "not_available", "Not available: its history could not be read.", "", ""},
		{"run graph unreadable", npConfig(nil, nil), npState(), npOn, empty, nil, "not_available", "Not available: its history could not be read.", "", ""},
		{"below the default thresholds", npConfig(nil, nil), npState(), npOn, empty, npGraph(rd.AdapterReturned, npEvery(5, npSince)...), "clear",
			"Clear: 5 messages and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "5/false/false", "0/false/false"},
		{"six messages raise it by default", npConfig(nil, nil), npState(), npOn, empty, npGraph(rd.AdapterReturned, npEvery(6, npSince)...), "flagged",
			"No progress: 6 messages with no recorded change to its repositories in 3 h.", "6/false/true", "0/false/false"},
		{"a first baseline says at least", npConfig(nil, nil), baseline, npOn, empty, npGraph(rd.AdapterReturned, npEvery(6, npSince)...), "flagged",
			"No progress: 6 messages with no recorded change to its repositories in at least 3 h.", "6/false/true", "0/false/false"},
		{"a send by the check that recorded the change counts", npConfig(nil, nil), npState(), npOn, empty, npGraph(rd.AdapterReturned, append([]time.Time{npSince}, npEvery(5, npSince)...)...), "flagged",
			"No progress: 6 messages with no recorded change to its repositories in 3 h.", "6/false/true", "0/false/false"},
		// Twenty sends ten minutes apart: three after the change and one at it.
		{"sends before the change do not count", npConfig(nil, nil), npState(), npOn, empty, npGraph(rd.AdapterReturned, npEvery(20, npSince.Add(-170*time.Minute))...), "clear",
			"Clear: 4 messages and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "4/false/false", "0/false/false"},
		{"sends the adapter refused are not sent", npConfig(nil, nil), npState(), npOn, empty, npGraph(rd.AdapterError, npEvery(9, npSince)...), "clear",
			"Clear: 0 messages and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "0/false/false", "0/false/false"},
		// The review's case: re-prompts as turn retries BB answered with
		// nothing to do sent nothing; they still found the session in error.
		// The graph has a returned turn_retry attempt for every one, as the
		// real writer leaves it; only the provider's answer says what was sent.
		{"re-prompts that sent nothing are not messages", npConfig(nil, npInt(0)), npState(), npOn, &incident.Journal{Records: []incident.Record{
			npRetry(npSince.Add(1*time.Minute), "already_queued"), npRetry(npSince.Add(2*time.Minute), "already_queued"), npRetry(npSince.Add(3*time.Minute), "not_in_error"),
			func() incident.Record { r := npRetry(npSince.Add(4*time.Minute), "refused"); r.Outcome = "failed"; return r }()}},
			npRetryGraph(npSince.Add(1*time.Minute), npSince.Add(2*time.Minute), npSince.Add(3*time.Minute), npSince.Add(4*time.Minute)), "clear",
			"Clear: 0 messages and 4 checks with an error or retry in 3 h with no recorded change to its repositories.", "0/false/false", "4/false/false"},
		// A retry with nothing to retry, or a provider without retry, falls
		// back to a prompt: one message, the prompt, not the retry as well.
		{"a fallback prompt counts once", npConfig(nil, npInt(0)), npState(), npOn, &incident.Journal{Records: []incident.Record{
			npRetry(npSince.Add(1*time.Minute), "nothing_to_retry"), npRetry(npSince.Add(2*time.Minute), "unsupported")}},
			npMerge(npRetryGraph(npSince.Add(1*time.Minute), npSince.Add(2*time.Minute)), npGraph(rd.AdapterReturned, npSince.Add(1*time.Minute), npSince.Add(2*time.Minute))), "clear",
			"Clear: 2 messages and 2 checks with an error or retry in 3 h with no recorded change to its repositories.", "2/false/false", "2/false/false"},
		{"turn retries the provider accepted are messages, once each", npConfig(npInt(2), nil), npState(), npOn, &incident.Journal{Records: []incident.Record{
			npRetry(npSince.Add(time.Minute), "retried"), npRetry(npSince.Add(2*time.Minute), "queued")}}, npRetryGraph(npSince.Add(time.Minute), npSince.Add(2*time.Minute)), "flagged",
			"No progress: 2 messages with no recorded change to its repositories in 3 h.", "2/false/true", "2/false/false"},
		{"an unconfirmed retry queue: a lower bound", npConfig(nil, nil), npState(), npOn, &incident.Journal{Records: []incident.Record{npRetry(npSince.Add(time.Minute), "queue_unconfirmed")}}, none, "not_enough_history",
			"Not enough history to tell: whether some messages were sent could not be confirmed.", "0/true/false", "1/false/false"},
		{"a send whose result was never saved: a lower bound", npConfig(nil, nil), npState(), npOn, empty, npMerge(npGraph(rd.AdapterReturned, npEvery(6, npSince)...), npGraph(rd.Intent, dockNow.Add(-time.Minute))), "flagged",
			"No progress: at least 6 messages with no recorded change to its repositories in 3 h.", "6/true/true", "0/false/false"},
		// A check recorded a wake, but its send attempt is not in the graph.
		{"a recorded send missing from the graph: a lower bound", npConfig(nil, nil), npState(), npOn, &incident.Journal{Records: []incident.Record{npTick(npSince.Add(time.Minute), "wake", "succeeded", "idle")}}, none, "not_enough_history",
			"Not enough history to tell: whether some messages were sent could not be confirmed.", "0/true/false", "0/false/false"},
		{"a window under a minute", npConfig(nil, nil), func() state.File { s := npState(); s.ProgressChangedAt = dockNow.Add(-20 * time.Second); return s }(), npOn, empty, none, "clear",
			"Clear: 0 messages and 0 checks with an error or retry in the last minute with no recorded change to its repositories.", "0/false/false", "0/false/false"},
		{"dry runs are neither", npConfig(npInt(1), npInt(1)), npState(), npOn, &incident.Journal{Records: []incident.Record{func() incident.Record {
			r := npRetry(npSince.Add(time.Minute), "retried")
			r.DryRun = true
			return r
		}()}}, none, "clear",
			"Clear: 0 messages and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "0/false/false", "0/false/false"},
		{"errors raise it", npConfig(nil, nil), npState(), npOn, &incident.Journal{Records: []incident.Record{
			npTick(npSince.Add(time.Minute), "none", "succeeded", "error"), npTick(npSince.Add(2*time.Minute), "none", "succeeded", "error"),
			npTick(npSince.Add(3*time.Minute), "none", "succeeded", "error"), npTick(npSince.Add(4*time.Minute), "none", "succeeded", "error")}}, none, "flagged",
			"No progress: 4 checks that found an error or retried the turn with no recorded change to its repositories in 3 h.", "0/false/false", "4/false/true"},
		{"both thresholds off", npConfig(npInt(0), npInt(0)), npState(), npOn, empty, npGraph(rd.AdapterReturned, npEvery(15, npSince)...), "off",
			"Off: both of its no-progress thresholds are 0.", "15/false/false", "0/false/false"},
		// The review's case: sends pruned from the graph after the change.
		{"sends dropped from the run graph: not enough history", npConfig(nil, nil), npState(), npOn, empty, dropped, "not_enough_history",
			"Not enough history to tell: the retained history covers only the last 1 h.", "2/true/false", "0/false/false"},
		{"enough sends retained to raise it anyway", npConfig(npInt(2), nil), npState(), npOn, empty, dropped, "flagged",
			"No progress: at least 2 messages with no recorded change to its repositories in 3 h.", "2/true/true", "0/false/false"},
		{"routine checks pruned after the change: errors are a lower bound", npConfig(nil, nil), npState(), npOn,
			&incident.Journal{Pruned: incident.Pruned{Routine: incident.TierPruned{Count: 50, NewestAt: dockPtr(dockNow.Add(-30 * time.Minute))}}}, npGraph(rd.AdapterReturned, npEvery(2, npSince)...), "not_enough_history",
			"Not enough history to tell: the retained history covers only the last 30 min.", "2/false/false", "0/true/false"},
		{"pruned only before the change: exact", npConfig(nil, nil), npState(), npOn,
			&incident.Journal{Pruned: incident.Pruned{Routine: incident.TierPruned{Count: 50, NewestAt: dockPtr(npSince.Add(-time.Minute))}}}, npGraph(rd.AdapterReturned, npEvery(2, npSince)...), "clear",
			"Clear: 2 messages and 0 checks with an error or retry in 3 h with no recorded change to its repositories.", "2/false/false", "0/false/false"},
		{"a folded run inside the window counts each check", npConfig(nil, nil), npState(), npOn, &incident.Journal{Records: []incident.Record{func() incident.Record {
			r := npTick(npSince.Add(time.Hour), "none", "succeeded", "error")
			r.Collapsed = &incident.Collapsed{Count: 3, FirstAt: npSince.Add(10 * time.Minute), LastAt: npSince.Add(50 * time.Minute)}
			return r
		}()}}, none, "flagged",
			"No progress: 4 checks that found an error or retried the turn with no recorded change to its repositories in 3 h.", "0/false/false", "4/false/true"},
	} {
		v := noProgressOf(c.cfg, c.st, c.ctl, c.j, c.g, dockNow)
		if v.Availability != c.availability || v.Text != c.text {
			t.Errorf("%s:\n got  %s %q\n want %s %q", c.name, v.Availability, v.Text, c.availability, c.text)
		}
		fact := func(f *noProgressFact) string {
			if f == nil {
				return ""
			}
			return fmt.Sprintf("%d/%t/%t", f.Count, f.AtLeast, f.Raised)
		}
		if fact(v.Messages) != c.messages || fact(v.Errors) != c.errors {
			t.Errorf("%s: messages %s errors %s", c.name, fact(v.Messages), fact(v.Errors))
		}
	}
}

// The facts, one sentence each, and the time held for a person, counted
// only over the time the hold changes in the history cover.
func TestNoProgressLinesAndHeldTime(t *testing.T) {
	off, held := &incident.Controls{Enabled: true}, &incident.Controls{Enabled: true, AwaitingUser: true}
	change := func(at time.Time, before, after *incident.Controls) incident.Record {
		return incident.Record{Kind: "control", CheckStartedAt: at, ControlBefore: before, ControlAfter: after, ControlChange: &incident.ControlChange{Operation: "await-user", SaveObservedAt: at}}
	}
	records := []incident.Record{change(npSince.Add(30*time.Minute), off, held), change(npSince.Add(2*time.Hour+40*time.Minute), held, off)}
	v := noProgressOf(npConfig(nil, nil), npState(), npOn, &incident.Journal{Records: records}, npGraph(rd.AdapterReturned, npEvery(6, npSince)...), dockNow)
	want := []string{
		"Its progress repositories last changed 3 h ago.",
		"6 messages sent since then (wakes, nudges, re-prompts, notices about failed lanes, and briefs sent on a move to another route or back); the flag is raised at 6.",
		"0 checks since then found an error or retried the turn; the flag is raised at 4.",
		"Held for a person for 2 h 10 min of the last 3 h.",
	}
	if strings.Join(v.Lines, "\n") != strings.Join(want, "\n") || v.Held.Seconds != int64((2*time.Hour+10*time.Minute)/time.Second) || v.Held.AtLeast {
		t.Errorf("%q %+v", v.Lines, v.Held)
	}
	baseline := npState()
	baseline.ProgressBaseline = true
	if v := noProgressOf(npConfig(nil, nil), baseline, npOn, &incident.Journal{}, &rungraph.Graph{}, dockNow); v.Lines[0] != "No change to its progress repositories in at least 3 h, since a check first recorded them." {
		t.Errorf("%q", v.Lines[0])
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
		{"never held", nil, npOn, incident.Pruned{}, 0, false, "Held for a person for no time of the last 3 h."},
		{"held throughout, no change recorded", nil, control.File{Enabled: true, AwaitingUser: true}, incident.Pruned{}, 3 * time.Hour, false, "Held for a person for 3 h of the last 3 h."},
		{"held since before the change, released", []incident.Record{change(npSince.Add(time.Hour), held, off)}, npOn, incident.Pruned{}, time.Hour, false, "Held for a person for 1 h of the last 3 h."},
		{"held now, since an hour ago", []incident.Record{change(npSince.Add(2*time.Hour), off, held)}, control.File{Enabled: true, AwaitingUser: true}, incident.Pruned{}, time.Hour, false, "Held for a person for 1 h of the last 3 h."},
		// The review's case: held for an hour, and both changes pruned. Only
		// the half hour after the newest pruned change is covered.
		{"hold changes pruned: only the covered time counts", nil, npOn,
			incident.Pruned{Key: incident.TierPruned{Count: 2, NewestAt: dockPtr(npSince.Add(2*time.Hour + 30*time.Minute))}}, 0, true,
			"Held for a person for no time of the last 30 min, which is as far back as its history covers."},
		{"a hold that began after the pruned changes", []incident.Record{change(npSince.Add(2*time.Hour+40*time.Minute), off, held)}, control.File{Enabled: true, AwaitingUser: true},
			incident.Pruned{Key: incident.TierPruned{Count: 2, NewestAt: dockPtr(npSince.Add(2*time.Hour + 30*time.Minute))}}, 20 * time.Minute, true,
			"Held for a person for 20 min of the last 30 min, which is as far back as its history covers."},
		{"an operation that did not change the hold", []incident.Record{change(npSince.Add(time.Hour), off, off)}, npOn, incident.Pruned{}, 0, false, "Held for a person for no time of the last 3 h."},
	} {
		v := noProgressOf(npConfig(nil, nil), npState(), c.now, &incident.Journal{Records: c.records, Pruned: c.pruned}, &rungraph.Graph{}, dockNow)
		if v.Held == nil || v.Held.Seconds != int64(c.held/time.Second) || v.Held.AtLeast != c.atLeast || v.Lines[3] != c.line {
			t.Errorf("%s: %+v %q", c.name, v.Held, v.Lines)
		}
	}
	// A threshold of 0 says it does not raise the flag.
	v = noProgressOf(npConfig(npInt(0), nil), npState(), npOn, &incident.Journal{}, &rungraph.Graph{}, dockNow)
	if v.Lines[1] != "0 messages sent since then (wakes, nudges, re-prompts, notices about failed lanes, and briefs sent on a move to another route or back); this does not raise the flag." {
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
	if np == nil || np.Availability != "flagged" || np.Text != "No progress: 8 messages with no recorded change to its repositories in 30 h." ||
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
	if code := status(env{context.Background(), &w, &w}, cfg, dockNow, false, defaultStaleAfter); code != exitOK || !strings.Contains(w.String(), "progress flag: "+np.Text+"\n") {
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
		s.NoProgress = noProgressView{Availability: "flagged", Text: "No progress: 6 messages with no recorded change to its repositories in 3 h.", Lines: []string{}}
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

// The review's case through the real run-graph writer: six sends after the
// change, then 256 sends the adapter refused. The graph keeps its newest
// attempts, so the six are dropped; the flag says it cannot tell rather
// than "clear".
func TestNoProgressSaysWhenTheGraphNoLongerCoversTheWindow(t *testing.T) {
	dir := t.TempDir()
	start := npSince.Add(-time.Minute)
	if err := rungraph.Update(dir, start, func(g *rungraph.Graph) (bool, error) {
		return g.Sync(rungraph.Boundary{Gate: rungraph.Gate{Enabled: true}, Operation: "start", At: start, NewRunID: "run_" + strings.Repeat("a", 32)})
	}); err != nil {
		t.Fatal(err)
	}
	send := func(i int, at time.Time, outcome rd.Outcome) {
		id := fmt.Sprintf("attempt_%032x", i+1)
		if err := rungraph.Update(dir, at, func(g *rungraph.Graph) (bool, error) {
			if err := g.Begin(rungraph.Attempt{ID: id, Target: state.Primary, Action: "wake", Mode: "auto", RecordedAt: at}); err != nil {
				return false, err
			}
			return g.Finish(id, outcome, at)
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 6 {
		send(i, npSince.Add(time.Duration(i+1)*time.Minute), rd.AdapterReturned)
	}
	for i := range 256 {
		send(6+i, npSince.Add(10*time.Minute+time.Duration(i)*30*time.Second), rd.AdapterError)
	}
	g, availability := rungraph.Load(dir)
	if availability != rungraph.Available || g.Dropped.Attempts == 0 {
		t.Fatalf("%s dropped %d", availability, g.Dropped.Attempts)
	}
	v := noProgressOf(npConfig(nil, nil), npState(), npOn, &incident.Journal{}, &g, dockNow)
	if v.Availability != "not_enough_history" || !strings.HasPrefix(v.Text, "Not enough history to tell: the retained history covers only the last ") || v.Messages.Count != 0 || !v.Messages.AtLeast {
		t.Fatalf("%s %q %+v", v.Availability, v.Text, v.Messages)
	}
}
