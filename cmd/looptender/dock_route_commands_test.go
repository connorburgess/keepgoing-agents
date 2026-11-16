package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

// The loop page builds its commands to copy from loopArg: --loop and the
// registered name quoted for a shell, so a name with a space or a quote
// pastes as one argument.
func TestDockLoopArgQuotesTheNameForAShell(t *testing.T) {
	for name, want := range map[string]string{
		"example-loop":           "--loop example-loop",
		"example loop":           "--loop 'example loop'",
		"the example's loop":     `--loop 'the example'\''s loop'`,
		"example $(loop) `x`":    "--loop 'example $(loop) `x`'",
		"":                       "",
		strings.Repeat("x", 121): "", // longer than the page shows in full
		strings.Repeat("'", 120): "", // quoted, over dockMaxLoopArg bytes
	} {
		if got := dockLoopArg(name); got != want {
			t.Errorf("dockLoopArg(%q) = %q, want %q", name, got, want)
		}
	}
	if got := dockLoopArg(strings.Repeat("x", 120)); got != "--loop "+strings.Repeat("x", 120) {
		t.Errorf("a name the page shows in full: %q", got)
	}
}

// The projected loop carries loopArg from the registered name, and its chain
// says when the loop follows the standard route list.
func TestDockLoopCarriesLoopArgAndStandard(t *testing.T) {
	in := dockEvidence{}.input()
	in.Entry.Loop = "example loop"
	in.Chain = &dockChainInput{Standard: true}
	r := buildDockReport([]dockInput{in}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true)
	if len(r.Loops) != 1 {
		t.Fatalf("%d loops", len(r.Loops))
	}
	l := r.Loops[0]
	if l.LoopArg != "--loop 'example loop'" || !l.Chain.Standard {
		t.Errorf("loopArg %q, standard %v", l.LoopArg, l.Chain.Standard)
	}
	in.Chain = &dockChainInput{}
	body, _ := json.Marshal(buildDockReport([]dockInput{in}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true))
	if strings.Contains(string(body), `"standard"`) {
		t.Error("a loop with its own list sends no standard field")
	}
}

// With the longest loopArg every one of the most loops can carry, the
// largest report stays under three quarters of the response bound.
func TestDockLoopArgStaysUnderTheResponseBound(t *testing.T) {
	inputs := make([]dockInput, dockMaxLoops)
	for i := range inputs {
		in := dockMaximalInput(i)
		wide := func(s string) string { return strings.Repeat("\U0001D4B3", 200) + s }
		// 37 four-byte runes and a number: a quoted name just under the cap.
		in.Entry.Loop = strings.Repeat("\U0001D4B3", 37) + " " + string(rune('a'+i%26))
		for j := range in.Chain.Candidates {
			c := &in.Chain.Candidates[j]
			c.Provider, c.Group, c.BB.Model = wide(c.Provider), wide(c.Group), wide(c.BB.Model)
		}
		in.Chain.Primary.Agent = in.Chain.Candidates[0].Agent
		inputs[i] = in
	}
	r := buildDockReport(inputs, dockNow, time.Minute, defaultStaleAfter, true, true, 0, true)
	r.AccountUses, r.AccountUsesOmitted = dockAccountUses(r, accounts.View{State: "available", Complete: true, Cards: []accounts.Card{}}, nil, true, dockNow)
	for _, l := range r.Loops {
		if l.LoopArg == "" || len(l.LoopArg) > dockMaxLoopArg {
			t.Fatalf("loopArg %d bytes", len(l.LoopArg))
		}
	}
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d bytes, loopArg %d bytes each", len(body), len(r.Loops[0].LoopArg))
	if len(body) > dockMaxResponse*3/4 {
		t.Errorf("%d bytes, over three quarters of the %d-byte bound", len(body), dockMaxResponse)
	}
}

// funcBody returns a dock.js function's source, from its declaration to the
// next top-level function, so an assertion reads only that block.
func funcBody(t *testing.T, js, name string) string {
	t.Helper()
	start := strings.Index(js, "  function "+name+"(")
	if start < 0 {
		t.Fatalf("dock.js has no function %s", name)
	}
	rest := js[start+1:]
	if end := strings.Index(rest, "\n  function "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// P1: the read-only page says how to add an account, add a route and change
// the order, with commands a person copies and runs in Terminal. These
// assertions keep the commands to ones that exist, the placeholders and the
// lines that come with them; the headless-browser width sweep runs outside
// the suite.
func TestDockHowToBlocksP1(t *testing.T) {
	js, css := dockUIText(t, "dock.js"), dockUIText(t, "dock.css")
	for name, wants := range map[string][]string{
		"addAccountBox": {
			`"Add an account"`,
			"looptender accounts add --label ACCOUNT_LABEL --provider PROVIDER --selection explicit_directory --account-dir FOLDER",
			"looptender accounts add --label ACCOUNT_LABEL --provider PROVIDER --selection externally_managed",
			"looptender accounts add --label ACCOUNT_LABEL --provider other --selection externally_managed",
			"NAMES_ONLY", "looptender never takes the key", "not named yet",
		},
		"addRouteBox": {
			`"Add a route to this loop"`,
			`"looptender fallbacks add " + arg`,
			`"looptender fallbacks check --id ROUTE_ID --exact " + arg`,
			`"looptender fallbacks handoff on " + arg`,
			"looptender routes add --id ROUTE_ID --provider PROVIDER --model MODEL --group GROUP --enter ENTER",
			`"looptender routes check ROUTE_ID --exact " + arg`,
			"one real request and uses this account's quota",
		},
		"reorderBox": {
			`"Change the order"`,
			`"looptender routes move " + id + " --to PLACE"`,
			`"looptender fallbacks move " + id + " --to PLACE " + loopArg(l)`,
			"Show identifiers to see each route's ID",
		},
		"standardRouteBox": {
			`"Add a route for every loop"`,
			"--enter ENTER --account ACCOUNT_ID",
			"looptender routes follow-standard --loop LOOP_NAME",
		},
		// Every block ends with where to run it.
		"howEnd": {"IN_TERMINAL"},
		// Each copy button is described by the line saying what it is for.
		"howStep": {`copyCommand("div", command, focus, (describedBy ? describedBy + " " : "") + p.id)`},
	} {
		body := funcBody(t, js, name)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
	}
	for _, want := range []string{
		`var IN_TERMINAL = "Run in Terminal, not in a BB agent thread."`,
		`var NAMES_ONLY = "This names the account. It does not put it in any loop. To use it, add it to a loop's order."`,
		`var FIRST_RUN = "An account is a sign-in you already have, with a name you choose. A route is one model on one of your accounts. Each loop works down its own order of routes: it uses the first that has room, moves to the next when one runs out, and moves back up when it can."`,
		// The loop name comes quoted from the server; never built here.
		`function loopArg(l) { return l.loopArg || "--loop LOOP_NAME"; }`,
		// The first-run state, open; otherwise a closed disclosure.
		`pane.appendChild(emptyBox("No accounts selected here yet", FIRST_RUN));
      pane.appendChild(addAccountBox(true));`,
		"pane.appendChild(addAccountBox(false, unnamed.length > 0));",
		"pane.appendChild(standardRouteBox(true));",
		"pane.appendChild(standardRouteBox(false));",
		"how.appendChild(addRouteBox(l));",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("dock.js lacks %q", want)
		}
	}
	// Nothing here posts, fetches or runs: the blocks only show text.
	for _, name := range []string{"addAccountBox", "addRouteBox", "reorderBox", "standardRouteBox", "howStep", "howBox"} {
		body := funcBody(t, js, name)
		for _, banned := range []string{"post(", "fetch(", "prepareChange(", "confirmChange("} {
			if strings.Contains(body, banned) {
				t.Errorf("%s calls %s", name, banned)
			}
		}
	}
	// Neutral, never a warning colour; a command wraps instead of overflowing.
	if r := cssRule(t, css, ".how"); strings.Contains(r, "--warn") || strings.Contains(r, "--attn") {
		t.Errorf(".how is coloured as a warning: %s", r)
	}
	if r := cssRule(t, css, ".how-step .command"); !strings.Contains(r, "white-space: normal") || !strings.Contains(r, "overflow-wrap: anywhere") {
		t.Errorf(".how-step .command: %s", r)
	}
	if r := cssRule(t, css, ".how-step .btn"); !strings.Contains(r, "white-space: nowrap") {
		t.Errorf(".how-step .btn: %s", r)
	}
}
