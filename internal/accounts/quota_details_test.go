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

func TestQuotaCacheRejectsNoncanonicalFieldsWithoutPublication(t *testing.T) {
	for _, pair := range [][2]string{
		{`"usedPercent":37.5`, `"usedPercent":37.5,"UsedPercent":0`},
		{`"hasCredits":true`, `"hasCredits":true,"HasCredits":false`},
		{`"remainingPercent":62.5`, `"remainingPercent":62.5,"RemainingPercent":0`},
		{`"ordinaryUsageAllowed":true`, `"ordinaryUsageAllowed":true,"OrdinaryUsageAllowed":false`},
		{`"partial":true`, `"partial":true,"Partial":false`},
		{`"buckets":`, `"Buckets":`},
		{`"schemaVersion":2`, `"schemaVersion":2,"SchemaVersion":1`},
	} {
		t.Run(pair[1], func(t *testing.T) {
			dir, e, env := quotaFixture(t)
			now := time.Now().UTC()
			yes := true
			d := &QuotaDetails{Partial: true, OrdinaryUsageAllowed: &yes, Buckets: []QuotaBucket{{Key: "codex", Windows: []QuotaMeter{{Name: "primary", UsedPercent: 37.5}}, Credits: &QuotaCredit{HasCredits: true}, Individual: &QuotaIndividual{Limit: "100", Used: "37.5", RemainingPercent: 62.5, ResetsAt: now.Add(time.Hour)}}}}
			a := QuotaAttempt{At: now, Details: d}
			if err := quotaPublish(t, dir, e, env, a); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, QuotaName)
			raw, _ := os.ReadFile(path)
			changed := []byte(strings.ReplaceAll(string(raw), pair[0], pair[1]))
			if bytes.Equal(raw, changed) {
				t.Fatal("fixture did not mutate")
			}
			if err := os.WriteFile(path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadQuota(dir); err == nil {
				t.Fatal("aliased field accepted")
			}
			if err := quotaPublish(t, dir, e, env, a); err == nil {
				t.Fatal("published over malformed evidence")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(changed, after) {
				t.Fatal("malformed evidence changed")
			}
		})
	}
}
