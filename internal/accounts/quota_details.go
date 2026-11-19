package accounts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const MaxQuotaBuckets = 8

var quotaKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var quotaDecimal = regexp.MustCompile(`^[0-9]{1,24}(\.[0-9]{1,12})?$`)

type QuotaMeter struct {
	Name            string     `json:"name"`
	UsedPercent     float64    `json:"usedPercent"`
	DurationMinutes *int64     `json:"durationMinutes,omitempty"`
	ResetsAt        *time.Time `json:"resetsAt,omitempty"`
}
type QuotaCredit struct {
	HasCredits bool    `json:"hasCredits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance,omitempty"`
}
type QuotaIndividual struct {
	Limit            string    `json:"limit"`
	Used             string    `json:"used"`
	RemainingPercent float64   `json:"remainingPercent"`
	ResetsAt         time.Time `json:"resetsAt"`
}
type QuotaBucket struct {
	Key                 string           `json:"key"`
	Windows             []QuotaMeter     `json:"windows"`
	Credits             *QuotaCredit     `json:"credits,omitempty"`
	Individual          *QuotaIndividual `json:"individual,omitempty"`
	SpendControlReached *bool            `json:"spendControlReached,omitempty"`
	Partial             bool             `json:"partial,omitempty"`
}
type QuotaDetails struct {
	Buckets              []QuotaBucket `json:"buckets"`
	OrdinaryUsageAllowed *bool         `json:"ordinaryUsageAllowed,omitempty"`
	Partial              bool          `json:"partial,omitempty"`
	Omitted              int           `json:"omitted,omitempty"`
}

func quotaPercent(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x <= 100 }
func quotaTime(t time.Time) bool  { return !t.IsZero() && t.Year() >= 2000 && t.Year() <= 9999 }
func ValidQuotaDetails(d *QuotaDetails) bool {
	if d == nil || d.Buckets == nil || len(d.Buckets) > MaxQuotaBuckets || d.Omitted < 0 || d.Omitted > 256 {
		return false
	}
	seen := map[string]bool{}
	for _, b := range d.Buckets {
		if !quotaKey.MatchString(b.Key) || seen[b.Key] || len(b.Windows) > 2 {
			return false
		}
		seen[b.Key] = true
		names := map[string]bool{}
		for _, w := range b.Windows {
			if (w.Name != "primary" && w.Name != "secondary") || names[w.Name] || !quotaPercent(w.UsedPercent) {
				return false
			}
			names[w.Name] = true
			if w.DurationMinutes != nil && (*w.DurationMinutes <= 0 || *w.DurationMinutes > 525600) {
				return false
			}
			if w.ResetsAt != nil && !quotaTime(*w.ResetsAt) {
				return false
			}
		}
		if c := b.Credits; c != nil && c.Balance != nil && !quotaDecimal.MatchString(*c.Balance) {
			return false
		}
		if i := b.Individual; i != nil && (!quotaDecimal.MatchString(i.Limit) || !quotaDecimal.MatchString(i.Used) || !quotaPercent(i.RemainingPercent) || !quotaTime(i.ResetsAt)) {
			return false
		}
	}
	return true
}

// QuotaSummary is the same bounded saved-evidence projection for web and native.
// Rows never combine buckets, account selections, route evidence or units.
type QuotaSummary struct {
	State             string            `json:"state"`
	Text              string            `json:"text"`
	Source            string            `json:"source,omitempty"`
	ObservedAt        *time.Time        `json:"observedAt,omitempty"`
	RetrievedAt       *time.Time        `json:"retrievedAt,omitempty"`
	Freshness         string            `json:"freshness"`
	Rows              []QuotaSummaryRow `json:"rows"`
	Partial           bool              `json:"partial,omitempty"`
	Omitted           int               `json:"omitted,omitempty"`
	FailedRefreshAt   *time.Time        `json:"failedRefreshAt,omitempty"`
	FailedRefreshText string            `json:"failedRefreshText,omitempty"`
}
type QuotaSummaryRow struct {
	Name             string     `json:"name"`
	Text             string     `json:"text"`
	UsedPercent      *float64   `json:"usedPercent,omitempty"`
	RemainingPercent *float64   `json:"remainingPercent,omitempty"`
	ResetsAt         *time.Time `json:"resetsAt,omitempty"`
	Ended            bool       `json:"ended,omitempty"`
}

func QuotaReasonText(reason string) string {
	switch reason {
	case "unsupported_provider":
		return "No supported model-free account quota source is available for this provider."
	case "not_checked":
		return "No quota reading is saved for this selection."
	case "context_changed":
		return "The selected context changed; refresh after reviewing the selection."
	case "cache_unavailable":
		return "Saved quota evidence is unavailable. No usage is assumed."
	case "timeout":
		return "The quota read timed out."
	case "cancelled":
		return "The quota read was cancelled."
	case "unsupported":
		return "This CLI does not support the quota read."
	case "unverified_cli":
		return "This native CLI build is not verified for isolated quota reads."
	case "isolation_unavailable":
		return "The CLI could not establish an isolated model-free quota read."
	case "quota_unavailable":
		return "The selected CLI context does not expose subscription quota."
	case "no_windows":
		return "The provider reported no supported quota detail."
	case "schema_unavailable", "unknown_windows", "unknown_bucket":
		return "Quota detail was not recognized. No usage is assumed."
	case "time_unavailable":
		return "The reading's time cannot be established."
	default:
		return "Quota reading unavailable."
	}
}
func SummarizeQuota(v QuotaView, now time.Time) QuotaSummary {
	s := QuotaSummary{State: v.State, Text: QuotaReasonText(v.Reason), Freshness: v.Freshness, Rows: []QuotaSummaryRow{}, FailedRefreshAt: v.FailedRefreshAt}
	if v.FailedRefreshAt != nil {
		s.FailedRefreshText = QuotaReasonText(v.FailedRefreshReason)
	}
	if v.State != "reported" {
		return s
	}
	claude := v.Source == ClaudeQuotaSource && v.Freshness == "provider_unknown" && v.RetrievedAt != nil && v.ObservedAt == nil
	codex := v.Source == QuotaSource && (v.Freshness == "fresh" || v.Freshness == "stale") && v.ObservedAt != nil && v.RetrievedAt == nil
	if !claude && !codex {
		s.State, s.Freshness, s.Text = "unavailable", "unavailable", QuotaReasonText("schema_unavailable")
		return s
	}
	s.Source = v.Source
	s.ObservedAt = v.ObservedAt
	s.RetrievedAt = v.RetrievedAt
	s.Text = "Saved selected-context quota; account identity and live thread binding are not verified."
	if claude {
		s.Text = "Saved selected-context quota; provider freshness unknown. Retrieval may return cached or seeded data. Account identity and live thread binding are not verified."
	}
	d := v.Details
	if d == nil { // Legacy evidence says nothing about additional provider buckets.
		d = &QuotaDetails{Partial: true, Buckets: []QuotaBucket{{Key: "codex", Windows: []QuotaMeter{}}}}
		for _, w := range v.Windows {
			d.Buckets[0].Windows = append(d.Buckets[0].Windows, QuotaMeter{Name: w.Name, UsedPercent: float64(w.UsedPercent), DurationMinutes: w.DurationMinutes, ResetsAt: w.ResetsAt})
		}
	}
	s.Partial = d.Partial
	s.Omitted = d.Omitted
	for n, b := range d.Buckets {
		label := "Additional bucket " + fmt.Sprint(n+1)
		if b.Key == "codex" {
			label = "Codex"
		}
		s.Partial = s.Partial || b.Partial
		for _, w := range b.Windows {
			name := w.Name + " window"
			if w.DurationMinutes != nil {
				switch *w.DurationMinutes {
				case 300:
					name = "5-hour"
				case 10080:
					name = "7-day"
				default:
					name = fmt.Sprintf("%d-minute", *w.DurationMinutes)
				}
			}
			used, remaining := w.UsedPercent, 100-w.UsedPercent
			row := QuotaSummaryRow{Name: label + " · " + name, Text: fmt.Sprintf("%g%% used · %g%% remaining", used, remaining), UsedPercent: &used, RemainingPercent: &remaining, ResetsAt: w.ResetsAt}
			if claude {
				row.Name = map[string]string{"five_hour": "5-hour", "seven_day": "7-day", "seven_day_oauth_apps": "7-day OAuth apps", "seven_day_opus": "7-day Opus", "seven_day_sonnet": "7-day Sonnet"}[b.Key]
				if row.Name == "" {
					s.Partial = true
					continue
				}
			}
			if w.ResetsAt != nil && !w.ResetsAt.After(now) {
				row.Ended = true
				row.Text = "Window ended; refresh needed. Last reading: " + row.Text
			}
			s.Rows = append(s.Rows, row)
		}
		if i := b.Individual; i != nil {
			remaining := i.RemainingPercent
			s.Rows = append(s.Rows, QuotaSummaryRow{Name: label + " · Individual limit", Text: fmt.Sprintf("%s used of %s · %g%% remaining · provider units", i.Used, i.Limit, remaining), RemainingPercent: &remaining, ResetsAt: &i.ResetsAt, Ended: !i.ResetsAt.After(now)})
		}
		if c := b.Credits; c != nil {
			text := "Credit availability: not available"
			if c.HasCredits {
				text = "Credits available"
			}
			if c.Unlimited {
				text = "Credits: unlimited"
			}
			if c.Balance != nil {
				text += " · balance " + *c.Balance + " (provider units; currency not reported)"
			}
			s.Rows = append(s.Rows, QuotaSummaryRow{Name: label + " · Credits", Text: text})
		}
		if b.SpendControlReached != nil {
			text := "Spend control not reached when read"
			if *b.SpendControlReached {
				text = "Spend control reached when read"
			}
			s.Rows = append(s.Rows, QuotaSummaryRow{Name: label + " · Spend control", Text: text})
		}
	}
	if d.OrdinaryUsageAllowed != nil {
		text := "Ordinary usage not allowed when read"
		if *d.OrdinaryUsageAllowed {
			text = "Ordinary usage allowed when read"
		}
		s.Rows = append(s.Rows, QuotaSummaryRow{Name: "Provider availability", Text: text})
	} else {
		s.Rows = append(s.Rows, QuotaSummaryRow{Name: "Provider availability", Text: "Unknown; percentages and reset times do not establish access."})
	}
	if len(s.Rows) == 1 {
		s.Partial = true
		s.Text = "No recognized quota meters were reported."
	}
	return s
}

// Required numeric/boolean fields must survive storage as explicit values;
// missing or null is never a reported zero/false.
func quotaDecodeRequired(raw []byte, value any, required ...string) error {
	if !quotaCanonicalFields(raw, reflect.TypeOf(value)) {
		return ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrInvalid
	}
	for _, key := range required {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			return ErrInvalid
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return ErrInvalid
	}
	return nil
}
func (w *QuotaMeter) UnmarshalJSON(raw []byte) error {
	type plain QuotaMeter
	return quotaDecodeRequired(raw, (*plain)(w), "name", "usedPercent")
}
func (c *QuotaCredit) UnmarshalJSON(raw []byte) error {
	type plain QuotaCredit
	return quotaDecodeRequired(raw, (*plain)(c), "hasCredits", "unlimited")
}
func (i *QuotaIndividual) UnmarshalJSON(raw []byte) error {
	type plain QuotaIndividual
	return quotaDecodeRequired(raw, (*plain)(i), "limit", "used", "remainingPercent", "resetsAt")
}

// encoding/json accepts case-insensitive field aliases. Persisted evidence must
// use the exact schema spelling, including optional fields at every depth.
func quotaCanonicalFields(raw []byte, typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if string(raw) == "null" {
		return true
	}
	switch typ.Kind() {
	case reflect.Struct:
		if typ == reflect.TypeOf(time.Time{}) {
			return true
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return false
		}
		allowed := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = f.Type
			}
		}
		for key, value := range fields {
			child, ok := allowed[key]
			if !ok || !quotaCanonicalFields(value, child) {
				return false
			}
		}
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return false
		}
		for _, item := range items {
			if !quotaCanonicalFields(item, typ.Elem()) {
				return false
			}
		}
	}
	return true
}
