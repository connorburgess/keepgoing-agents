package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

// Fictional data drives both consumers without querying installed providers.
func TestPanelAccountQuotaClaudeConsumerFixture(t *testing.T) {
	d, reason := accountClaudeQuotaParse([]byte(`{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":0,"resets_at":"2030-03-04T17:00:00Z"},"seven_day":{"utilization":64.5,"resets_at":"2030-03-10T12:00:00Z"},"seven_day_oauth_apps":{"utilization":12},"seven_day_opus":{"utilization":87},"seven_day_sonnet":{"utilization":25}}}`), nil)
	if reason != "" {
		t.Fatal(reason)
	}
	makeView := func(q accounts.QuotaView) *accounts.QuotaView {
		s := accounts.SummarizeQuota(q, dockNow)
		q.Summary = &s
		return &q
	}
	failed := dockNow.Add(time.Minute)
	claude := makeView(accounts.QuotaView{State: "reported", Source: accounts.ClaudeQuotaSource, RetrievedAt: &dockNow, Freshness: "provider_unknown", Details: d, FailedRefreshAt: &failed, FailedRefreshReason: "timeout"})
	if claude.ObservedAt != nil || claude.Summary.ObservedAt != nil || claude.Summary.RetrievedAt == nil || len(claude.Summary.Rows) != 6 {
		t.Fatal("Claude timestamp/scopes were substituted", claude)
	}
	reader, _ := dockFixtureReader(t)
	r := reader.read(context.Background(), false)
	r.Accounts = &accounts.View{State: "available", Complete: true, Cards: []accounts.Card{
		{ID: acctWork, Label: "Example Codex", Provider: "codex", Selection: "inherited", Quota: makeView(accounts.QuotaView{State: "reported", Source: accounts.QuotaSource, ObservedAt: &dockNow, Freshness: "fresh", Windows: []accounts.QuotaWindow{{Name: "primary", UsedPercent: 37}}})},
		{ID: acctOther, Label: "Example Claude " + strings.Repeat("A", 80), Provider: "claude-code", Selection: "inherited", Quota: claude},
		{ID: strings.Repeat("c", 32), Label: "Example unavailable", Provider: "claude-code", Selection: "inherited", Quota: makeView(accounts.QuotaView{State: "unavailable", Reason: "unverified_cli", Freshness: "unavailable"})},
	}}
	r.AccountUses = []dockAccountUse{{Context: "fictional-unbound", Routes: []dockAccountRoute{{Route: r.Routes[0].Key, Title: "Example dated route", Loops: []string{"Example loop"}}}}}
	r.Routes[0].Usage = acctReading(time.Hour, 73, nil)
	list := buildPanelList(r)
	if list.AccountQuotas == nil || len(list.AccountQuotas.Accounts) != 3 {
		t.Fatal("native projection lost selected accounts")
	}
	if path := os.Getenv("LOOPTENDER_CLAUDE_CONSUMER_FIXTURE"); path != "" {
		for name, value := range map[string]any{path: r, path + ".native.json": list} {
			raw, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPanelAccountQuotaProviderCapability(t *testing.T) {
	for _, platform := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "arm64"}, {"linux", "amd64"}, {"windows", "arm64"}, {"windows", "amd64"}} {
		want := []string{"codex"}
		if platform == [2]string{"darwin", "arm64"} {
			want = append(want, "claude-code")
		}
		if got := panelQuotaProvidersFor(platform[0], platform[1]); !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: %v", platform, got)
		}
	}
}

func TestPanelAccountQuotaClaudeExplicitReviewAndFreshness(t *testing.T) {
	dir, entry := accountFixture(t, "claude-code")
	c, err := newPanelChanges(context.Background(), "", &dockReader{accountsDir: dir}, time.Now(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.quotaRun = func(_ context.Context, provider, cli string, env []string, cwd string) ([]byte, error) {
		calls++
		if provider != "claude-code" || cli != entry.CLI || cwd != entry.Workdir {
			t.Fatal("selected execution context changed")
		}
		return []byte(claudeQuotaUsage), nil
	}
	p := c.prepareAccountQuota("session", entry.ID)
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if p.Preview != nil || p.Result == nil || calls != 0 {
			t.Fatal("unsupported platform advertised or queried Claude", p, calls)
		}
		return
	}
	if p.Preview == nil || calls != 0 {
		t.Fatal("review queried provider or refused supported platform", p, calls)
	}
	lines := ""
	for _, line := range p.Preview.Lines {
		lines += line.render(time.Now())
	}
	for _, required := range []string{"2.1.326", "Darwin arm64", "disables hooks", "no model request", "transcript scan", "cached or seeded", "Provider freshness is unknown"} {
		if !strings.Contains(lines, required) {
			t.Fatalf("missing review disclosure %q: %s", required, lines)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, accounts.ClaudeQuotaName)); !os.IsNotExist(err) {
		t.Fatal("preparation wrote quota evidence", err)
	}
	r := c.confirmAccountQuota("session", entry.ID, p.Preview.Form)
	if r.Result == nil || r.Result.Outcome != "done" || calls != 1 {
		t.Fatal(r, calls)
	}
	catalog, err := accounts.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := accounts.QuotaViews(dir, catalog, c.quotaEnv, time.Now())[entry.ID]
	if v.Freshness != "provider_unknown" || v.ObservedAt != nil || v.RetrievedAt == nil {
		t.Fatalf("retrieval became provider-fresh: %+v", v)
	}
	r = c.confirmAccountQuota("session", entry.ID, p.Preview.Form)
	if r.Result == nil || r.Result.Outcome != "review_again" || calls != 1 {
		t.Fatal("replayed Claude read", r, calls)
	}
}
