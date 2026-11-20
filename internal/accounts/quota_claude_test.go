package accounts

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func claudeCacheFixture(t *testing.T) (string, Entry, []string) {
	t.Helper()
	dir, e, env := quotaFixture(t)
	if err := Update(dir, func(f *File) error {
		e.Provider = "claude-code"
		e.Context = Context(*f, e, env)
		f.Entries[0] = e
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return dir, e, env
}

func claudeCacheAttempt(at time.Time) QuotaAttempt {
	minutes := int64(300)
	return QuotaAttempt{At: at, Details: &QuotaDetails{Buckets: []QuotaBucket{{Key: "five_hour", Windows: []QuotaMeter{{Name: "primary", UsedPercent: 0, DurationMinutes: &minutes}}}}}}
}

func TestClaudeQuotaCacheRetrievalNeverProviderFresh(t *testing.T) {
	dir, e, env := claudeCacheFixture(t)
	f, _ := Load(dir)
	bound, _ := Bound(f, e, env)
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	// Even corrupt unrelated Codex evidence cannot be replaced by this read.
	codex := []byte("unrelated malformed Codex cache")
	if err := os.WriteFile(filepath.Join(dir, QuotaName), codex, 0600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-3 * time.Hour)
	if err := PublishClaudeQuota(dir, e, bound, claudeCacheAttempt(at), env, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	view := QuotaViews(dir, f, env, at.Add(time.Second))[e.ID]
	if view.State != "reported" || view.Freshness != "provider_unknown" || view.Source != ClaudeQuotaSource || view.ObservedAt != nil || view.RetrievedAt == nil || !view.RetrievedAt.Equal(at) || view.Summary == nil || view.Summary.RetrievedAt == nil || view.Summary.ObservedAt != nil || !strings.Contains(view.Summary.Text, "provider freshness unknown") || view.Summary.Rows[0].Name != "5-hour" || *view.Summary.Rows[0].UsedPercent != 0 {
		t.Fatalf("retrieval became a provider observation: %+v", view)
	}
	if err := PublishClaudeQuota(dir, e, bound, QuotaAttempt{At: at.Add(2 * time.Hour), Reason: "timeout"}, env, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	view = QuotaViews(dir, f, env, at.Add(3*time.Hour))[e.ID]
	if view.Freshness != "provider_unknown" || !view.RetrievedAt.Equal(at) || view.ObservedAt != nil || view.FailedRefreshReason != "timeout" || !view.FailedRefreshAt.Equal(at.Add(2*time.Hour)) {
		t.Fatal("failure refreshed/removed prior retrieval", view)
	}
	if err := PublishClaudeQuota(dir, e, bound, claudeCacheAttempt(at), env, func() error { return nil }); !errors.Is(err, ErrChanged) {
		t.Fatal("out-of-order reply replaced newer attempt", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ClaudeQuotaName))
	if bytes.Contains(raw, []byte("observedAt")) || !bytes.Contains(raw, []byte("retrievedAt")) || bytes.Contains(raw, []byte("fictional-private-value")) {
		t.Fatal("provider observation/private data retained", string(raw))
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	codexAfter, _ := os.ReadFile(filepath.Join(dir, QuotaName))
	if !bytes.Equal(before, after) || !bytes.Equal(codex, codexAfter) {
		t.Fatal("Claude publication changed catalog or Codex evidence")
	}
}

func TestClaudeQuotaCacheCorruptionRefusesUnchanged(t *testing.T) {
	for _, kind := range []string{"absent-percent", "null-percent", "alias", "future", "fake-observation", "unknown-scope", "wrong-duration", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir, e, env := claudeCacheFixture(t)
			f, _ := Load(dir)
			bound, _ := Bound(f, e, env)
			at := time.Now().UTC()
			if err := PublishClaudeQuota(dir, e, bound, claudeCacheAttempt(at), env, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ClaudeQuotaName)
			raw, _ := os.ReadFile(path)
			s := string(raw)
			switch kind {
			case "absent-percent":
				s = strings.ReplaceAll(s, `,"usedPercent":0`, "")
			case "null-percent":
				s = strings.ReplaceAll(s, `"usedPercent":0`, `"usedPercent":null`)
			case "alias":
				s = strings.ReplaceAll(s, `"retrievedAt":`, `"RetrievedAt":`)
			case "future":
				s = strings.Replace(s, `"schemaVersion":1`, `"schemaVersion":2`, 1)
			case "fake-observation":
				s = strings.ReplaceAll(s, `"retrievedAt":`, `"observedAt":`)
			case "unknown-scope":
				s = strings.ReplaceAll(s, `"five_hour"`, `"private-model"`)
			case "wrong-duration":
				s = strings.ReplaceAll(s, `"durationMinutes":300`, `"durationMinutes":10080`)
			case "symlink":
				other := filepath.Join(dir, "fictional-evidence")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Skip("symlink unavailable", err)
				}
			}
			if kind != "symlink" {
				if err := os.WriteFile(path, []byte(s), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadClaudeQuota(dir); !errors.Is(err, ErrInvalid) {
				t.Fatal("damaged Claude cache accepted", err)
			}
			if err := PublishClaudeQuota(dir, e, bound, claudeCacheAttempt(at.Add(time.Second)), env, func() error { return nil }); !errors.Is(err, ErrInvalid) {
				t.Fatal("damaged cache repaired silently", err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, []byte(s)) || QuotaViews(dir, f, env, at)[e.ID].Reason != "cache_unavailable" {
				t.Fatal("damage overwritten or displayed as usage")
			}
		})
	}
}
