package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
)

// Fictional catalog IDs and directories only.
const (
	acctWork  = "0123456789abcdef0123456789abcdef"
	acctOther = "fedcba9876543210fedcba9876543210"
)

func acctEntry(loop, route, title string, a config.Agent, catalog *accounts.File, u dockUsageTarget) (string, dockChainEntry) {
	e := dockChainEntry{Route: route, Title: title, Usage: u}
	r := accounts.CheckRoute(catalog, a)
	e.CatalogAccount, e.acct = &r, dockAccountFactsOf(a)
	return dockKey(loop), e
}

func acctReading(ago time.Duration, used int, resets *time.Time) dockUsageTarget {
	at := dockNow.Add(-ago)
	age := int64(ago / time.Second)
	return dockUsageTarget{Availability: "available", ObservedAt: &at, AgeSeconds: &age, Windows: []dockUsageWindow{{Name: "five-hour", UsedPercent: used, ResetsAt: resets}}}
}

func TestDockAccountUses(t *testing.T) {
	catalog := &accounts.File{Entries: []accounts.Entry{
		{ID: acctWork, Label: "Work Codex", Provider: "codex", Mode: "inherited"},
		{ID: acctOther, Label: "Side Claude", Provider: "claude-code", Mode: "explicit_directory", Directory: "/home/example/.claude-side"},
	}}
	view := accounts.View{State: "available", Complete: true, Cards: []accounts.Card{{ID: acctWork, Label: "Work Codex"}, {ID: acctOther, Label: "Side Claude"}}}
	reset := dockNow.Add(3 * time.Hour)
	codexBound := config.Agent{Provider: "codex", Group: "codex", Account: acctWork, Codex: &config.Codex{}}
	codexBB := config.Agent{Provider: "codex", Group: "codex", Account: acctWork, BB: &config.BB{}}
	claudeUnnamed := config.Agent{Provider: "claude-code", Group: "claude-side", Claude: &config.Claude{Env: map[string]string{"CLAUDE_CONFIG_DIR": "/home/example/.claude-side"}}}
	bbUnnamed := config.Agent{Provider: "claude-code", Group: "claude-code", BB: &config.BB{}}

	loop := func(name string, entries ...dockChainEntry) dockLoop {
		return dockLoop{Key: dockKey(name), Name: name, Chain: dockChain{Entries: entries}}
	}
	_, a1 := acctEntry("alpha", "rCodex", "gpt-example", codexBound, catalog, acctReading(2*time.Hour, 100, &reset))
	_, a2 := acctEntry("alpha", "rCodexBB", "BB thread gpt-example", codexBB, catalog, acctReading(5*time.Hour, 20, nil))
	// A route that names the Claude entry but is a Codex route: listed under
	// the entry with its check, and its reading, though out and newest, is
	// left out of the entry's summary.
	codexOnSide := config.Agent{Provider: "codex", Group: "codex", Account: acctOther, Codex: &config.Codex{}}
	_, d1 := acctEntry("delta", "rMismatch", "gpt-example", codexOnSide, catalog, acctReading(time.Minute, 100, &reset))
	_, b1 := acctEntry("beta", "rCodex", "gpt-example", codexBound, catalog, dockUsageTarget{Availability: "unavailable"})
	_, b2 := acctEntry("beta", "rSide", "sonnet-example", claudeUnnamed, catalog, dockUsageTarget{Availability: "unavailable"})
	_, c1 := acctEntry("gamma", "rBB", "opus-example", bbUnnamed, catalog, dockUsageTarget{Availability: "unavailable"})
	r := dockReport{Complete: true, Loops: []dockLoop{loop("alpha", a1, a2), loop("beta", b1, b2), loop("gamma", c1), loop("delta", d1)},
		Routes: []dockRoute{{Key: "rCodex", Usage: acctReading(2*time.Hour, 100, &reset)}, {Key: "rCodexBB", Usage: acctReading(5*time.Hour, 20, nil)}}}

	for _, ids := range []bool{false, true} {
		uses, omitted := dockAccountUses(r, view, catalog, ids, dockNow)
		if omitted != 0 || len(uses) != 4 {
			t.Fatalf("identifiers %t: %d uses, %d omitted", ids, len(uses), omitted)
		}
		work, side, unnamedClaude, unnamedBB := uses[0], uses[1], uses[2], uses[3]

		// A catalog entry with two routes, one of them a BB thread route,
		// each with the loops that use it.
		if work.Card != acctWork || len(work.Routes) != 2 || work.Routes[0].Route != "rCodex" || !slices.Equal(work.Routes[0].Loops, []string{dockKey("alpha"), dockKey("beta")}) ||
			work.Routes[0].BBThread || !work.Routes[1].BBThread || work.Context != "" || work.Add != "" {
			t.Errorf("work: %+v", work)
		}
		// Out, from the newest reading among its routes only.
		if got := say(work.Reading, dockNow); !work.Out || got != "gpt-example: Five-hour allowance was out when read 2 h ago · resets in 3 h (15:00)" {
			t.Errorf("work reading: %t %q", work.Out, got)
		}
		// A route whose check found a mismatch: listed under the entry it
		// names, with what its check says, and kept out of the summary.
		if side.Card != acctOther || len(side.Routes) != 1 || side.Routes[0].CheckText != accounts.RouteCheckText(accounts.RouteProviderMismatch) || work.Routes[0].CheckText != "" {
			t.Errorf("side: %+v", side)
		}
		if got := say(side.Reading, dockNow); side.Out || got != "No usage reading is recorded for its route." {
			t.Errorf("mismatch reading: %t %q", side.Out, got)
		}
		// An unbound headless context stays separate from the selected
		// entry even when its directory matches.
		if unnamedClaude.Title != "Claude Code routes (account unknown)" || unnamedClaude.Selection != "explicit_directory" || unnamedClaude.Group != "claude-side" ||
			len(unnamedClaude.Routes) != 1 || unnamedClaude.Routes[0].Loops[0] != dockKey("beta") {
			t.Errorf("unnamed claude: %+v", unnamedClaude)
		}
		wantAdd := "looptender accounts add --label ACCOUNT_LABEL --provider claude-code --selection explicit_directory --account-dir ACCOUNT_DIRECTORY"
		wantDir := ""
		if ids {
			wantAdd = strings.Replace(wantAdd, "ACCOUNT_DIRECTORY", "/home/example/.claude-side", 1)
			wantDir = "/home/example/.claude-side"
		}
		if unnamedClaude.Add != wantAdd || unnamedClaude.Directory != wantDir {
			t.Errorf("identifiers %t: add %q, directory %q", ids, unnamedClaude.Add, unnamedClaude.Directory)
		}
		// No reading: said neutrally.
		if got := say(unnamedClaude.Reading, dockNow); unnamedClaude.Out || got != "No usage reading is recorded for its route." {
			t.Errorf("no reading: %q", got)
		}
		// A BB thread context: never compared with a catalog entry.
		if unnamedBB.Title != "Claude Code routes (account unknown)" || unnamedBB.Selection != "bb_thread" || !unnamedBB.Routes[0].BBThread ||
			unnamedBB.Add != "looptender accounts add --label ACCOUNT_LABEL --provider bb --selection externally_managed" {
			t.Errorf("unnamed bb: %+v", unnamedBB)
		}
	}
}

func TestDockAccountReading(t *testing.T) {
	reset := dockNow.Add(90 * time.Minute)
	ended := acctReading(30*time.Hour, 100, nil)
	ended.Availability, ended.Windows[0].Stale = "stale", true
	r := func(title string, u dockUsageTarget) dockRouteReading {
		return dockRouteReading{title: title, usage: u}
	}
	for _, c := range []struct {
		name   string
		rs     []dockRouteReading
		routes int
		want   string
		out    bool
	}{
		{"out with reset, one route", []dockRouteReading{r("opus-example", acctReading(10*time.Minute, 100, &reset))}, 1, "Five-hour allowance was out when read 10 min ago · resets in 1 h 30 min (13:30)", true},
		{"out without reset", []dockRouteReading{r("opus-example", acctReading(time.Hour, 100, nil))}, 1, "Five-hour allowance was out when read 1 h ago · no reset time was reported", true},
		// A newer reading that is not out does not hide an older one that
		// is, and the route that was out is named.
		{"older out route not hidden", []dockRouteReading{r("sonnet-example", acctReading(10*time.Minute, 30, &reset)), r("opus-example", acctReading(time.Hour, 100, &reset))}, 2,
			"opus-example: Five-hour allowance was out when read 1 h ago · resets in 1 h 30 min (13:30)", true},
		// Of two out readings, the newest is said.
		{"newest out", []dockRouteReading{r("opus-example", acctReading(3*time.Hour, 100, &reset)), r("haiku-example", acctReading(time.Hour, 100, nil))}, 2,
			"haiku-example: Five-hour allowance was out when read 1 h ago · no reset time was reported", true},
		{"not out", []dockRouteReading{r("opus-example", acctReading(2*time.Hour, 40, &reset)), r("haiku-example", acctReading(time.Hour, 40, &reset))}, 2, "Not out when read 1 h ago", false},
		{"windows ended", []dockRouteReading{r("opus-example", ended)}, 2, "Not out when read 30 h ago; every window it describes has ended since", false},
		{"no reading", []dockRouteReading{r("opus-example", dockUsageTarget{Availability: "unavailable"})}, 2, "No usage reading is recorded for its routes.", false},
		{"no routes", nil, 0, "", false},
	} {
		got, out := dockAccountReading(c.rs, c.routes, dockNow)
		if s := say(got, dockNow); s != c.want || out != c.out {
			t.Errorf("%s: %q %t", c.name, s, out)
		}
	}
}

// A provider, selection, directory, group or model does not bind a route
// to a selected account, even when those facts match a catalog entry.
func TestDockUnknownAccountDoesNotInferBinding(t *testing.T) {
	for _, selection := range []string{"inherited", "explicit_directory", "bb_thread"} {
		catalog := &accounts.File{Entries: []accounts.Entry{{ID: acctWork, Label: "Example", Provider: "codex", Mode: "inherited"}}}
		agent := config.Agent{Provider: "codex", Group: "codex", Codex: &config.Codex{}}
		if selection == "explicit_directory" {
			catalog.Entries[0].Mode = selection
			catalog.Entries[0].Directory = "/home/example/.codex"
			agent.Codex.Env = map[string]string{"CODEX_HOME": "/home/example/.codex"}
		}
		if selection == "bb_thread" {
			agent.BB = &config.BB{}
		}
		_, entry := acctEntry("example-loop", "example-route", "example-model", agent, catalog, dockUsageTarget{})
		view := accounts.View{Cards: []accounts.Card{{ID: acctWork, Label: "Example"}}}
		uses, _ := dockAccountUses(dockReport{Loops: []dockLoop{{Key: "example-loop", Chain: dockChain{Entries: []dockChainEntry{entry}}}}}, view, catalog, false, dockNow)
		if len(uses) != 2 || uses[0].Card != acctWork || len(uses[0].Routes) != 0 || uses[1].Context == "" || uses[1].Card != "" || len(uses[1].Routes) != 1 {
			t.Fatalf("%s: route was lost or assigned by inference: %+v", selection, uses)
		}
		data, _ := json.Marshal(uses)
		if strings.Contains(string(data), "mayBeSame") || strings.Contains(string(data), "Unnamed") {
			t.Fatalf("%s: suggests account identity: %s", selection, data)
		}
	}
}

// The account route lists stay small in the largest registry: capped
// accounts, routes and loops, keys and short titles only.
func TestDockAccountUsesStayCompact(t *testing.T) {
	inputs := make([]dockInput, dockMaxLoops)
	for i := range inputs {
		inputs[i] = dockMaximalInput(i)
	}
	r := buildDockReport(inputs, dockNow, time.Minute, defaultStaleAfter, true, true, 0, true)
	view := accounts.View{State: "available", Complete: true, Cards: []accounts.Card{}}
	uses, omitted := dockAccountUses(r, view, nil, true, dockNow)
	body, err := json.Marshal(struct {
		U []dockAccountUse `json:"accountUses"`
		O int              `json:"accountUsesOmitted"`
	}{uses, omitted})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d accounts (%d omitted): %d bytes", len(uses), omitted, len(body))
	if len(uses) > dockMaxAccountUses || len(body) > 64<<10 {
		t.Errorf("%d accounts, %d bytes", len(uses), len(body))
	}
	for _, u := range uses {
		if len(u.Routes) > dockMaxAccountRoutes {
			t.Errorf("%d routes", len(u.Routes))
		}
		for _, rt := range u.Routes {
			if len(rt.Loops) > dockMaxAccountLoops {
				t.Errorf("%d loops", len(rt.Loops))
			}
		}
	}
}

// The Accounts view must not turn missing bindings into additional selected
// accounts or propose identity matches. Browser fixtures also exercise the
// rendered counts, route retention and unavailable/empty states.
func TestDockUnknownAccountsStaySeparateFromSelectedCards(t *testing.T) {
	js := dockUIText(t, "dock.js")
	for _, legacy := range []string{"Unnamed ", "not named yet", "May be the same account as", "function unnamedCard", "function unnamedCount"} {
		if strings.Contains(js, legacy) {
			t.Errorf("misleading account presentation returned: %q", legacy)
		}
	}
	section := funcBody(t, js, "unknownAccountSection")
	for _, want := range []string{"Routes with unknown accounts", "separate routes do not mean separate subscriptions", "accountRouteList(d, u, false)", "aria-labelledby"} {
		if !strings.Contains(section, want) {
			t.Errorf("unknown routes lose explanation or route evidence: %q", want)
		}
	}
	picker := funcBody(t, js, "renderPicker")
	if strings.Contains(picker, "accountUses") {
		t.Error("the selected-account count includes inferred route contexts")
	}
}

// Unknown routes live outside account-body dd, so they must retain their
// own reflow rule. Browser checks use an unbroken 80-character route title
// and 120-character loop name at 320 px and verify the reading stays inside.
func TestDockUnknownRouteNamesWrap(t *testing.T) {
	css := dockUIText(t, "dock.css")
	start := strings.Index(css, ".unknown-route-context {")
	if start < 0 {
		t.Fatal("unknown route context rule missing")
	}
	rule := strings.SplitN(css[start:], "}", 2)[0]
	for _, property := range []string{"overflow-wrap: anywhere;", "min-width: 0;"} {
		if !strings.Contains(rule, property) {
			t.Errorf("unknown route context lost %s", property)
		}
	}
}
