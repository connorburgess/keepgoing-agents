package accounts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuotaDetailStoredMissingValuesRefused(t *testing.T) {
	for _, raw := range []string{`{"name":"primary"}`, `{"name":"primary","usedPercent":null}`, `{"name":"primary","usedPercent":0,"extra":1}`} {
		var w QuotaMeter
		if json.Unmarshal([]byte(raw), &w) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	var w QuotaMeter
	if json.Unmarshal([]byte(`{"name":"primary","usedPercent":0}`), &w) != nil || w.UsedPercent != 0 {
		t.Fatal(w)
	}
	for _, raw := range []string{`{"limit":"5","used":"1","resetsAt":"2030-03-04T12:00:00Z"}`, `{"limit":"5","used":"1","remainingPercent":null,"resetsAt":"2030-03-04T12:00:00Z"}`} {
		var i QuotaIndividual
		if json.Unmarshal([]byte(raw), &i) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
func TestQuotaSummaryLegacyAndStaleUnits(t *testing.T) {
	now := time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	s := SummarizeQuota(QuotaView{State: "reported", ObservedAt: &old, Source: QuotaSource, Freshness: "stale", Windows: []QuotaWindow{{Name: "primary", UsedPercent: 100, ResetsAt: &old}}}, now)
	if !s.Partial || !s.Rows[0].Ended || s.Rows[0].Text == "0% used" || !strings.Contains(s.Rows[1].Text, "Unknown") {
		t.Fatal(s)
	}
}

func TestQuotaLegacyReadIsInertAndDetailUpgradePreservesCatalog(t *testing.T) {
	dir, e, env := quotaFixture(t)
	now := time.Now().UTC()
	if err := quotaPublish(t, dir, e, env, QuotaAttempt{At: now, Windows: []QuotaWindow{{Name: "primary", UsedPercent: 0}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, QuotaName)
	q, err := LoadQuota(dir)
	if err != nil {
		t.Fatal(err)
	}
	q.SchemaVersion = 1
	raw, _ := json.Marshal(q)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, _ := os.ReadFile(filepath.Join(dir, FileName))
	f, _ := Load(dir)
	v := QuotaViews(dir, f, env, now.Add(time.Minute))[e.ID]
	after, _ := os.ReadFile(path)
	if !bytes.Equal(raw, after) || v.Summary == nil || !v.Summary.Partial || *v.Summary.Rows[0].UsedPercent != 0 {
		t.Fatal("legacy read rewrote or lost explicit zero", v)
	}
	d := &QuotaDetails{Buckets: []QuotaBucket{{Key: "codex", Windows: []QuotaMeter{{Name: "primary", UsedPercent: 17.5}}}}}
	if err := quotaPublish(t, dir, e, env, QuotaAttempt{At: now.Add(2 * time.Minute), Details: d}); err != nil {
		t.Fatal(err)
	}
	q, err = LoadQuota(dir)
	if err != nil || q.SchemaVersion != 2 {
		t.Fatal(q, err)
	}
	current, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !bytes.Equal(catalog, current) {
		t.Fatal("quota detail changed catalog")
	}
}
