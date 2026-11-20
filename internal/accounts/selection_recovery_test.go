package accounts

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuotaPreTagRecoveryDistinguishesSavedContext(t *testing.T) {
	dir, e, env := quotaFixture(t)
	// Genuine pre-tag selection compatibility stays intact. It is neither a
	// valid v2 protected binding nor a silent current-definition restamp.
	if err := Update(dir, func(f *File) error { e.Context = strings.Repeat("a", 64); f.Entries[0] = e; return nil }); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(dir)
	if protectedDefinition(f, e.Context) {
		t.Fatal("fixture unexpectedly protected")
	}
	at := time.Now().UTC()
	if err := quotaPublish(t, dir, e, env, QuotaAttempt{At: at, Windows: []QuotaWindow{{Name: "secondary", UsedPercent: 64}}}); err != nil {
		t.Fatal(err)
	}
	catalog, _ := os.ReadFile(filepath.Join(dir, FileName))
	cache, _ := os.ReadFile(filepath.Join(dir, QuotaName))
	v := QuotaViews(dir, f, env, at)[e.ID]
	if v.State != "reported" || v.Windows[0].UsedPercent != 64 {
		t.Fatal("same-context legacy evidence lost", v)
	}
	changed := append(append([]string{}, env...), "HTTPS_PROXY=https://example.com")
	v = QuotaViews(dir, f, changed, at)[e.ID]
	if v.Reason != "selection_review_required" || v.State != "unavailable" || len(v.Windows) != 0 {
		t.Fatal("mismatched legacy cache needs explicit selection review", v)
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	saved, _ := os.ReadFile(filepath.Join(dir, QuotaName))
	if !bytes.Equal(catalog, after) || !bytes.Equal(cache, saved) {
		t.Fatal("projection migrated private evidence")
	}
}
