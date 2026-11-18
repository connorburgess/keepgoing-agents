package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

const detailedQuota = `{"ordinaryUsageAllowed":null,"rateLimits":{"primary":{"usedPercent":99}},"rateLimitsByLimitId":{"codex":{"limitId":"codex","limitName":"private@example.com","primary":{"usedPercent":37.5,"windowDurationMins":300,"resetsAt":1900000000},"secondary":{"usedPercent":82,"windowDurationMins":10080,"resetsAt":1900500000},"credits":{"hasCredits":true,"unlimited":false,"balance":"12.75"},"individualLimit":{"limit":"100.00","used":"12.50","remainingPercent":87.5,"resetsAt":1900500000}},"example-meter":{"limitId":"example-meter","normalModelSlug":"private-model-alias","primary":{"usedPercent":0,"windowDurationMins":60}}},"accountId":"private-id","rateLimitUpsell":{"private":"banner"}}`

func TestAccountQuotaDetailsIndependentBucketsAndPrivacy(t *testing.T) {
	d, reason := accountQuotaDetailsParse([]byte(detailedQuota), nil)
	if reason != "" || len(d.Buckets) != 2 || len(d.Buckets[0].Windows) != 2 || d.Buckets[0].Windows[0].UsedPercent != 37.5 || d.Buckets[1].Windows[0].UsedPercent != 0 || d.OrdinaryUsageAllowed != nil {
		t.Fatalf("%+v %s", d, reason)
	}
	raw, _ := json.Marshal(d)
	for _, secret := range []string{"private@example.com", "private-model-alias", "private-id", "banner", "99"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("private/legacy data retained: %s", raw)
		}
	}
	v := accounts.QuotaView{State: "reported", Source: accounts.QuotaSource, ObservedAt: &dockNow, Freshness: "fresh", Details: d}
	s := accounts.SummarizeQuota(v, dockNow)
	raw, _ = json.Marshal(s)
	if len(s.Rows) != 6 || s.Rows[0].Name != "Codex · 5-hour" || *s.Rows[0].RemainingPercent != 62.5 || !strings.Contains(s.Rows[2].Text, "provider units") || strings.Contains(string(raw), "USD") || !strings.Contains(s.Rows[len(s.Rows)-1].Text, "Unknown") {
		t.Fatalf("%s", raw)
	}
}
func TestAccountQuotaDetailsPartialAndMissingNotZero(t *testing.T) {
	for _, raw := range []string{
		`{"rateLimits":{"primary":{"usedPercent":null},"secondary":{"usedPercent":7}}}`,
		`{"rateLimits":{"primary":{"future":5},"secondary":{"usedPercent":7}}}`,
		`{"rateLimits":{"primary":{"usedPercent":101},"secondary":{"usedPercent":7}}}`,
		`{"rateLimits":{"primary":{"usedPercent":7,"extra":8}}}`,
		`{"rateLimits":{"credits":{"hasCredits":true}}}`,
		`{"rateLimits":{"individualLimit":{"limit":"99","used":"1","remainingPercent":null,"resetsAt":1900000000}}}`,
	} {
		d, reason := accountQuotaDetailsParse([]byte(raw), nil)
		if reason != "" || !d.Partial {
			t.Errorf("partial response silently accepted/refused: %+v %s", d, reason)
		}
		for _, b := range d.Buckets {
			for _, w := range b.Windows {
				if w.UsedPercent == 0 {
					t.Errorf("missing became zero: %s", raw)
				}
			}
		}
	}
	for _, raw := range []string{`{"rateLimits":{"primary":{"usedPercent":1,"usedPercent":2}}}`, `{"rateLimitsByLimitId":{"private@example.com":{"primary":{"usedPercent":2}}}}`} {
		if d, reason := accountQuotaDetailsParse([]byte(raw), nil); reason == "" {
			t.Errorf("malformed/private key accepted: %+v", d)
		}
	}
}
func TestAccountQuotaDetailsOmissionAndLegacyFallback(t *testing.T) {
	buckets := map[string]any{}
	for i := range 12 {
		buckets[strings.Repeat("a", i+1)] = map[string]any{"primary": map[string]any{"usedPercent": i}}
	}
	raw, _ := json.Marshal(map[string]any{"rateLimitsByLimitId": buckets})
	d, reason := accountQuotaDetailsParse(raw, nil)
	if reason != "" || len(d.Buckets) != 8 || d.Omitted != 4 || !d.Partial {
		t.Fatalf("%+v %s", d, reason)
	}
	d, reason = accountQuotaDetailsParse([]byte(`{"rateLimits":{"primary":{"usedPercent":0}}}`), nil)
	if reason != "" || len(d.Buckets) != 1 || d.Buckets[0].Windows[0].UsedPercent != 0 {
		t.Fatal(d, reason)
	}
}
func TestQuotaSummaryAndNativeFixture(t *testing.T) {
	d, reason := accountQuotaDetailsParse([]byte(detailedQuota), nil)
	if reason != "" {
		t.Fatal(reason)
	}
	// Reset dates relative to the fictional reading, including independent ended windows.
	for i := range d.Buckets[0].Windows {
		at := dockNow.Add(time.Duration(i+1) * time.Hour)
		d.Buckets[0].Windows[i].ResetsAt = &at
	}
	older := dockNow.Add(-48 * time.Hour)
	failed := dockNow.Add(-time.Minute)
	makeView := func(q accounts.QuotaView) *accounts.QuotaView {
		s := accounts.SummarizeQuota(q, dockNow)
		q.Summary = &s
		return &q
	}
	cards := []accounts.Card{
		{ID: acctWork, Label: "Example Codex", Provider: "codex", Selection: "inherited", Quota: makeView(accounts.QuotaView{State: "reported", Source: accounts.QuotaSource, ObservedAt: &dockNow, Freshness: "fresh", Details: d})},
		{ID: acctOther, Label: "Example Claude", Provider: "claude-code", Selection: "inherited", Quota: makeView(accounts.QuotaView{State: "unavailable", Reason: "unsupported_provider", Freshness: "unavailable"})},
		{ID: strings.Repeat("c", 32), Label: "Example older " + strings.Repeat("A", 80), Provider: "codex", Selection: "inherited", Quota: makeView(accounts.QuotaView{State: "reported", Source: accounts.QuotaSource, ObservedAt: &older, Freshness: "stale", FailedRefreshAt: &failed, FailedRefreshReason: "timeout", Windows: []accounts.QuotaWindow{{Name: "primary", UsedPercent: 100, ResetsAt: &older}}})},
		{ID: strings.Repeat("d", 32), Label: "Example partial", Provider: "codex", Selection: "inherited", Quota: makeView(accounts.QuotaView{State: "reported", Source: accounts.QuotaSource, ObservedAt: &dockNow, Freshness: "fresh", Details: &accounts.QuotaDetails{Partial: true, Buckets: []accounts.QuotaBucket{{Key: "codex", Partial: true, Windows: []accounts.QuotaMeter{}}}}})},
	}
	reader, _ := dockFixtureReader(t)
	r := reader.read(context.Background(), false)
	r.Accounts = &accounts.View{State: "available", Complete: true, Cards: cards}
	r.AccountUses = []dockAccountUse{}
	r.Routes[0].Usage = acctReading(time.Hour, 73, nil)
	r.AccountUses = append(r.AccountUses, dockAccountUse{Context: "fictional-unbound", Routes: []dockAccountRoute{{Route: r.Routes[0].Key, Title: "Example route reading", Loops: []string{"Example loop"}}}})
	list := buildPanelList(r)
	if list.AccountQuotas == nil || len(list.AccountQuotas.Accounts) != 4 {
		t.Fatal(list.AccountQuotas)
	}
	raw, _ := json.Marshal(list.AccountQuotas)
	if strings.Contains(string(raw), "private@example") || strings.Contains(string(raw), "73") || strings.Contains(string(raw), "accountId") {
		t.Fatalf("identity/route leak: %s", raw)
	}
	if path := os.Getenv("LOOPTENDER_QUOTA_FIXTURE"); path != "" {
		raw, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		raw, _ = json.Marshal(list)
		if err := os.WriteFile(path+".native.json", raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQuotaMaximalWebAndNativeBounds(t *testing.T) {
	inputs := make([]dockInput, dockMaxLoops)
	for i := range inputs {
		inputs[i] = dockMaximalInput(i)
	}
	r := buildDockReport(inputs, dockNow, time.Minute, defaultStaleAfter, true, true, 0, true)
	d := &accounts.QuotaDetails{Buckets: []accounts.QuotaBucket{}}
	balance := strings.Repeat("9", 24) + "." + strings.Repeat("9", 12)
	reached := true
	duration := int64(525600)
	reset := dockNow.Add(time.Hour)
	for i := range accounts.MaxQuotaBuckets {
		d.Buckets = append(d.Buckets, accounts.QuotaBucket{Key: strings.Repeat("b", i+1), Windows: []accounts.QuotaMeter{{Name: "primary", UsedPercent: 99.9, DurationMinutes: &duration, ResetsAt: &reset}, {Name: "secondary", UsedPercent: 99.9, DurationMinutes: &duration, ResetsAt: &reset}}, Individual: &accounts.QuotaIndividual{Limit: balance, Used: balance, RemainingPercent: 0.1, ResetsAt: reset}, Credits: &accounts.QuotaCredit{HasCredits: true, Unlimited: false, Balance: &balance}, SpendControlReached: &reached})
	}
	if !accounts.ValidQuotaDetails(d) {
		t.Fatal("invalid maximum fixture")
	}
	r.Accounts = &accounts.View{State: "available", Complete: true, Cards: []accounts.Card{}}
	for i := range accounts.MaxEntries {
		q := accounts.QuotaView{State: "reported", Source: accounts.QuotaSource, ObservedAt: &dockNow, Freshness: "fresh", Details: d}
		summary := accounts.SummarizeQuota(q, dockNow)
		q.Summary = &summary
		r.Accounts.Cards = append(r.Accounts.Cards, accounts.Card{ID: fmt.Sprintf("%032x", i), Label: strings.Repeat("\U0001D4B3", 60), Provider: "codex", Quota: &q})
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > dockMaxResponse {
		t.Fatalf("web response %d exceeds %d", len(raw), dockMaxResponse)
	}
	list := buildPanelList(r)
	native, _ := json.Marshal(list)
	if len(native) > 1<<20 || len(list.AccountQuotas.Accounts) != 16 || list.AccountQuotas.Omitted != 48 {
		t.Fatalf("native%d bytes: %+v", len(native), list.AccountQuotas)
	}
	t.Logf("max registry +64 detailed quota selections: web%d/%d; native%d/%d bytes", len(raw), dockMaxResponse, len(native), 1<<20)
}
func TestAccountQuotaDetailsUnknownWindowKeepsRecognizedCredits(t *testing.T) {
	d, reason := accountQuotaDetailsParse([]byte(`{"rateLimits":{"primary":{"future":7},"credits":{"hasCredits":true,"unlimited":false,"balance":"2.50"}}}`), nil)
	if reason != "" || !d.Partial || d.Buckets[0].Credits == nil || len(d.Buckets[0].Windows) != 0 {
		t.Fatalf("%+v %s", d, reason)
	}
}
