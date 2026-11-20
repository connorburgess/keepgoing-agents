package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
)

// The allowances strip is headed by the account the person assigned each
// route to, with each route's own reading beneath it; any other route keeps
// its group and says why it is not under an account. Fictional catalog only.
func TestDockAllowancesHeadedByAssignedAccount(t *testing.T) {
	catalog := &accounts.File{Entries: []accounts.Entry{
		{ID: acctWork, Label: "Work Codex", Provider: "codex", Mode: "inherited"},
		{ID: acctOther, Label: "Side Claude", Provider: "claude-code", Mode: "explicit_directory", Directory: "/home/example/.claude-side"},
	}}
	reset := dockNow.Add(3 * time.Hour)
	codexBound := config.Agent{Provider: "codex", Group: "codex", Account: acctWork, Codex: &config.Codex{}}
	codexBB := config.Agent{Provider: "codex", Group: "codex", Account: acctWork, BB: &config.BB{}}
	codexUnbound := config.Agent{Provider: "codex", Group: "codex", Codex: &config.Codex{}}
	codexOnClaude := config.Agent{Provider: "codex", Group: "codex", Account: acctOther, Codex: &config.Codex{}}
	codexMissing := config.Agent{Provider: "codex", Group: "codex", Account: "00000000000000000000000000000000", Codex: &config.Codex{}}

	out := acctReading(2*time.Hour, 100, &reset)
	low := acctReading(20*time.Minute, 20, nil)
	none := dockUsageTarget{Availability: "unavailable", Windows: []dockUsageWindow{}}
	route := func(key, title, identity string, u dockUsageTarget) dockRoute {
		return dockRoute{Key: key, Provider: "codex", Account: "codex", Title: title, Tool: "Codex CLI", identity: identity, Usage: u}
	}
	grouped := func(r dockRoute, group string) dockRoute { r.TitleAccount = group; return r }
	type use struct {
		loop, route string
		agent       config.Agent
	}
	build := func(uses []use, routes []dockRoute) ([]dockAllowance, []dockLoop) {
		byLoop := map[string][]dockChainEntry{}
		order := []string{}
		for _, u := range uses {
			_, e := acctEntry(u.loop, u.route, "example", u.agent, catalog, none)
			if _, ok := byLoop[u.loop]; !ok {
				order = append(order, u.loop)
			}
			byLoop[u.loop] = append(byLoop[u.loop], e)
		}
		loops := []dockLoop{}
		for _, n := range order {
			loops = append(loops, dockLoop{Key: dockKey(n), Name: n, Chain: dockChain{Entries: byLoop[n]}})
		}
		allow, _ := dockAllowances(routes, loops, dockNow)
		return allow, loops
	}
	// Each block in words: title, why it is or is not under an account, its
	// routes, and for an account each route's own reading in order.
	describe := func(a dockAllowance) string {
		s := a.Title + " | " + a.Sub
		if a.Assigned != "" {
			s += " | assigned " + a.Assigned
		}
		if a.Assignment != "" {
			s += " | " + a.Assignment
		}
		for _, l := range a.Lines {
			s += " | " + l.Title + ": " + say(l.Reading, dockNow)
			if l.BBThread {
				s += " (BB thread)"
			}
		}
		if a.Usage != nil {
			s += " | own reading: " + a.Usage.ReadLine
		}
		return s
	}
	for _, c := range []struct {
		name   string
		uses   []use
		routes []dockRoute
		want   []string
	}{
		{"bound to one label", []use{{"alpha", "rA", codexBound}}, []dockRoute{route("rA", "gpt-example", "", low)},
			[]string{"Work Codex | 1 route | assigned by_you | gpt-example: Read 20 min ago"}},
		{"unbound keeps its group", []use{{"alpha", "rA", codexUnbound}}, []dockRoute{route("rA", "gpt-example", "id-1", low)},
			[]string{"codex | 1 route: gpt-example · account unknown | Not assigned to an account. | own reading: "}},
		{"a failed check is not under the label", []use{{"alpha", "rA", codexOnClaude}}, []dockRoute{route("rA", "gpt-example", "", low)},
			[]string{"codex | 1 route: gpt-example · account unknown | The accounts catalog entry is for another provider. | own reading: "}},
		{"an entry the catalog lacks is not under a label", []use{{"alpha", "rA", codexMissing}}, []dockRoute{route("rA", "gpt-example", "", none)},
			[]string{"codex | 1 route: gpt-example · account unknown | The accounts catalog has no entry with this ID. | own reading: No reading"}},
		{"a BB thread route is under the label, not verified", []use{{"alpha", "rB", codexBB}}, []dockRoute{route("rB", "BB thread gpt-example", "", low)},
			[]string{"Work Codex | 1 route | assigned by_you | BB thread gpt-example: Read 20 min ago (BB thread)"}},
		{"two routes on one label keep their own readings, the one out first",
			[]use{{"alpha", "rB", codexBB}, {"alpha", "rA", codexBound}, {"beta", "rA", codexBound}},
			[]dockRoute{route("rB", "BB thread gpt-example", "", low), route("rA", "gpt-example", "id-1", out)},
			[]string{"Work Codex | 2 routes | assigned by_you | gpt-example: Read 2 h ago | BB thread gpt-example: Read 20 min ago (BB thread)"}},
		{"no reading", []use{{"alpha", "rA", codexBound}}, []dockRoute{route("rA", "gpt-example", "", none)},
			[]string{"Work Codex | 1 route | assigned by_you | gpt-example: No usage reading is recorded for its route."}},
		{"loops that state different accounts are not under either", []use{{"alpha", "rA", codexBound}, {"beta", "rA", codexUnbound}}, []dockRoute{route("rA", "gpt-example", "", none)},
			[]string{"codex | 1 route: gpt-example · account unknown | Its loops assign it to different accounts. | own reading: No reading"}},
		{"bound and unbound routes of one identity are two blocks, the one out first",
			[]use{{"alpha", "rA", codexUnbound}, {"alpha", "rB", codexBound}},
			[]dockRoute{route("rA", "gpt-small", "id-1", low), route("rB", "gpt-example", "id-1", out)},
			[]string{"Work Codex | 1 route | assigned by_you | gpt-example: Read 2 h ago",
				"codex | 1 route: gpt-small · account unknown | Not assigned to an account. | own reading: "}},
		{"under a label a route is named by its model alone",
			[]use{{"alpha", "rA", codexBound}, {"alpha", "rB", codexBB}},
			[]dockRoute{grouped(route("rA", "gpt-example", "", low), "team-account"), grouped(route("rB", "gpt-small", "", low), "team-second-account")},
			[]string{"Work Codex | 2 routes | assigned by_you | gpt-example: Read 20 min ago | gpt-small: Read 20 min ago (BB thread)"}},
		{"the group is added back only where two lines share a name",
			[]use{{"alpha", "rA", codexBound}, {"alpha", "rB", codexBB}, {"alpha", "rC", codexBB}},
			[]dockRoute{grouped(route("rA", "gpt-example", "", low), "team-account"), grouped(route("rB", "gpt-example", "", low), "team-second-account"),
				grouped(route("rC", "gpt-small", "", low), "team-third-account")},
			[]string{"Work Codex | 3 routes | assigned by_you | gpt-example on team-account: Read 20 min ago | gpt-example on team-second-account: Read 20 min ago (BB thread) | gpt-small: Read 20 min ago (BB thread)"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			allow, _ := build(c.uses, c.routes)
			got := []string{}
			for _, a := range allow {
				d := describe(a)
				// An unbound block keeps its single route's read line;
				// only whether it has one is compared here.
				if i := strings.Index(d, " | own reading: "); i >= 0 && a.Usage.ObservedAt != nil {
					d = d[:i+len(" | own reading: ")]
				}
				got = append(got, d)
				// The reset is said once per route: by its windows, never
				// again in its line.
				for _, l := range a.Lines {
					if strings.Contains(say(l.Reading, dockNow), "reset") {
						t.Errorf("%s says its reset in its line: %q", l.Title, say(l.Reading, dockNow))
					}
				}
				if a.Assigned != "" && (a.Usage != nil || len(a.Notes) != 0 || len(a.Next) != 0) {
					t.Errorf("an account block carries a figure of its own: %+v", a)
				}
			}
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("\n got %q\nwant %q", got, c.want)
			}
		})
	}

	// The JSON of an account block names each route by its key and sends no
	// usage of its own, so the page reads each route's own reading.
	allow, _ := build([]use{{"alpha", "rA", codexBound}, {"alpha", "rB", codexBB}},
		[]dockRoute{route("rA", "gpt-example", "", out), route("rB", "BB thread gpt-example", "", low)})
	raw, err := json.Marshal(allow[0])
	if err != nil {
		t.Fatal(err)
	}
	if s := string(raw); strings.Contains(s, `"usage"`) || !strings.Contains(s, `"route":"rA"`) || !strings.Contains(s, `"route":"rB"`) || strings.Count(s, `"reading"`) != 2 {
		t.Errorf("account block JSON: %s", s)
	}
}

// A loop that left a route of an account on a usage limit is said on that
// route's line, compared with that route's own reading.
func TestDockAllowanceLineSaysTheLimitALoopLeftItOn(t *testing.T) {
	catalog := &accounts.File{Entries: []accounts.Entry{{ID: acctWork, Label: "Work Codex", Provider: "codex", Mode: "inherited"}}}
	a := config.Agent{Provider: "codex", Group: "codex", Account: acctWork, Codex: &config.Codex{}}
	_, e := acctEntry("alpha", "rA", "gpt-example", a, catalog, dockUsageTarget{})
	at, reset := dockNow.Add(-time.Hour), dockNow.Add(2*time.Hour)
	r := dockRoute{Key: "rA", Provider: "codex", Account: "codex", Title: "gpt-example", Usage: acctReading(time.Hour, 100, &reset),
		LeftBy: []dockDeparture{{loop: "example-loop", How: dockLeftLimit, At: &at, ResetsAt: &reset}}}
	allow, _ := dockAllowances([]dockRoute{r}, []dockLoop{{Key: dockKey("alpha"), Name: "alpha", Chain: dockChain{Entries: []dockChainEntry{e}}}}, dockNow)
	if len(allow) != 1 || len(allow[0].Lines) != 1 || len(allow[0].Lines[0].Notes) != 1 ||
		!strings.HasPrefix(say(allow[0].Lines[0].Notes[0].Text, dockNow), "example-loop hit a usage limit on this route on ") {
		t.Fatalf("%+v", allow)
	}
}

// The page draws an account block from each route's own reading, in the
// words the Accounts page and the loop page use, and draws no combined
// figure for it.
func TestDockAllowancesPageReadsEachRoute(t *testing.T) {
	js := dockUIText(t, "dock.js")
	f := jsFunction(t, js, "function allowancesPanel(d) {")
	for _, want := range []string{
		`head.appendChild(el("span", "sub", bbOnly ? ASSIGNED_BB : ASSIGNED));`,
		`if (a.assignment) head.appendChild(el("span", "sub", a.assignment));`,
		`link("#/routes/" + r.route, r.title)`,
		`var reading = el("p", "allow-reading", say(r.reading));`,
		`var u = routeUsage(d, r.route);`,
	} {
		if !strings.Contains(f, want) {
			t.Errorf("allowancesPanel lacks %q", want)
		}
	}
	// The account branch never reads a block-level usage.
	account := f[strings.Index(f, "if (a.assigned) {"):strings.Index(f, "} else {")]
	if strings.Contains(account, "a.usage") || strings.Contains(account, "a.next") {
		t.Errorf("an account block draws a figure of its own:\n%s", account)
	}
	if !strings.Contains(js, `var ASSIGNED = "assigned by you · identity not verified";`) {
		t.Error("the assigned wording moved; the strip must use the Accounts page's")
	}
}

// A usage context describes configured probe inputs, not account identity.
// Previously the strip selected the newest route's windows for their shared
// context. Different readings, ended windows and missing evidence must survive.
func TestDockAllowancesNeverSubstituteSharedContextReadings(t *testing.T) {
	reset := dockNow.Add(-time.Minute)
	ended := acctReading(3*time.Hour, 93, &reset)
	ended.Availability = "stale"
	ended.Windows[0].Stale = true
	missing := dockUsageTarget{Availability: "unavailable", ReadLine: "No reading", Windows: []dockUsageWindow{}, Reason: "not recorded"}
	recent := acctReading(5*time.Minute, 21, nil)
	routes := []dockRoute{
		{Key: "old", Provider: "codex", Account: "example-group", Title: "example-large", identity: "same-context", Usage: ended},
		{Key: "missing", Provider: "codex", Account: "example-group", Title: "example-missing", identity: "same-context", Usage: missing},
		{Key: "recent", Provider: "codex", Account: "example-group", Title: "example-small", identity: "same-context", Usage: recent},
	}
	allow, _ := dockAllowances(routes, nil, dockNow)
	if len(allow) != 3 {
		t.Fatalf("shared context collapsed %d routes into %d blocks", len(routes), len(allow))
	}
	for i, a := range allow {
		want := routes[i].Usage
		if want.ReadLine == "" {
			if want.ObservedAt != nil {
				want.ReadLine = "Read " + dockAgo(dockNow, *want.ObservedAt)
			} else {
				want.ReadLine = "No reading"
			}
		}
		want.ReadLine = strings.Replace(want.ReadLine, "Usage read", "Read", 1)
		if !reflect.DeepEqual(a.Usage, &want) {
			t.Errorf("route %s reading substituted: got %+v want %+v", routes[i].Key, a.Usage, want)
		}
		if a.Assigned != "" || a.Title != "example-group" || strings.Contains(a.Sub, "account 1") {
			t.Errorf("inferred account: %+v", a)
		}
	}
}

// Nil-first and assigned-first loop orders must both refuse a combined label.
func TestDockAllowancesMixedAssignmentOrder(t *testing.T) {
	a := &accounts.RouteAccount{Assigned: "by_you", Account: acctWork, Label: "Example selected", Check: accounts.RouteConsistent}
	for _, entries := range [][]dockChainEntry{
		{{Route: "r"}, {Route: "r", CatalogAccount: a}},
		{{Route: "r", CatalogAccount: a}, {Route: "r"}},
	} {
		loops := []dockLoop{{Chain: dockChain{Entries: entries[:1]}}, {Chain: dockChain{Entries: entries[1:]}}}
		got, _ := dockAllowances([]dockRoute{{Key: "r", Account: "example-group"}}, loops, dockNow)
		if len(got) != 1 || got[0].Assigned != "" || got[0].Assignment != "Its loops assign it to different accounts." {
			t.Errorf("mixed order: %+v", got)
		}
	}
}

// Explicit assignment combines only the heading. Neither shared context nor
// the selected-context quota cache supplies a replacement reading for a line.
func TestDockAllowancesAssignedSharedContextKeepsEachRoute(t *testing.T) {
	a := &accounts.RouteAccount{Assigned: "by_you", Account: acctWork, Label: "Example selected", Check: accounts.RouteConsistent}
	routes := []dockRoute{
		{Key: "low", Title: "example-small", identity: "shared", Usage: acctReading(5*time.Minute, 21, nil)},
		{Key: "out", Title: "example-large", identity: "shared", Usage: acctReading(time.Hour, 100, nil)},
		{Key: "missing", Title: "example-missing", identity: "shared", Usage: dockUsageTarget{Availability: "unavailable", Windows: []dockUsageWindow{}}},
	}
	entries := []dockChainEntry{}
	for _, r := range routes {
		entries = append(entries, dockChainEntry{Route: r.Key, CatalogAccount: a})
	}
	got, _ := dockAllowances(routes, []dockLoop{{Chain: dockChain{Entries: entries}}}, dockNow)
	if len(got) != 1 || got[0].Usage != nil || len(got[0].Lines) != 3 {
		t.Fatalf("%+v", got)
	}
	lines := got[0].Lines
	if lines[0].Route != "out" || lines[1].Route != "low" || lines[2].Route != "missing" {
		t.Errorf("out-first stable order: %+v", lines)
	}
	if say(lines[0].Reading, dockNow) != "Read 1 h ago" || say(lines[1].Reading, dockNow) != "Read 5 min ago" || !strings.Contains(say(lines[2].Reading, dockNow), "No usage reading") {
		t.Errorf("readings: %+v", lines)
	}
}
