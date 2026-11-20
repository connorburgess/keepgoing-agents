package accounts

import (
	"errors"
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
	if _, err := os.Stat(filepath.Join(dir, QuotaName)); !os.IsNotExist(err) {
		t.Fatalf("unexpected codex quota file: %v", err)
	}
}

func TestAntigravityQuotaCachePartialAndOmittedRoundTrip(t *testing.T) {
	dir, e, env := antigravityCacheFixture(t)
	f, _ := Load(dir)
	bound, _ := Bound(f, e, env)
	at := time.Now().UTC().Add(-time.Minute)

	bad := antigravityCacheAttempt(at)
	bad.Details.Omitted = 1
	bad.Details.Partial = false
	if err := PublishAntigravityQuota(dir, e, bound, bad, env, func() error { return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("omitted without partial: got %v, want ErrInvalid", err)
	}

	good := antigravityCacheAttempt(at)
	good.Details.Partial = true
	good.Details.Omitted = 2
	if err := PublishAntigravityQuota(dir, e, bound, good, env, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	view := QuotaViews(dir, f, env, at.Add(time.Second))[e.ID]
	if view.State != "reported" || view.Summary == nil || !view.Summary.Partial || view.Summary.Omitted != 2 {
		t.Fatalf("partial/omitted summary: %+v", view.Summary)
	}
}

func TestAntigravityRejectsExplicitDirectoryAndNeverSetsClaudeConfigDir(t *testing.T) {
	dir, e, env := antigravityCacheFixture(t)
	if err := Update(dir, func(f *File) error {
		bad := e
		bad.Mode = "explicit_directory"
		bad.Directory = e.Workdir
		bad.Context = Context(*f, bad, env)
		f.Entries[0] = bad
		return nil
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for antigravity explicit_directory, got %v", err)
	}

	gotEnv := ExecutionEnvironment(Entry{Provider: "antigravity", Mode: "explicit_directory", Directory: e.Workdir}, []string{"HOME=/tmp/example"})
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") || strings.HasPrefix(kv, "CODEX_HOME=") {
			t.Fatalf("unexpected explicit directory env var for antigravity: %q", kv)
		}
	}
}

func TestAntigravityContextFingerprintsAgyAndAntigravityEnvOnly(t *testing.T) {
	dir, e, baseEnv := antigravityCacheFixture(t)
	f, _ := Load(dir)
	withAgy := append(append([]string{}, baseEnv...), "AGY_PROFILE=fictional")
	if Context(f, e, baseEnv) == Context(f, e, withAgy) {
		t.Fatal("expected AGY_ env var to change antigravity context")
	}
	codexEntry := e
	codexEntry.Provider = "codex"
	if Context(f, codexEntry, baseEnv) != Context(f, codexEntry, withAgy) {
		t.Fatal("AGY_ env var must not alter codex context digest")
	}
}
