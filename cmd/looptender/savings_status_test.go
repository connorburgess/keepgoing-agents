package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/incident"
	"github.com/looptender/looptender/internal/state"
)

var savingsNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// savingsTick is a non-dry-run check of an enabled loop that succeeded.
func savingsTick(at time.Time, action, cause string) incident.Record {
	return incident.Record{Kind: "tick", CheckStartedAt: at, Action: action, Cause: cause, Outcome: "succeeded", TurnRetry: "none",
		ControlBefore: &incident.Controls{Enabled: true}, ControlAfter: &incident.Controls{Enabled: true}}
}

func savingsWith(r incident.Record, change func(*incident.Record)) incident.Record {
	change(&r)
	return r
}

// Every record kind, to the count and reason it adds, or to nothing.
func TestSavingsCountsFromTheirRecords(t *testing.T) {
	in := savingsNow.Add(-time.Hour)
	cases := []struct {
		name              string
		r                 incident.Record
		withheld, nothing map[string]int
		sends             map[string]int
		partial           []string
	}{
		{"idle check sent nothing", savingsTick(in, "none", "idle"), map[string]int{"idle": 1}, map[string]int{"idle": 1}, nil, nil},
		{"working session left alone", savingsTick(in, "none", "active"), map[string]int{"active": 1}, map[string]int{"active": 1}, nil, nil},
		{"held-back nudge", savingsTick(in, "nudge", "nudge_held"), map[string]int{"nudge_held": 1}, map[string]int{"nudge_held": 1}, nil, nil},
		{"limit wait", savingsTick(in, "none", "limit_wait"), map[string]int{"limit_wait": 1}, map[string]int{"limit_wait": 1}, nil, nil},
		{"paused: a hold, not withheld", savingsWith(savingsTick(in, "none", "paused"), func(r *incident.Record) { r.ControlBefore.Paused = true }),
			nil, map[string]int{"paused": 1}, nil, nil},
		{"awaiting a person", savingsTick(in, "await_user", "stall_limit"), nil, map[string]int{"stall_limit": 1}, nil, nil},
		{"hold cause with an enabled gate", savingsTick(in, "none", "completed"), nil, map[string]int{"completed": 1}, nil, nil},
		{"no control record", savingsWith(savingsTick(in, "none", "idle"), func(r *incident.Record) { r.ControlBefore = nil }), nil, map[string]int{"idle": 1}, nil, nil},
		{"wake sent", savingsTick(in, "wake", "idle"), nil, nil, map[string]int{"wake": 1}, nil},
		{"nudge sent", savingsTick(in, "nudge", "stall"), nil, nil, map[string]int{"nudge": 1}, nil},
		{"re-prompt sent", savingsTick(in, "reprompt", "other"), nil, nil, map[string]int{"reprompt": 1}, nil},
		{"failed turn retried by BB", savingsWith(savingsTick(in, "reprompt", "other"), func(r *incident.Record) { r.TurnRetry = "retried" }), nil, nil, map[string]int{"turn_retry": 1}, nil},
		{"handoff", savingsTick(in, "handoff", "limit_wait"), nil, nil, map[string]int{"handoff": 1}, nil},
		{"switch-back", savingsTick(in, "switch_back", "idle"), nil, nil, map[string]int{"switch_back": 1}, nil},
		{"a failed wake is neither", savingsWith(savingsTick(in, "wake", "idle"), func(r *incident.Record) { r.Outcome = "failed" }), nil, nil, nil, nil},
		{"a dry run executed nothing", savingsWith(savingsTick(in, "wake", "idle"), func(r *incident.Record) { r.DryRun, r.Outcome = true, "dry_run" }), nil, nil, nil, nil},
		{"before the period", savingsTick(time.Date(2026, 11, 9, 23, 59, 0, 0, time.UTC), "none", "idle"), nil, nil, nil, nil},
		{"first minute of the period", savingsTick(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), "none", "idle"), map[string]int{"idle": 1}, map[string]int{"idle": 1}, nil, nil},
		{"folded checks inside the period", savingsWith(savingsTick(in, "none", "idle"), func(r *incident.Record) {
			r.Collapsed = &incident.Collapsed{Count: 3, FirstAt: in.Add(-3 * time.Hour), LastAt: in.Add(-time.Hour)}
		}), map[string]int{"idle": 4}, map[string]int{"idle": 4}, nil, nil},
		{"folded checks that began before the period", savingsWith(savingsTick(in, "none", "idle"), func(r *incident.Record) {
			r.Collapsed = &incident.Collapsed{Count: 3, FirstAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), LastAt: in.Add(-time.Hour)}
		}), map[string]int{"idle": 1}, map[string]int{"idle": 1}, nil, []string{"collapsed_span"}},
		{"a control save is not a check", incident.Record{Kind: "control", ControlChange: &incident.ControlChange{Operation: "pause", SaveObservedAt: in}}, nil, nil, nil, nil},
	}
	reasons := func(c savingsCounts) map[string]int {
		m := map[string]int{}
		for _, r := range c.ByReason {
			m[r.Reason] = r.Count
		}
		return m
	}
	kinds := func(s savingsSends) map[string]int {
		m := map[string]int{}
		for _, k := range s.ByKind {
			m[k.Kind] = k.Count
		}
		return m
	}
	orEmpty := func(m map[string]int) map[string]int {
		if m == nil {
			return map[string]int{}
		}
		return m
	}
	for _, c := range cases {
		got := describeSavings(&incident.Journal{Records: []incident.Record{c.r}}, savingsSpaced{}, nil, contextStatus{Availability: "unavailable"}, savingsNow)
		if !reflect.DeepEqual(reasons(got.WithheldWakes), orEmpty(c.withheld)) || !reflect.DeepEqual(reasons(got.ChecksSentNothing), orEmpty(c.nothing)) || !reflect.DeepEqual(kinds(got.Sends), orEmpty(c.sends)) {
			t.Errorf("%s: withheld %v, nothing %v, sends %v", c.name, reasons(got.WithheldWakes), reasons(got.ChecksSentNothing), kinds(got.Sends))
		}
		if want := append([]string{}, c.partial...); !reflect.DeepEqual(got.History.PartialReasons, want) || got.History.Partial != (len(want) > 0) {
			t.Errorf("%s: partial %v %v", c.name, got.History.Partial, got.History.PartialReasons)
		}
	}
}

// The period's figures, through to the exact lines status prints.
func TestSavingsPhrasesFromTheirRecords(t *testing.T) {
	in := savingsNow.Add(-time.Hour)
	reading := func(used int64, at time.Time) state.ContextReading {
		return state.ContextReading{UsedTokens: used, WindowTokens: 200_000, Estimated: true, At: at}
	}
	var f state.File
	f.NoteContext("primary", reading(90_000, time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))) // outside the period
	f.NoteContext("primary", reading(20_000, time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)))
	f.NoteContext("primary", reading(60_000, in.Add(-2*time.Hour)))
	f.NoteContext("primary", reading(10_000, in.Add(-time.Minute)))
	journal := &incident.Journal{Records: []incident.Record{
		savingsTick(in.Add(-3*time.Hour), "wake", "idle"),
		savingsTick(in.Add(-2*time.Hour), "none", "active"),
		savingsTick(in.Add(-90*time.Minute), "none", "active"),
		savingsWith(savingsTick(in.Add(-80*time.Minute), "none", "paused"), func(r *incident.Record) { r.ControlBefore.Paused = true }),
		savingsTick(in.Add(-70*time.Minute), "nudge", "nudge_held"),
		{Kind: "control", ControlChange: &incident.ControlChange{Operation: sessionResetByTerminal, SaveObservedAt: in.Add(-30 * time.Minute)}},
		savingsTick(in.Add(-10*time.Minute), "nudge", "stall"),
	}}
	ctx := describeContext(f.Context, "primary", true, savingsNow)
	got := describeSavings(journal, savingsSpaced{}, f.Context, ctx, savingsNow)
	want := []string{
		"savings:   last 7 UTC days, measured from looptender's records; diagnostic only, no decision uses it",
		"  withheld wakes: 3 checks (while the loop was not held; compared with a timer that sends at every check): 2 active, 1 nudge_held",
		"  spaced wakes: unavailable; no record of spaced wakes is kept yet",
		"  checks that sent nothing: 4 checks (holds included): 2 active, 1 nudge_held, 1 paused",
		"  sends: 2 sends: 1 wake, 1 nudge",
		"  context now: 10,000 of 200,000 tokens (5%), estimated by BB, read 1 h 1 min ago",
		"  session resets: 2026-11-16T10:30:00Z by terminal, before 60,000 tokens, after 10,000 tokens",
		"  estimate: 30,000 to 180,000 tokens not spent on 3 withheld wakes, compared with a timer that sends at every check (withheld wakes times the lowest and highest context reading of the period, estimated by BB; not tokens billed)",
	}
	if !reflect.DeepEqual(got.Lines, want) {
		t.Fatalf("lines:\n%s\nwant:\n%s", strings.Join(got.Lines, "\n"), strings.Join(want, "\n"))
	}
	e := got.Estimate
	if e.Label != "estimate" || e.Availability != "recorded" || *e.WithheldWakes != 3 || *e.LowestReading != 10_000 || *e.HighestReading != 60_000 ||
		*e.LowTokens != 30_000 || *e.HighTokens != 180_000 || !e.Estimated || e.Baseline != savingsBaseline {
		t.Fatalf("estimate: %+v", e)
	}
	if got.Period.From != time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) || got.Period.Days != 7 || got.Version != 1 {
		t.Fatalf("period: %+v version %d", got.Period, got.Version)
	}
	if r := got.Resets.Items; len(r) != 1 || r[0].By != "terminal" || r[0].Before.UsedTokens != 60_000 || r[0].After.UsedTokens != 10_000 {
		t.Fatalf("resets: %+v", r)
	}
	if got.SpacedWakes.Availability != "unavailable" || got.SpacedWakes.Reason != "not_recorded" || got.SpacedWakes.Total != nil {
		t.Fatalf("spaced wakes: %+v", got.SpacedWakes)
	}
}

// What is missing reads as missing: never as zero, never borrowed.
func TestSavingsAbsentWhenNotRecorded(t *testing.T) {
	none := contextStatus{Availability: "unavailable", Reason: "not_recorded", Text: "unavailable; BB has not given a reading for this thread at a check yet"}

	unreadable := describeSavings(nil, savingsSpaced{}, nil, none, savingsNow)
	for name, c := range map[string]savingsCounts{"withheld": unreadable.WithheldWakes, "nothing": unreadable.ChecksSentNothing} {
		if c.Availability != "unavailable" || c.Reason != "record_unreadable" || c.Total != nil {
			t.Errorf("unreadable history, %s: %+v", name, c)
		}
	}
	if unreadable.Sends.Total != nil || unreadable.Resets.Availability != "unavailable" || unreadable.Estimate.Availability != "unavailable" || unreadable.Estimate.LowTokens != nil {
		t.Errorf("unreadable history: %+v %+v %+v", unreadable.Sends, unreadable.Resets, unreadable.Estimate)
	}

	// Withheld wakes, but the only reading is from before the period.
	var f state.File
	f.NoteContext("primary", state.ContextReading{UsedTokens: 50_000, WindowTokens: 200_000, Estimated: true, At: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
	j := &incident.Journal{Records: []incident.Record{savingsTick(savingsNow.Add(-time.Hour), "none", "idle")}}
	old := describeSavings(j, savingsSpaced{}, f.Context, none, savingsNow)
	if e := old.Estimate; e.Availability != "unavailable" || e.Reason != "no_readings" || e.LowTokens != nil || e.HighTokens != nil || e.WithheldWakes != nil {
		t.Fatalf("estimate without a reading in the period: %+v", e)
	}
	if *old.WithheldWakes.Total != 1 || old.Resets.Text != "none in the period" || len(old.Resets.Items) != 0 {
		t.Fatalf("counts: %+v resets %+v", old.WithheldWakes, old.Resets)
	}
	if old.ContextNow.Availability != "unavailable" || old.ContextNow.UsedTokens != nil {
		t.Fatalf("context now: %+v", old.ContextNow)
	}

	// Retention removed records from inside the period: counts are a floor.
	pruned := savingsNow.Add(-24 * time.Hour)
	j.Pruned.Routine = incident.TierPruned{Count: 5, NewestAt: &pruned}
	floor := describeSavings(j, savingsSpaced{}, nil, none, savingsNow)
	if !floor.History.Partial || !reflect.DeepEqual(floor.History.PartialReasons, []string{"history_pruned"}) || !strings.HasPrefix(floor.WithheldWakes.Text, "at least 1 check ") {
		t.Fatalf("pruned: %+v %q", floor.History, floor.WithheldWakes.Text)
	}
	j.Pruned.Routine = incident.TierPruned{}
	j.Dropped = 2
	if d := describeSavings(j, savingsSpaced{}, nil, none, savingsNow); !reflect.DeepEqual(d.History.PartialReasons, []string{"history_dropped"}) {
		t.Fatalf("dropped: %+v", d.History)
	}
}

// The figures never carry money, a headline, or a chat comparison, and the
// estimate always says it is one, with its baseline in the same line.
func TestSavingsWordsStayHonest(t *testing.T) {
	var f state.File
	f.NoteContext("primary", state.ContextReading{UsedTokens: 40_000, WindowTokens: 200_000, At: savingsNow.Add(-time.Hour)})
	j := &incident.Journal{Records: []incident.Record{savingsTick(savingsNow.Add(-time.Hour), "none", "idle")}}
	s := describeSavings(j, savingsSpaced{}, f.Context, describeContext(f.Context, "primary", true, savingsNow), savingsNow)
	b, _ := json.Marshal(s)
	all := strings.ToLower(string(b))
	for _, banned := range []string{"$", "usd", "cost", "money", "dollar", "chat", "saved", "headline"} {
		if strings.Contains(all, banned) {
			t.Errorf("savings mentions %q: %s", banned, all)
		}
	}
	last := s.Lines[len(s.Lines)-1]
	if !strings.HasPrefix(last, "  estimate: ") || !strings.Contains(last, savingsBaseline) || !strings.Contains(last, "reported by BB") || !strings.Contains(last, "not tokens billed") {
		t.Fatalf("estimate line: %q", last)
	}
}

// state.json idleSpacedDays, read without the writer's types: each shape to
// what savings shows.
func TestSavingsSpacedWakesFromTheirRecord(t *testing.T) {
	cases := []struct {
		name, json, avail, reason string
		total                     int
		text                      string
	}{
		{"absent", `{"target":"primary"}`, "unavailable", "not_recorded", 0, "unavailable; no record of spaced wakes is kept yet"},
		{"null", `{"idleSpacedDays":null}`, "unavailable", "not_recorded", 0, "unavailable; no record of spaced wakes is kept yet"},
		{"not a list", `{"idleSpacedDays":{"day":"2026-11-16"}}`, "unavailable", "record_unreadable", 0, "unavailable; the record of spaced wakes in state.json could not be read"},
		{"not a day", `{"idleSpacedDays":[{"day":"yesterday","counts":{"idle_unchanged":2}}]}`, "unavailable", "record_unreadable", 0, "unavailable; the record of spaced wakes in state.json could not be read"},
		{"negative count", `{"idleSpacedDays":[{"day":"2026-11-16","counts":{"idle_unchanged":-1}}]}`, "unavailable", "record_unreadable", 0, "unavailable; the record of spaced wakes in state.json could not be read"},
		{"a fractional count", `{"idleSpacedDays":[{"day":"2026-11-16","counts":{"idle_unchanged":1.5}}]}`, "unavailable", "record_unreadable", 0, "unavailable; the record of spaced wakes in state.json could not be read"},
		{"days in and out of the period", `{"idleSpacedDays":[{"day":"2026-11-09","counts":{"idle_unchanged":40}},{"day":"2026-11-10","counts":{"idle_unchanged":3}},{"day":"2026-11-16","counts":{"idle_unchanged":2,"new_code":1}}]}`,
			"recorded", "", 6, "6 checks (checks at which an idle wake was spaced; compared with a timer that sends at every check): 5 idle_unchanged, 1 new_code"},
		{"recorded, none in the period", `{"idleSpacedDays":[]}`, "recorded", "", 0, "0 checks (checks at which an idle wake was spaced; compared with a timer that sends at every check)"},
	}
	for _, c := range cases {
		got := describeSavings(&incident.Journal{}, parseSavingsSpaced([]byte(c.json)), nil, contextStatus{}, savingsNow).SpacedWakes
		total := 0
		if got.Total != nil {
			total = *got.Total
		}
		if got.Availability != c.avail || got.Reason != c.reason || total != c.total || got.Text != c.text || (c.avail != "recorded") != (got.Total == nil) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	got := describeSavings(&incident.Journal{}, parseSavingsSpaced([]byte(`{"idleSpacedDays":[{"day":"2026-11-16","counts":{"idle_unchanged":2}}]}`)), nil, contextStatus{}, savingsNow)
	if r := got.SpacedWakes.ByReason; len(r) != 1 || r[0].Text != "the session was idle and nothing had changed" {
		t.Fatalf("wording: %+v", r)
	}
	if *got.WithheldWakes.Total != 0 || got.Estimate.WithheldWakes != nil {
		t.Fatalf("spaced wakes were added to withheld wakes or the estimate: %+v %+v", got.WithheldWakes, got.Estimate)
	}
	if s := loadSavingsSpaced(t.TempDir()); s.Availability != "not_recorded" {
		t.Fatalf("no state.json: %+v", s)
	}
}
