package main

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

// Only the structured quota method is used: no reserve experiment capability,
// reset-credit consumption, model call, login, account switch or Claude RPC.
func accountQuotaDetailsParse(raw []byte, err error) (*accounts.QuotaDetails, string) {
	if err != nil {
		_, reason := accountCodexPlan(nil, err)
		return nil, reason
	}
	if len(raw) > 64<<10 || !accounts.QuotaJSONUnique(raw) {
		return nil, "schema_unavailable"
	}
	var response struct {
		RateLimits json.RawMessage            `json:"rateLimits"`
		ByID       map[string]json.RawMessage `json:"rateLimitsByLimitId"`
		Allowed    *bool                      `json:"ordinaryUsageAllowed"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, "schema_unavailable"
	}
	d := &accounts.QuotaDetails{Buckets: []accounts.QuotaBucket{}, OrdinaryUsageAllowed: response.Allowed}
	byID := response.ByID
	if byID == nil {
		key := "codex"
		var legacy struct {
			ID *string `json:"limitId"`
		}
		if len(response.RateLimits) > 0 && string(response.RateLimits) != "null" {
			if json.Unmarshal(response.RateLimits, &legacy) != nil {
				return nil, "schema_unavailable"
			}
			if legacy.ID != nil && *legacy.ID != "" {
				key = *legacy.ID
			}
		}
		byID = map[string]json.RawMessage{key: response.RateLimits}
	}
	if len(byID) > 256 {
		return nil, "schema_unavailable"
	}
	keys := make([]string, 0, len(byID))
	for k := range byID {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Keep the ordinary Codex bucket first without treating it as exhaustive.
	for i, k := range keys {
		if k == "codex" {
			keys = append([]string{k}, append(keys[:i], keys[i+1:]...)...)
			break
		}
	}
	for _, key := range keys {
		if len(d.Buckets) >= accounts.MaxQuotaBuckets {
			d.Omitted++
			d.Partial = true
			continue
		}
		raw := byID[key]
		if len(raw) == 0 || string(raw) == "null" {
			d.Partial = true
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			d.Partial = true
			continue
		}
		b := accounts.QuotaBucket{Key: key, Windows: []accounts.QuotaMeter{}}
		// limitName, normalModelSlug, planType and identifiers are not public
		// account/model identity. Never include raw display names or banners.
		for k := range fields {
			switch k {
			case "primary", "secondary", "limitId", "limitName", "normalModelSlug", "credits", "individualLimit", "spendControlReached", "planType", "rateLimitReachedType":
			default:
				b.Partial = true
			}
		}
		if id := fields["limitId"]; len(id) > 0 && string(id) != "null" {
			var got string
			if json.Unmarshal(id, &got) != nil || (got != "" && got != key) {
				d.Partial = true
				continue
			}
		}
		for _, name := range []string{"primary", "secondary"} {
			r := fields[name]
			if len(r) == 0 || string(r) == "null" {
				continue
			}
			var w struct {
				Used     *float64 `json:"usedPercent"`
				Duration *int64   `json:"windowDurationMins"`
				Reset    *int64   `json:"resetsAt"`
			}
			var wk map[string]json.RawMessage
			if json.Unmarshal(r, &wk) != nil || wk == nil || json.Unmarshal(r, &w) != nil || w.Used == nil {
				b.Partial = true
				continue
			}
			known := true
			for k := range wk {
				if k != "usedPercent" && k != "windowDurationMins" && k != "resetsAt" {
					known = false
				}
			}
			m := accounts.QuotaMeter{Name: name, UsedPercent: *w.Used, DurationMinutes: w.Duration}
			if w.Reset != nil {
				t := time.Unix(*w.Reset, 0).UTC()
				m.ResetsAt = &t
			}
			test := &accounts.QuotaDetails{Buckets: []accounts.QuotaBucket{{Key: key, Windows: []accounts.QuotaMeter{m}}}}
			if !known || !accounts.ValidQuotaDetails(test) {
				b.Partial = true
				continue
			}
			b.Windows = append(b.Windows, m)
		}
		if r := fields["credits"]; len(r) > 0 && string(r) != "null" {
			var c struct {
				Has       *bool   `json:"hasCredits"`
				Unlimited *bool   `json:"unlimited"`
				Balance   *string `json:"balance"`
			}
			var ck map[string]json.RawMessage
			if json.Unmarshal(r, &c) == nil && json.Unmarshal(r, &ck) == nil && len(ck) <= 3 && c.Has != nil && c.Unlimited != nil {
				creditsKnown := true
				for k := range ck {
					if k != "hasCredits" && k != "unlimited" && k != "balance" {
						creditsKnown = false
						b.Partial = true
					}
				}
				b.Credits = &accounts.QuotaCredit{HasCredits: *c.Has, Unlimited: *c.Unlimited, Balance: c.Balance}
				if !creditsKnown || !accounts.ValidQuotaDetails(&accounts.QuotaDetails{Buckets: []accounts.QuotaBucket{b}}) {
					b.Credits = nil
					b.Partial = true
				}
			} else {
				b.Partial = true
			}
		}
		if r := fields["individualLimit"]; len(r) > 0 && string(r) != "null" {
			var i struct {
				Limit     *string  `json:"limit"`
				Used      *string  `json:"used"`
				Remaining *float64 `json:"remainingPercent"`
				Reset     *int64   `json:"resetsAt"`
			}
			var ik map[string]json.RawMessage
			if json.Unmarshal(r, &i) == nil && json.Unmarshal(r, &ik) == nil && len(ik) == 4 && i.Limit != nil && i.Used != nil && i.Remaining != nil && i.Reset != nil {
				b.Individual = &accounts.QuotaIndividual{Limit: *i.Limit, Used: *i.Used, RemainingPercent: *i.Remaining, ResetsAt: time.Unix(*i.Reset, 0).UTC()}
				if !accounts.ValidQuotaDetails(&accounts.QuotaDetails{Buckets: []accounts.QuotaBucket{b}}) {
					b.Individual = nil
					b.Partial = true
				}
			} else {
				b.Partial = true
			}
		}
		if r := fields["spendControlReached"]; len(r) > 0 && string(r) != "null" {
			var v bool
			if json.Unmarshal(r, &v) == nil {
				b.SpendControlReached = &v
			} else {
				b.Partial = true
			}
		}
		if !accounts.ValidQuotaDetails(&accounts.QuotaDetails{Buckets: []accounts.QuotaBucket{b}}) {
			d.Partial = true
			continue
		}
		if len(b.Windows) == 0 && b.Individual == nil && b.Credits == nil && b.SpendControlReached == nil {
			b.Partial = true
		}
		d.Buckets = append(d.Buckets, b)
		d.Partial = d.Partial || b.Partial
	}
	if len(d.Buckets) == 0 {
		return nil, "no_windows"
	}
	return d, ""
}
