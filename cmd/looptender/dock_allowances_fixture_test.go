package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

// This fictional projection also supplies the browser fixture when explicitly
// requested. It reads no registry, catalog, provider or live quota cache.
func TestDockAllowancesBrowserFixture(t *testing.T) {
	now := dockNow
	reset := now.Add(3 * time.Hour)
	reading := func(age time.Duration, used int, stale bool) dockUsageTarget {
		u := acctReading(age, used, &reset)
		u.ReadLine = "Usage read " + dockAgo(now, *u.ObservedAt)
		u.Windows[0].Name = "Five-hour"
		u.Windows[0].Reset = txt("Resets in 3 h")
		if stale {
			ended := now.Add(-time.Hour)
			u.Windows[0].ResetsAt = &ended
			u.Availability = "stale"
			u.Windows[0].Stale = true
			u.Windows[0].Reset = txt("That window ended")
		}
		return u
	}
	none := dockUsageTarget{Availability: "unavailable", ReadLine: "No usage reading", Reason: "not recorded", Windows: []dockUsageWindow{}}
	label := "Example selected " + strings.Repeat("A", 80)
	a := &accounts.RouteAccount{Assigned: "by_you", Account: acctWork, Label: label, Check: accounts.RouteConsistent}
	routes := []dockRoute{
		{Key: "r1", Provider: "codex", Account: "example-assigned", Title: "example-small", identity: "shared-assigned", Usage: reading(20*time.Minute, 21, false)},
		{Key: "r2", Provider: "codex", Account: "example-assigned", Title: strings.Repeat("R", 80), identity: "shared-assigned", Usage: reading(2*time.Hour, 100, false)},
		{Key: "r3", Provider: "codex", Account: "example-assigned", Title: "example-missing", identity: "shared-assigned", Usage: none},
		{Key: "r4", Provider: "codex", Account: "example-unassigned", Title: "example-old", identity: "shared-unknown", Usage: reading(24*time.Hour, 93, true)},
		{Key: "r5", Provider: "codex", Account: "example-unassigned", Title: "example-missing", identity: "shared-unknown", Usage: none},
		{Key: "r6", Provider: "codex", Account: "example-unassigned", Title: strings.Repeat("U", 80), identity: "shared-unknown", Usage: reading(5*time.Minute, 73, false)},
		{Key: "r7", Provider: "codex", Account: "example-mismatch", Title: "example-mismatched", identity: "shared-unknown", Usage: reading(time.Hour, 44, false)},
	}
	entries := []dockChainEntry{}
	for i, r := range routes {
		ra := &accounts.RouteAccount{Context: "shared-unknown"}
		if i < 3 {
			ra = a
		}
		if i == 6 {
			ra = &accounts.RouteAccount{Assigned: "by_you", Account: acctOther, Label: "Example other", Check: accounts.RouteProviderMismatch}
		}
		entries = append(entries, dockChainEntry{Route: r.Key, Title: r.Title, Account: r.Account, Provider: r.Provider, CatalogAccount: ra, Usage: r.Usage})
	}
	loops := []dockLoop{{Key: "exampleloop", Name: "Example loop", State: "paused", Chain: dockChain{Entries: entries}}}
	allow, omitted := dockAllowances(routes, loops, now)
	q := &accounts.QuotaView{State: "reported", Source: "Fictional selected-context account/rateLimits/read", ObservedAt: &now, Freshness: "fresh", Windows: []accounts.QuotaWindow{{Name: "primary", UsedPercent: 37}}}
	view := &accounts.View{State: "available", Complete: true, Cards: []accounts.Card{{ID: acctWork, Label: label, Provider: "codex", Selection: "inherited", Quota: q}}}
	report := dockReport{SchemaVersion: 1, ReadOnly: true, ReadAt: now, RefreshSeconds: 15, Registry: dockRegistry{Availability: "available"}, Complete: true, AllClear: true, Loops: loops, Routes: routes, Accounts: view, Allowances: allow, AllowancesOmitted: omitted, NeedsYou: []dockNeed{}, NeedsLine: "Nothing needs you", RoutesSummary: dockRoutesLine{Picker: "7 configured routes"}}
	report.AccountUses = []dockAccountUse{{Card: acctWork, Routes: []dockAccountRoute{{Route: "r1", Title: "example-small", Loops: []string{"Example loop"}}}}}
	if len(allow) != 5 || allow[0].Assigned != "by_you" || len(allow[0].Lines) != 3 {
		t.Fatalf("fictional strip: %+v", allow)
	}
	if path := os.Getenv("LOOPTENDER_ALLOWANCES_FIXTURE"); path != "" {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
