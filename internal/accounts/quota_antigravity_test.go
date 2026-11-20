package accounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func antigravityCacheFixture(t *testing.T) (string, Entry, []string) {
	t.Helper()
	dir, e, env := quotaFixture(t)
	if err := Update(dir, func(f *File) error {
		e.Provider = "antigravity"
		e.Context = Context(*f, e, env)
		f.Entries[0] = e
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return dir, e, env
}

func antigravityCacheAttempt(at time.Time) QuotaAttempt {
	five := int64(300)
	week := int64(10080)
	return QuotaAttempt{At: at, Details: &QuotaDetails{Buckets: []QuotaBucket{
		{Key: "gemini-5h", Windows: []QuotaMeter{{Name: "primary", UsedPercent: 0, DurationMinutes: &five, ResetsAt: &at}}},
		{Key: "gemini-weekly", Windows: []QuotaMeter{{Name: "primary", UsedPercent: 25, DurationMinutes: &week, ResetsAt: &at}}},
		{Key: "3p-5h", Windows: []QuotaMeter{{Name: "primary", UsedPercent: 10, DurationMinutes: &five, ResetsAt: &at}}},
		{Key: "3p-weekly", Windows: []QuotaMeter{{Name: "primary", UsedPercent: 40, DurationMinutes: &week, ResetsAt: &at}}},
	}}}
}

func TestAntigravityQuotaCacheRetrievalNeverProviderFresh(t *testing.T) {
	dir, e, env := antigravityCacheFixture(t)
	f, _ := Load(dir)
	bound, _ := Bound(f, e, env)
	at := time.Now().UTC().Add(-time.Hour)
	if err := PublishAntigravityQuota(dir, e, bound, antigravityCacheAttempt(at), env, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	view := QuotaViews(dir, f, env, at.Add(time.Second))[e.ID]
	if view.State != "reported" || view.Freshness != "provider_unknown" || view.Source != AntigravityQuotaSource || view.ObservedAt != nil || view.RetrievedAt == nil || !view.RetrievedAt.Equal(at) || view.Summary == nil || !strings.Contains(view.Summary.Text, "provider freshness unknown") {
		t.Fatalf("%+v", view)
	}
	names := map[string]bool{}
	for _, row := range view.Summary.Rows {
		names[row.Name] = true
	}
	for _, want := range []string{"Gemini · 5-hour", "Gemini · 7-day", "Claude/GPT · 5-hour", "Claude/GPT · 7-day"} {
		if !names[want] {
			t.Fatalf("missing %s in %#v", want, view.Summary.Rows)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, AntigravityQuotaName)); err != nil {
		t.Fatal(err)
	}
}
