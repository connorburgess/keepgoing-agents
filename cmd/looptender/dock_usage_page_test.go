package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/looptender/looptender/internal/incident"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/state"
)

func usageInt(n int) *int              { return &n }
func usageI64(n int64) *int64          { return &n }
func usageAt(h int) time.Time          { return dockNow.Add(time.Duration(-h) * time.Hour) }
func usageTime(t time.Time) *time.Time { return &t }

// usageCounts builds a count by reason, largest first, as describeSavings does.
func usageCounts(rs ...savingsReason) savingsCounts {
	total := 0
	for _, r := range rs {
		total += r.Count
	}
	return savingsCounts{Availability: "recorded", Total: &total, ByReason: append([]savingsReason{}, rs...)}
}

func usageReason(code string, n int) savingsReason {
	return savingsReason{Reason: code, Count: n, Text: savingsReasonText(code)}
}

// usageStatus is a BB loop with a context reading and nothing counted.
func usageStatus() *statusOut {
	from := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	used, window := int64(48000), int64(200000)
	st := &statusOut{}
	st.ContextWindow = contextStatus{Version: 1, Availability: "recorded", UsedTokens: &used, WindowTokens: &window, ReadAt: usageTime(usageAt(1)), Age: "1 h ago",
		Text: contextFigures(used, window, false),
		Days: []state.ContextDay{
			{Day: "2026-11-01", Lowest: 1000, Highest: 2000, Last: 2000, WindowTokens: window, Count: 3},
			{Day: "2026-11-12", Lowest: 8000, Highest: 150000, Last: 150000, WindowTokens: window, Count: 5},
			{Day: "2026-11-13", Lowest: 12000, Highest: 48000, Last: 48000, WindowTokens: window, Count: 4, Estimated: true},
		},
		Recent: []state.ContextReading{{UsedTokens: 150000, WindowTokens: window, At: usageAt(30)}, {UsedTokens: 12000, WindowTokens: window, At: usageAt(20)}}}
	st.Savings = savingsStatus{Version: 1, Period: savingsPeriod{From: from, Days: 7},
		History:           savingsHistory{Availability: "recorded", PartialReasons: []string{}},
		WithheldWakes:     usageCounts(),
		SpacedWakes:       savingsCounts{Availability: "unavailable", Reason: "not_recorded", ByReason: []savingsReason{}},
		ChecksSentNothing: usageCounts(),
		Sends:             savingsSends{Availability: "recorded", Total: usageInt(0), ByKind: []savingsKind{}, ContextBlocks: usageInt(0), ContextBytes: usageI64(0)},
		ContextNow:        savingsContextOf(st.ContextWindow),
		Resets:            savingsResets{Availability: "recorded", Items: []savingsReset{}},
		Estimate: savingsEstimate{Label: "estimate", Availability: "recorded", Baseline: savingsBaseline, WithheldWakes: usageInt(0),
			LowestReading: usageI64(8000), HighestReading: usageI64(150000), LowTokens: usageI64(0), HighTokens: usageI64(0)},
	}
	return st
}

// usageBusy is a loop that did a lot: holds, withheld wakes, sends, a reset.
func usageBusy() *statusOut {
	st := usageStatus()
	s := &st.Savings
	s.ChecksSentNothing = usageCounts(usageReason("awaiting_user", 150), usageReason("child_busy", 23))
	s.WithheldWakes = usageCounts(usageReason("idle", 120), usageReason("child_busy", 30))
	s.Sends = savingsSends{Availability: "recorded", Total: usageInt(6), ByKind: []savingsKind{{"wake", 3}, {"nudge", 2}, {"turn_retry", 1}}, ContextBlocks: usageInt(4), ContextBytes: usageI64(1840)}
	before, after := st.ContextWindow.Recent[0], st.ContextWindow.Recent[1]
	s.Resets = savingsResets{Availability: "recorded", Items: []savingsReset{{At: time.Date(2026, 11, 12, 14, 3, 0, 0, time.UTC), By: "thread", Before: &before, After: &after}}}
	s.Estimate.WithheldWakes, s.Estimate.LowTokens, s.Estimate.HighTokens, s.Estimate.Estimated = usageInt(150), usageI64(150*8000), usageI64(150*150000), true
	return st
}

func TestDockUsageLoopSentences(t *testing.T) {
	cases := []struct {
		name   string
		status func() *statusOut
		want   []string // in Did, Window or Brief
		absent []string
		brief  int
	}{
		{"all zero", usageStatus, []string{
			"0 checks sent nothing this week.",
			"No wakes were withheld this week.",
			"No messages were sent this week, so looptender added no turns.",
			"Spaced wakes: unavailable; this loop's records hold no count of spaced wakes.",
			"Estimate: no tokens, since no wakes were withheld.",
			"No session resets this week.",
			"The latest context reading: 48,000 of 200,000 tokens (24%), reported by BB, read 1 h ago.",
			"This week's lowest reading was 8,000 tokens and the highest 150,000 tokens.",
		}, []string{"at least", "At least", "supervision context"}, 4},
		{"busy", usageBusy, []string{
			"173 checks sent nothing: 150 because the loop is held for a person, 23 because lanes are working.",
			"150 wakes withheld: 120 because the session was idle, 30 because lanes are working.",
			"6 messages sent: 3 wakes, 2 nudges, 1 retry. Each is a turn looptender added to the conversation.",
			"4 checks' messages carried the supervision context, 1,840 bytes in all.",
			"Estimate: 1,200,000 to 22,500,000 tokens not re-read, from 150 withheld wakes times the lowest (8,000 tokens) and the highest (150,000 tokens) context reading this week (some estimated by BB), against a plain timer that wakes the session at every check. It is not tokens billed.",
			"1 session reset: Thu 12 Nov 14:03 UTC, run in the thread, 150,000 tokens before and 12,000 after.",
			"The last 2 readings, oldest first: 150,000, 12,000 tokens.",
		}, nil, 4},
		{"pruned history", func() *statusOut {
			st := usageBusy()
			st.Savings.History = savingsHistory{Availability: "recorded", Partial: true, PartialReasons: []string{"history_pruned", "collapsed_span"}}
			return st
		}, []string{
			"At least 173 checks sent nothing",
			"At least 150 wakes withheld",
			"At least 6 messages sent",
			"Estimate: at least 1,200,000 to 22,500,000 tokens not re-read, from at least 150 withheld wakes",
			"Some of this week's history is missing (older records were pruned; a run of repeated checks began before the week), so these counts are at least the figures shown.",
		}, nil, 4},
		{"pruned and nothing found", func() *statusOut {
			st := usageStatus()
			st.Savings.History = savingsHistory{Availability: "recorded", Partial: true, PartialReasons: []string{"history_dropped"}}
			return st
		}, []string{
			"0 checks sent nothing in the history that remains for this week.",
			"No wakes were withheld in the history that remains for this week.",
			"No messages were sent in the history that remains for this week.",
			"(an older build removed records)",
		}, nil, 4},
		{"singular", func() *statusOut {
			st := usageStatus()
			s := &st.Savings
			s.ChecksSentNothing = usageCounts(usageReason("idle", 1))
			s.WithheldWakes = usageCounts(usageReason("idle", 1))
			s.Sends = savingsSends{Availability: "recorded", Total: usageInt(1), ByKind: []savingsKind{{"reprompt", 1}}, ContextBlocks: usageInt(1), ContextBytes: usageI64(1)}
			s.SpacedWakes = usageCounts(savingsReason{Reason: "idle_unchanged", Count: 1, Text: "the session was idle and nothing had changed"})
			s.Estimate.WithheldWakes, s.Estimate.LowTokens, s.Estimate.HighTokens = usageInt(1), usageI64(8000), usageI64(150000)
			return st
		}, []string{
			"1 check sent nothing, all because the session was idle.",
			"1 wake withheld, all because the session was idle.",
			"1 message sent: 1 re-prompt. It is a turn looptender added to the conversation.",
			"1 check's messages carried the supervision context, 1 bytes in all.",
			"1 wake spaced: at that check the session was idle and nothing had changed",
			"from 1 withheld wake times",
		}, []string{"1 checks", "1 wakes", "1 messages"}, 4},
		{"spaced recorded", func() *statusOut {
			st := usageStatus()
			st.Savings.SpacedWakes = usageCounts(savingsReason{Reason: "idle_unchanged", Count: 12})
			return st
		}, []string{"12 wakes spaced: at each of those checks the session was idle and nothing had changed, so looptender spaced its wakes out where a plain timer would have sent. They are not added to the withheld wakes or the estimate."}, nil, 4},
		{"spaced recorded zero", func() *statusOut {
			st := usageStatus()
			st.Savings.SpacedWakes = usageCounts()
			return st
		}, []string{"No wakes were spaced this week."}, nil, 4},
		{"spaced unreadable", func() *statusOut {
			st := usageStatus()
			st.Savings.SpacedWakes = savingsCounts{Availability: "unavailable", Reason: "record_unreadable", ByReason: []savingsReason{}}
			return st
		}, []string{"Spaced wakes: unavailable; the record of spaced wakes could not be read."}, nil, 4},
		{"not a BB thread", func() *statusOut {
			st := usageBusy()
			st.ContextWindow = contextStatus{Version: 1, Availability: "unavailable", Reason: "not_supported", Days: []state.ContextDay{}, Recent: []state.ContextReading{}}
			st.Savings.ContextNow = savingsContextOf(st.ContextWindow)
			st.Savings.Estimate = savingsEstimate{Label: "estimate", Availability: "unavailable", Reason: "no_readings", Baseline: savingsBaseline}
			return st
		}, []string{
			"No estimate: this loop's agent reports no context window size.",
			"This loop's agent reports no context window size, so there is no context reading for it.",
			"1 session reset",
		}, []string{"latest context reading", "lowest reading"}, 4},
		{"BB thread with no reading yet", func() *statusOut {
			st := usageStatus()
			st.ContextWindow = contextStatus{Version: 1, Availability: "unavailable", Reason: "not_recorded", Days: []state.ContextDay{}, Recent: []state.ContextReading{}}
			st.Savings.ContextNow = savingsContextOf(st.ContextWindow)
			st.Savings.Estimate = savingsEstimate{Label: "estimate", Availability: "unavailable", Reason: "no_readings", Baseline: savingsBaseline}
			return st
		}, []string{
			"No estimate: no context window reading was recorded this week.",
			"No context window reading has been recorded for this thread yet.",
		}, nil, 4},
		{"history unreadable", func() *statusOut {
			st := usageStatus()
			h := savingsHistory{Availability: "unavailable", Reason: "record_unreadable", PartialReasons: []string{}}
			st.Savings.History = h
			st.Savings.WithheldWakes = savingsCountsOf(nil, h, "")
			st.Savings.ChecksSentNothing = savingsCountsOf(nil, h, "")
			st.Savings.Resets = savingsResets{Availability: "unavailable", Reason: "record_unreadable", Items: []savingsReset{}}
			return st
		}, []string{"This loop's check history could not be read, so what looptender did this week cannot be counted."}, []string{"withheld:", "Estimate"}, 2},
		{"no status", func() *statusOut { return nil }, []string{"Nothing is recorded for this loop that this page can read, so there is nothing to count."}, nil, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := dockUsageLoopOf(dockUsageInput{Key: "lexample", Name: "example-loop", Status: c.status()})
			all := strings.Join(append(append(append([]string{}, l.Did...), l.Window...), l.Brief...), "\n")
			for _, w := range c.want {
				if !strings.Contains(all, w) {
					t.Errorf("missing %q in\n%s", w, all)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(all, a) {
					t.Errorf("unexpected %q in\n%s", a, all)
				}
			}
			if len(l.Brief) != c.brief {
				t.Errorf("brief has %d sentences: %q", len(l.Brief), l.Brief)
			}
			for _, banned := range []string{"$", "saved you", "% saved", "savings"} {
				if strings.Contains(all, banned) {
					t.Errorf("wording %q in\n%s", banned, all)
				}
			}
		})
	}
}

// The day table lists only the period's days, oldest first, already worded.
func TestDockUsageDaysAreThePeriods(t *testing.T) {
	l := dockUsageLoopOf(dockUsageInput{Key: "lexample", Name: "example-loop", Status: usageStatus()})
	if len(l.Days) != 2 || l.Days[0].Day != "Thu 12 Nov" || l.Days[0].Highest != "150,000" || l.Days[1].Window != "200,000" || !l.Days[1].Estimated {
		t.Errorf("%+v", l.Days)
	}
}

// Reasons with the same wording are joined, a zero count is left out, and
// past the cap the rest are summed.
func TestDockUsageBreakdownJoinsCapsAndDropsZeros(t *testing.T) {
	rs := []savingsReason{usageReason("unavailable", 0), usageReason("weird_one", 5), usageReason("weird_two", 4)}
	for i, code := range []string{"idle", "active", "child_busy", "paused", "stall", "input"} {
		rs = append(rs, usageReason(code, 3-i/2))
	}
	got := dockUsageBreakdown(rs, 9+3+3+2+2+1+1)
	want := ": 9 because a reason outside the fixed vocabulary, 3 because the session was idle, 3 because the session was working, 2 because lanes are working, 2 because the loop is paused, 1 because no progress for a while, 1 for other reasons"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestDockUsageResetsCapAndMissingSizes(t *testing.T) {
	st := usageStatus()
	items := []savingsReset{}
	for i := 0; i < dockUsageMaxResets+3; i++ {
		items = append(items, savingsReset{At: time.Date(2026, 11, 12, 10, i, 0, 0, time.UTC), By: "terminal"})
	}
	st.Savings.Resets.Items = items
	got := dockUsageResets(st.Savings)
	if !strings.HasPrefix(got, "11 session resets: Thu 12 Nov 10:00 UTC, run from a terminal, sizes before and after not recorded;") || !strings.HasSuffix(got, "; and 3 more.") {
		t.Error(got)
	}
	r := state.ContextReading{UsedTokens: 9000}
	if s := dockUsageBeforeAfter(&r, nil); s != "9,000 tokens before, size after not recorded" {
		t.Error(s)
	}
	if s := dockUsageBeforeAfter(nil, &r); s != "size before not recorded, 9,000 tokens after" {
		t.Error(s)
	}
}

func TestDockUsageEstimateSingleValue(t *testing.T) {
	st := usageBusy()
	e := &st.Savings.Estimate
	e.LowestReading, e.HighestReading, e.LowTokens, e.HighTokens, e.Estimated = usageI64(10000), usageI64(10000), usageI64(1500000), usageI64(1500000), false
	got := dockUsageEstimate(st.Savings)
	if !strings.HasPrefix(got, "Estimate: 1,500,000 tokens not re-read, from 150 withheld wakes") || !strings.Contains(got, "(reported by BB)") {
		t.Error(got)
	}
}

func TestDockUsagePickerAndTotals(t *testing.T) {
	unread := usageStatus()
	h := savingsHistory{Availability: "unavailable", Reason: "record_unreadable", PartialReasons: []string{}}
	unread.Savings.History, unread.Savings.WithheldWakes = h, savingsCountsOf(nil, h, "")
	partial := usageBusy()
	partial.Savings.History.Partial = true
	cases := []struct {
		name   string
		loops  []*statusOut
		picker string
		total  []string
	}{
		{"no loops", nil, "Nothing recorded", []string{"No loops are registered, so there is nothing to count."}},
		{"none readable", []*statusOut{nil, unread}, "Nothing recorded", []string{"No loop's check history could be read, so nothing can be counted this week."}},
		{"all zero", []*statusOut{usageStatus()}, "Nothing withheld this week",
			[]string{"Across 1 loop this week: 0 checks sent nothing, 0 wakes withheld, 0 messages sent and 0 session resets."}},
		{"sum", []*statusOut{usageBusy(), usageBusy(), usageStatus(), unread}, "300 wakes withheld this week", []string{
			"Across 3 loops this week: 346 checks sent nothing, 300 wakes withheld, 12 messages sent and 2 session resets.",
			"1 loop's history could not be read and is not counted.",
		}},
		{"partial", []*statusOut{partial, nil, nil}, "At least 150 wakes withheld this week", []string{
			"Across 1 loop this week: at least 173 checks sent nothing, at least 150 wakes withheld, at least 6 messages sent and at least 1 session reset.",
			"2 loops' histories could not be read and are not counted.",
			"Some of this week's history is missing for at least one loop, so these totals are at least the figures shown.",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := []dockUsageInput{}
			for i, st := range c.loops {
				in = append(in, dockUsageInput{Key: fmt.Sprintf("l%d", i), Name: fmt.Sprintf("example-%d", i), Status: st})
			}
			if got := dockUsagePicker(in); got != c.picker {
				t.Errorf("picker %q", got)
			}
			p := buildDockUsagePage(in, dockNow, true)
			if strings.Join(p.Total, "|") != strings.Join(c.total, "|") {
				t.Errorf("total\n got %q\nwant %q", p.Total, c.total)
			}
			if !strings.HasPrefix(p.Lede, "This page is about what looptender did") || len(p.Loops) != len(c.loops) || p.Chat == "" || len(p.Context) != 4 {
				t.Errorf("%+v", p)
			}
		})
	}
}

// The page's context wording claims nothing looptender does not do.
func TestDockUsageContextWordingClaimsNoMore(t *testing.T) {
	all := strings.Join(append(append([]string{}, dockUsageContext...), dockUsageChat, dockUsageMechanism, dockUsageLede), " ")
	for _, want := range []string{"prompt_goal_on_change", "is something a person runs", "is a preview only: this build never resets or compacts", "nobody is woken", "Routes page"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, banned := range []string{"saves you", "money", "$", "%", "compacts the", "resets the conversation when"} {
		if strings.Contains(all, banned) {
			t.Errorf("claims %q", banned)
		}
	}
}

// The main report carries only the picker line; the page itself stays out
// of it and under its own bound for the largest registry.
func TestDockUsagePageStaysUnderItsBound(t *testing.T) {
	inputs := make([]dockInput, dockMaxLoops)
	for i := range inputs {
		in := dockMaximalInput(i)
		in.Entry.Loop = strings.Repeat("\U0001D4B3", 200) + in.Entry.Loop
		st := usageBusy()
		s := &st.Savings
		s.History = savingsHistory{Availability: "recorded", Partial: true, PartialReasons: []string{"history_pruned", "history_dropped", "collapsed_span"}}
		rs := []savingsReason{}
		for j := 0; j < 40; j++ {
			rs = append(rs, usageReason(fmt.Sprintf("example_code_%d", j), 1000000+j))
		}
		for code := range dockReasons {
			rs = append(rs, usageReason(code, 999999))
		}
		s.ChecksSentNothing, s.WithheldWakes = usageCounts(rs...), usageCounts(rs...)
		s.SpacedWakes = usageCounts(savingsReason{Reason: "idle_unchanged", Count: 1000000})
		s.Sends = savingsSends{Availability: "recorded", Total: usageInt(6000000), ByKind: []savingsKind{{"wake", 1000000}, {"nudge", 1000000}, {"reprompt", 1000000}, {"turn_retry", 1000000}, {"handoff", 1000000}, {"switch_back", 1000000}},
			ContextBlocks: usageInt(1000000), ContextBytes: usageI64(1 << 40)}
		resets := []savingsReset{}
		big := state.ContextReading{UsedTokens: 1 << 40}
		for j := 0; j < 500; j++ {
			resets = append(resets, savingsReset{At: dockNow, By: "terminal", Before: &big, After: &big})
		}
		s.Resets.Items = resets
		days := []state.ContextDay{}
		for j := 0; j < state.ContextDaysKept; j++ {
			days = append(days, state.ContextDay{Day: dockNow.AddDate(0, 0, -j).Format(time.DateOnly), Lowest: 1 << 40, Highest: 1 << 40, Last: 1 << 40, WindowTokens: 1 << 40, Count: 9, Estimated: true})
		}
		st.ContextWindow.Days = days
		recent := []state.ContextReading{}
		for j := 0; j < state.ContextRecentKept; j++ {
			recent = append(recent, big)
		}
		st.ContextWindow.Recent = recent
		in.Status.Savings, in.Status.ContextWindow = st.Savings, st.ContextWindow
		inputs[i] = in
	}
	r := buildDockReport(inputs, dockNow, time.Minute, defaultStaleAfter, true, true, 0, true)
	body, err := json.Marshal(r.usage)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("usage page %d bytes", len(body))
	if len(body) > dockMaxUsageResponse*3/4 {
		t.Errorf("%d bytes, over three quarters of the %d-byte bound", len(body), dockMaxUsageResponse)
	}
	main, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(main), "not re-read") || !strings.Contains(string(main), `"usagePicker":"At least `) {
		t.Errorf("the main report carries the page, or lacks the picker line")
	}
	for _, l := range r.usage.Loops {
		if len(l.Days) > savingsDays {
			t.Fatalf("%d days", len(l.Days))
		}
	}
}

// api/v1/usage answers from the same read under the same session check,
// refuses what the report refuses, and writes nothing.
func TestDockUsageEndpointIsReadOnlyAndGuarded(t *testing.T) {
	base, addr, _ := dockServe(t)
	resp, body := dockRequest(t, http.MethodGet, base+dockUsagePath, nil)
	var p dockUsagePage
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" ||
		json.Unmarshal([]byte(body), &p) != nil || p.SchemaVersion != 1 || !p.ReadOnly || len(p.Loops) != len(dockLoops) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	wrong := strings.Repeat("0", 64)
	for _, c := range []struct{ method, url string }{
		{"GET", "http://" + addr + "/" + dockUsagePath},
		{"GET", "http://" + addr + "/" + wrong + "/" + dockUsagePath},
		{"POST", base + dockUsagePath},
		{"PUT", base + dockUsagePath},
		{"DELETE", base + dockUsagePath},
	} {
		resp, body := dockRequest(t, c.method, c.url, nil)
		if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "schemaVersion") {
			t.Errorf("%s %s: %d", c.method, c.url, resp.StatusCode)
		}
	}
	resp, _ = dockRequest(t, http.MethodGet, base+dockUsagePath, func(q *http.Request) { q.Header.Set("Origin", "https://example.com") })
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: %d", resp.StatusCode)
	}
}

// Reading the page changes nothing on disk, and a fresh report and page
// come from one read.
func TestDockUsageReadWritesNothing(t *testing.T) {
	d, root := dockFixtureReader(t)
	before := treeDigest(t, root)
	for i := 0; i < 3; i++ {
		d.cache = nil
		if _, err := d.usageJSON(); err != nil {
			t.Fatal(err)
		}
	}
	if after := treeDigest(t, root); after != before {
		t.Fatal("reading the Usage page changed the registry, a configuration or a state directory")
	}
	d.cache = nil
	main, err := d.json(false)
	if err != nil {
		t.Fatal(err)
	}
	page, err := d.usageJSON()
	if err != nil {
		t.Fatal(err)
	}
	c := d.cache[false]
	if string(c.body) != string(main) || string(c.usage) != string(page) {
		t.Error("the page did not come from the report's read")
	}
	var p dockUsagePage
	if err := json.Unmarshal(page, &p); err != nil || len(p.Loops) == 0 {
		t.Fatalf("%v %s", err, page)
	}
	r := d.read(context.Background(), false)
	if r.UsagePicker == "" {
		t.Error("no picker line")
	}
}

// dockUsageScratch gives one fictional loop of the screenshot registry a
// week to show on the Usage page: idle checks that sent nothing, a few
// sends, a session reset and context readings.
func dockUsageScratch(t *testing.T, stateDir string, now time.Time) {
	t.Helper()
	on := &incident.Controls{Enabled: true}
	idle := incident.Observation{Availability: "observed", State: "idle"}
	add := func(r incident.Record) {
		if err := incident.Append(stateDir, r); err != nil {
			t.Fatal(err)
		}
	}
	for h := 140; h > 2; h -= 3 {
		at := now.Add(-time.Duration(h) * time.Hour)
		switch {
		case h%27 == 0:
			add(incident.Record{Kind: "tick", CheckStartedAt: at, Action: "wake", Outcome: "succeeded", Cause: "idle", ControlBefore: on, ControlAfter: on, Observation: idle})
		case h%10 == 0:
			add(incident.Record{Kind: "tick", CheckStartedAt: at, Action: "none", Outcome: "succeeded", Cause: "child_busy", ControlBefore: on, ControlAfter: on, Observation: idle})
		default:
			add(incident.Record{Kind: "tick", CheckStartedAt: at, Action: "none", Outcome: "succeeded", Cause: "idle", ControlBefore: on, ControlAfter: on, Observation: idle})
		}
	}
	reset := now.Add(-50 * time.Hour)
	add(incident.Record{Kind: "control", CheckStartedAt: reset, ControlBefore: on, ControlAfter: on,
		ControlChange: &incident.ControlChange{Operation: sessionResetByThread, SaveObservedAt: reset}})
	st, err := state.Load(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	rec := &state.ContextRecord{Version: state.ContextVersion, Target: st.Target()}
	for d := 6; d >= 0; d-- {
		day := now.AddDate(0, 0, -d).Format(time.DateOnly)
		low, high := int64(20000+d*9000), int64(90000+d*14000)
		rec.Days = append(rec.Days, state.ContextDay{Day: day, Lowest: low, Highest: high, Last: high, WindowTokens: 200000, Count: 6, Estimated: d == 3})
	}
	for i, used := range []int64{150000, 162000, 171000, 18000, 26000, 41000, 52000, 64000} {
		at := reset.Add(time.Duration(i-3) * 4 * time.Hour)
		if i >= 3 {
			at = reset.Add(time.Duration(i-2) * 6 * time.Hour)
		}
		rec.Recent = append(rec.Recent, state.ContextReading{UsedTokens: used, WindowTokens: 200000, At: at})
	}
	rec.Latest = rec.Recent[len(rec.Recent)-1]
	st.Context = rec
	if err := state.Save(stateDir, st); err != nil {
		t.Fatal(err)
	}
}

// The page's script: the Usage row comes after Versions with a neutral dot,
// the route is #/usage, the page reads its own endpoint only with GET, the
// loop page's block sits after every other panel so loading it moves
// nothing above it, and the days table is a labelled, scrollable region.
func TestDockUsagePageScript(t *testing.T) {
	js, css := dockUIText(t, "dock.js"), dockUIText(t, "dock.css")
	versions := strings.Index(js, `pickRow("#/versions"`)
	usage := strings.Index(js, `pickRow("#/usage", "dot-calm", "Usage", d.usagePicker`)
	if versions < 0 || usage < versions {
		t.Error("the Usage row is not after Versions with a neutral dot")
	}
	for _, want := range []string{
		`var USAGE = "api/v1/usage";`,
		`if (h === "usage") return { view: "usage" };`,
		`else if (r.view === "usage") renderUsage(d, pane);`,
		`pane.appendChild(checks);
    pane.appendChild(usageBrief(key));
  }`,
		`link("#/usage", "Every count, on the Usage page", "work-inspect")`,
		`wrap.setAttribute("role", "region");`,
		`wrap.setAttribute("aria-labelledby", id);`,
		`th.scope = "row";`,
		`priority: route().view === "usage" ? "auto" : "low"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("dock.js lacks %q", want)
		}
	}
	load := js[strings.Index(js, "function loadUsage()"):]
	load = load[:strings.Index(load, "\n  }\n")]
	if strings.Contains(load, "method") || strings.Contains(load, "post(") {
		t.Error("the Usage read is not a plain GET")
	}
	if !strings.Contains(cssRule(t, css, ".table-wrap"), "overflow-x: auto") || !strings.Contains(cssRule(t, css, ".usage-days th, .usage-days td"), "white-space: nowrap") {
		t.Error("the days table neither scrolls nor keeps its cells on one line")
	}
	// Nothing on the page is a warning.
	page := js[strings.Index(js, "// ---- Usage: what looptender did"):strings.Index(js, "function wantsUsage(")]
	for _, banned := range []string{"c-warn", "tone-warn", "is-alarm", "dot-warn", "dot-attn", "notice("} {
		if strings.Contains(page, banned) {
			t.Errorf("the Usage page uses %q", banned)
		}
	}
}

// A Usage read that never answers is abandoned within the refresh
// interval and takes the failure path, so later reads are not blocked.
func TestDockUsageReadHasATimeout(t *testing.T) {
	js := dockUIText(t, "dock.js")
	load := js[strings.Index(js, "function loadUsage()"):]
	load = load[:strings.Index(load, "\n  }\n")]
	for _, want := range []string{
		"var abort = new AbortController();",
		"setTimeout(function () { abort.abort(); }, usageTimeout());",
		"signal: abort.signal",
		".catch(function () { state.usageFailed = true; })",
		"clearTimeout(timer);\n        state.usageBusy = false;",
	} {
		if !strings.Contains(load, want) {
			t.Errorf("loadUsage lacks %q", want)
		}
	}
	if !strings.Contains(js, "function usageTimeout() { return Math.min(20000, Math.max(3000, refreshSeconds() * 1000 - 2000)); }") {
		t.Error("the timeout is not bounded under the refresh interval")
	}
}
