package accounts

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func quotaFixture(t *testing.T) (string, Entry, []string) {
	t.Helper()
	dir, e := fixture(t)
	env := []string{"HOME=/example", "OPENAI_API_KEY=fictional-private-value"}
	if err := Update(dir, func(f *File) error {
		e.Provider = "codex"
		e.Context = Context(*f, e, env)
		f.Entries[0] = e
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return dir, e, env
}
func quotaPublish(t *testing.T, dir string, e Entry, env []string, a QuotaAttempt) error {
	t.Helper()
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	bound, _ := Bound(f, e, env)
	return PublishQuota(dir, e, bound, a, env, func() error { return nil })
}

func TestQuotaCacheFreshnessAndLegacyCatalog(t *testing.T) {
	dir, e, env := quotaFixture(t)
	catalog, _ := os.ReadFile(filepath.Join(dir, FileName))
	at := time.Now().UTC().Add(-2 * time.Hour)
	reset := at.Add(time.Hour)
	a := QuotaAttempt{At: at, Windows: []QuotaWindow{{Name: "primary", UsedPercent: 0, ResetsAt: &reset}}}
	if err := quotaPublish(t, dir, e, env, a); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(dir)
	v := QuotaViews(dir, f, env, at.Add(10*time.Minute))[e.ID]
	if v.State != "reported" || v.Freshness != "fresh" || len(v.Windows) != 1 || v.Windows[0].UsedPercent != 0 {
		t.Fatal(v)
	}
	if err := quotaPublish(t, dir, e, env, QuotaAttempt{At: at.Add(90 * time.Minute), Reason: "timeout"}); err != nil {
		t.Fatal(err)
	}
	v = QuotaViews(dir, f, env, at.Add(2*time.Hour))[e.ID]
	if v.State != "reported" || v.Freshness != "stale" || v.FailedRefreshReason != "timeout" || !v.ObservedAt.Equal(at) || *v.AgeSeconds != 7200 || !v.Windows[0].ResetsAt.Equal(reset) {
		t.Fatal(v)
	}
	if err := quotaPublish(t, dir, e, env, a); !errors.Is(err, ErrChanged) {
		t.Fatal("old response published", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !bytes.Equal(catalog, after) || f.SchemaVersion != 1 {
		t.Fatal("quota refresh rewrote/promoted catalog")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, QuotaName))
	if bytes.Contains(raw, []byte("fictional-private-value")) || bytes.Contains(raw, []byte("/example")) {
		t.Fatal("private context leaked")
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, QuotaName))
		if st.Mode().Perm() != 0600 {
			t.Fatal(st.Mode())
		}
	}
}

func TestQuotaPublicationFences(t *testing.T) {
	for _, kind := range []string{"revision", "removed", "environment", "executable", "verify"} {
		t.Run(kind, func(t *testing.T) {
			dir, e, env := quotaFixture(t)
			f, _ := Load(dir)
			bound, _ := Bound(f, e, env)
			at := time.Now().UTC()
			a := QuotaAttempt{At: at, Windows: []QuotaWindow{{Name: "primary", UsedPercent: 25}}}
			if err := PublishQuota(dir, e, bound, a, env, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, QuotaName))
			verify := func() error { return nil }
			if kind == "environment" {
				env = []string{"HOME=/different"}
			} else if kind == "verify" {
				verify = func() error { return ErrChanged }
			} else {
				if err := Update(dir, func(f *File) error {
					switch kind {
					case "removed":
						f.Entries = []Entry{}
					case "revision":
						f.Entries[0].Revision = token()
					case "executable":
						f.Entries[0].CLI = filepath.Join(dir, "replacement")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := PublishQuota(dir, e, bound, a, env, verify); !errors.Is(err, ErrChanged) {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(filepath.Join(dir, QuotaName))
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed cache")
			}
		})
	}
}

func TestQuotaDamagedEvidenceRefusesReplacement(t *testing.T) {
	for _, kind := range []string{"corrupt", "future", "unknown", "duplicate", "oversize", "symlink", "mode"} {
		t.Run(kind, func(t *testing.T) {
			dir, e, env := quotaFixture(t)
			p := filepath.Join(dir, QuotaName)
			raw := []byte(`{"schemaVersion":1,"entries":[]}`)
			switch kind {
			case "corrupt":
				raw = []byte("{")
			case "future":
				raw = []byte(`{"schemaVersion":99,"entries":[]}`)
			case "unknown":
				raw = []byte(`{"schemaVersion":1,"entries":[],"private":"fictional"}`)
			case "duplicate":
				raw = []byte(`{"schemaVersion":1,"schemaVersion":1,"entries":[]}`)
			case "oversize":
				raw = make([]byte, MaxBytes+1)
			}
			if err := os.WriteFile(p, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "mode" {
				if runtime.GOOS == "windows" {
					t.Skip("Unix mode")
				}
				_ = os.Chmod(p, 0644)
			}
			if kind == "symlink" {
				target := p + ".original"
				_ = os.Rename(p, target)
				if os.Symlink(target, p) != nil {
					t.Skip("symlink unavailable")
				}
			}
			if _, err := LoadQuota(dir); err == nil {
				t.Fatal("damaged cache accepted")
			}
			if err := quotaPublish(t, dir, e, env, QuotaAttempt{At: time.Now(), Reason: "timeout"}); err == nil {
				t.Fatal("damaged cache replaced")
			}
			after, _ := os.ReadFile(p)
			if !bytes.Equal(raw, after) {
				t.Fatal("damaged bytes changed")
			}
		})
	}
}

func TestQuotaDoesNotJoinChangedContext(t *testing.T) {
	dir, e, env := quotaFixture(t)
	at := time.Now().UTC()
	if err := quotaPublish(t, dir, e, env, QuotaAttempt{At: at, Windows: []QuotaWindow{{Name: "secondary", UsedPercent: 70}}}); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(dir)
	v := QuotaViews(dir, f, []string{"HOME=/other"}, at)[e.ID]
	if v.State != "unavailable" || v.Reason != "selection_review_required" || len(v.Windows) != 0 {
		t.Fatal(v)
	}
	v = QuotaViews(dir, f, env, at.Add(-time.Second))[e.ID]
	if v.State != "unavailable" || len(v.Windows) != 0 {
		t.Fatal(v)
	}
}

func TestQuotaRejectsDivergentSuccessfulAttempt(t *testing.T) {
	dir, e, env := quotaFixture(t)
	a := QuotaAttempt{At: time.Now().UTC(), Windows: []QuotaWindow{{Name: "primary", UsedPercent: 1}}}
	if err := quotaPublish(t, dir, e, env, a); err != nil {
		t.Fatal(err)
	}
	q, err := LoadQuota(dir)
	if err != nil {
		t.Fatal(err)
	}
	q.Entries[0].Attempt.Windows = []QuotaWindow{{Name: "primary", UsedPercent: 99}}
	raw, _ := json.Marshal(q)
	p := filepath.Join(dir, QuotaName)
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadQuota(dir); !errors.Is(err, ErrInvalid) {
		t.Fatal("divergent success accepted", err)
	}
}

func TestQuotaRefreshPreservesUnrelatedSelection(t *testing.T) {
	dir, first, env := quotaFixture(t)
	var second Entry
	if err := Update(dir, func(f *File) error {
		second = NewEntry(*f, first, "", time.Now().UTC(), env)
		second.Label = "Example second"
		f.Entries = append(f.Entries, second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute)
	a := QuotaAttempt{At: at, Windows: []QuotaWindow{{Name: "primary", UsedPercent: 17}}}
	if err := quotaPublish(t, dir, second, env, a); err != nil {
		t.Fatal(err)
	}
	before, err := LoadQuota(dir)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(before.Entries[0])
	a.Windows[0].UsedPercent = 84
	if err := quotaPublish(t, dir, first, env, a); err != nil {
		t.Fatal(err)
	}
	after, err := LoadQuota(dir)
	if err != nil {
		t.Fatal(err)
	}
	retained, _ := json.Marshal(after.Entries[0])
	if len(after.Entries) != 2 || !bytes.Equal(original, retained) {
		t.Fatal("unrelated quota evidence changed")
	}
}
