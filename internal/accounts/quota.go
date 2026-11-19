package accounts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

// QuotaName is deliberately separate from the catalog. Old catalog readers
// ignore this display-only cache; refreshing it never promotes catalog schema.
const QuotaName = "subscription-quota.json"
const QuotaSource = "codex account/rateLimits/read"
const ClaudeQuotaName = "claude-subscription-quota.json"
const ClaudeQuotaSource = "claude:get_usage"

type QuotaWindow struct {
	Name            string     `json:"name"`
	UsedPercent     int        `json:"usedPercent"`
	DurationMinutes *int64     `json:"durationMinutes,omitempty"`
	ResetsAt        *time.Time `json:"resetsAt,omitempty"`
}

// UnmarshalJSON distinguishes a reported zero from absent/null persisted data.
// The ordinary encoder always writes usedPercent, including zero.
func (w *QuotaWindow) UnmarshalJSON(b []byte) error {
	var wire struct {
		Name            string     `json:"name"`
		UsedPercent     *int       `json:"usedPercent"`
		DurationMinutes *int64     `json:"durationMinutes,omitempty"`
		ResetsAt        *time.Time `json:"resetsAt,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&wire); err != nil {
		return err
	}
	if wire.UsedPercent == nil {
		return ErrInvalid
	}
	*w = QuotaWindow{Name: wire.Name, UsedPercent: *wire.UsedPercent, DurationMinutes: wire.DurationMinutes, ResetsAt: wire.ResetsAt}
	return nil
}

type QuotaAttempt struct {
	Details *QuotaDetails `json:"details,omitempty"`
	At      time.Time     `json:"observedAt"`
	Reason  string        `json:"reason,omitempty"`
	Windows []QuotaWindow `json:"windows,omitempty"`
}
type QuotaEntry struct {
	ID       string        `json:"id"`
	Revision string        `json:"revision"`
	Context  string        `json:"context"`
	Success  *QuotaAttempt `json:"lastSuccess,omitempty"`
	Attempt  QuotaAttempt  `json:"lastAttempt"`
}
type QuotaFile struct {
	SchemaVersion int          `json:"schemaVersion"`
	Entries       []QuotaEntry `json:"entries"`
}
type QuotaView struct {
	Details             *QuotaDetails `json:"-"` // internal evidence; shared summary is the public projection
	Summary             *QuotaSummary `json:"summary,omitempty"`
	State               string        `json:"state"`
	Reason              string        `json:"reason,omitempty"`
	Source              string        `json:"source,omitempty"`
	ObservedAt          *time.Time    `json:"observedAt,omitempty"`
	RetrievedAt         *time.Time    `json:"retrievedAt,omitempty"`
	AgeSeconds          *int64        `json:"ageSeconds,omitempty"`
	Freshness           string        `json:"freshness"`
	Windows             []QuotaWindow `json:"windows"`
	FailedRefreshAt     *time.Time    `json:"failedRefreshAt,omitempty"`
	FailedRefreshReason string        `json:"failedRefreshReason,omitempty"`
}

func ValidQuotaReason(s string) bool {
	switch s {
	case "", "unsupported_provider", "unsupported", "timeout", "cancelled", "status_unavailable", "schema_unavailable", "no_windows", "unknown_bucket", "unknown_windows", "isolation_unavailable", "quota_unavailable", "unverified_cli":
		return true
	}
	return false
}
func validQuotaAttempt(a QuotaAttempt) bool {
	if a.At.IsZero() || !ValidQuotaReason(a.Reason) {
		return false
	}
	if a.Reason != "" {
		return len(a.Windows) == 0 && a.Details == nil
	}
	if a.Details != nil && !ValidQuotaDetails(a.Details) {
		return false
	}
	if (len(a.Windows) == 0 && a.Details == nil) || len(a.Windows) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, w := range a.Windows {
		if (w.Name != "primary" && w.Name != "secondary") || seen[w.Name] || w.UsedPercent < 0 || w.UsedPercent > 100 {
			return false
		}
		seen[w.Name] = true
		if w.DurationMinutes != nil && (*w.DurationMinutes <= 0 || *w.DurationMinutes > 525600) {
			return false
		}
		if w.ResetsAt != nil && (w.ResetsAt.IsZero() || w.ResetsAt.Year() < 2000 || w.ResetsAt.Year() > 9999) {
			return false
		}
	}
	return true
}
func validQuota(f QuotaFile) bool {
	if (f.SchemaVersion != 1 && f.SchemaVersion != 2) || f.Entries == nil || len(f.Entries) > MaxEntries {
		return false
	}
	seen := map[string]bool{}
	for _, e := range f.Entries {
		if !validToken(e.ID) || seen[e.ID] || !validToken(e.Revision) || !validDigest(e.Context) || !validQuotaAttempt(e.Attempt) {
			return false
		}
		seen[e.ID] = true
		if f.SchemaVersion == 1 && (e.Attempt.Details != nil || (e.Success != nil && e.Success.Details != nil)) {
			return false
		}
		if e.Success != nil && (!validQuotaAttempt(*e.Success) || e.Success.Reason != "" || e.Success.At.After(e.Attempt.At)) {
			return false
		}
		if e.Attempt.Reason == "" && (e.Success == nil || !reflect.DeepEqual(*e.Success, e.Attempt)) {
			return false
		}
	}
	return true
}

// LoadQuota never repairs damaged evidence or reads provider files.
func LoadQuota(dir string) (QuotaFile, error) {
	return loadQuotaFile(dir, QuotaName, decodeQuotaFile)
}

func loadQuotaFile(dir, name string, decode func([]byte) (QuotaFile, error)) (QuotaFile, error) {
	empty := QuotaFile{SchemaVersion: 1, Entries: []QuotaEntry{}}
	if checkDir(dir) != nil {
		return empty, ErrInvalid
	}
	p := filepath.Join(dir, name)
	st, err := snapshot.LstatRecord(p)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil || !config.PrivateFile(st) || st.Mode()&os.ModeSymlink != 0 || st.Size() > MaxBytes {
		return empty, ErrInvalid
	}
	f, err := snapshot.OpenRecord(p)
	if err != nil {
		return empty, ErrInvalid
	}
	defer f.Close()
	st, err = f.Stat()
	if err != nil || !config.PrivateFile(st) || st.Size() > MaxBytes {
		return empty, ErrInvalid
	}
	st, err = snapshot.LstatRecord(p)
	if err != nil || !config.PrivateFile(st) || st.Mode()&os.ModeSymlink != 0 {
		return empty, ErrInvalid
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil || len(b) > MaxBytes {
		return empty, ErrInvalid
	}
	return decode(b)
}

func decodeQuotaFile(b []byte) (QuotaFile, error) {
	empty := QuotaFile{SchemaVersion: 1, Entries: []QuotaEntry{}}
	var out QuotaFile
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if !QuotaJSONUnique(b) || !quotaCanonicalFields(b, reflect.TypeOf(out)) || d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || !validQuota(out) {
		return empty, ErrInvalid
	}
	return out, nil
}

// QuotaJSONUnique rejects ambiguous duplicate keys before metadata is decoded.
// The byte bounds belong to the RPC/cache readers; nesting is bounded here.
func QuotaJSONUnique(b []byte) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 16 {
			return false
		}
		t, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				s, ok := k.(string)
				if err != nil || !ok || seen[s] {
					return false
				}
				seen[s] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

// PublishQuota runs after the query, under the catalog writer lock, but never
// rewrites the catalog. The caller holds CheckGuard throughout the query, so
// overlapping refreshes/checks cannot publish out of order. External edits are
// caught by selection/context and executable/directory revalidation.
func PublishQuota(dir string, selected Entry, bound string, a QuotaAttempt, env []string, verify func() error) error {
	return publishQuotaFile(dir, QuotaName, selected, bound, a, env, verify, LoadQuota, func(q QuotaFile) ([]byte, error) { return json.Marshal(q) })
}

func publishQuotaFile(dir, name string, selected Entry, bound string, a QuotaAttempt, env []string, verify func() error, load func(string) (QuotaFile, error), encode func(QuotaFile) ([]byte, error)) error {
	if checkDir(dir) != nil || checkFile(filepath.Join(dir, "accounts.lock")) != nil || !validQuotaAttempt(a) {
		return ErrInvalid
	}
	l, err := lock.AcquireNamed(dir, "accounts")
	if err != nil {
		return err
	}
	defer l.Release()
	f, err := Load(dir)
	if err != nil {
		return err
	}
	e := Find(&f, selected.ID)
	if e == nil || !SameSelection(*e, selected) {
		return ErrChanged
	}
	current, ok := Bound(f, *e, env)
	if !ok || current != bound {
		return ErrChanged
	}
	if verify == nil || verify() != nil {
		return ErrChanged
	}
	q, err := load(dir)
	if err != nil {
		return err
	}
	// Drop evidence for removed selections, without merging identities.
	kept := q.Entries[:0]
	for _, x := range q.Entries {
		if Find(&f, x.ID) != nil {
			kept = append(kept, x)
		}
	}
	q.Entries = kept
	i := -1
	for n, x := range q.Entries {
		if x.ID == e.ID {
			i = n
			break
		}
	}
	if i < 0 {
		q.Entries = append(q.Entries, QuotaEntry{ID: e.ID})
		i = len(q.Entries) - 1
	}
	x := q.Entries[i]
	if x.Revision != e.Revision || x.Context != bound {
		x = QuotaEntry{ID: e.ID, Revision: e.Revision, Context: bound}
	}
	if !x.Attempt.At.IsZero() && a.At.Before(x.Attempt.At) {
		return ErrChanged
	}
	x.Attempt = a
	if a.Reason == "" {
		copy := a
		x.Success = &copy
	}
	q.Entries[i] = x
	q.SchemaVersion = 2
	if !validQuota(q) {
		return ErrInvalid
	}
	b, err := encode(q)
	if err != nil || len(b) > MaxBytes {
		return ErrInvalid
	}
	return control.WritePrivateFile(filepath.Join(dir, name), append(b, '\n'))
}
func SameSelection(a, b Entry) bool {
	return a.ID == b.ID && a.Revision == b.Revision && a.Context == b.Context && a.Label == b.Label && a.Provider == b.Provider && a.Mode == b.Mode && a.CLI == b.CLI && a.Workdir == b.Workdir && a.Directory == b.Directory
}

func QuotaViews(dir string, catalog File, env []string, now time.Time) map[string]QuotaView {
	codex, codexErr := LoadQuota(dir)
	claude, claudeErr := LoadClaudeQuota(dir)
	out := map[string]QuotaView{}
	for _, e := range catalog.Entries {
		v := QuotaView{State: "unavailable", Reason: "not_checked", Freshness: "unavailable", Windows: []QuotaWindow{}}
		q, err := codex, codexErr
		if e.Provider == "claude-code" {
			q, err = claude, claudeErr
		}
		if e.Provider != "codex" && e.Provider != "claude-code" {
			v.Reason = "unsupported_provider"
		} else if err != nil {
			v.Reason = "cache_unavailable"
		} else {
			bound, ok := Bound(catalog, e, env)
			for _, x := range q.Entries {
				if x.ID != e.ID {
					continue
				}
				if !ok || x.Revision != e.Revision || x.Context != bound {
					v.Reason = "context_changed"
					break
				}
				if x.Attempt.At.After(now) {
					v.Reason = "time_unavailable"
					break
				}
				if x.Attempt.Reason != "" {
					v.FailedRefreshAt = &x.Attempt.At
					v.FailedRefreshReason = x.Attempt.Reason
					v.Reason = x.Attempt.Reason
				}
				if x.Success != nil {
					at := x.Success.At
					age := int64(now.Sub(at) / time.Second)
					v.State = "reported"
					v.Reason = ""
					v.Source = QuotaSource
					v.ObservedAt = &at
					v.AgeSeconds = &age
					v.Freshness = "fresh"
					if age > 1800 {
						v.Freshness = "stale"
					}
					v.Windows = x.Success.Windows
					v.Details = x.Success.Details
					if e.Provider == "claude-code" {
						v.Source, v.Freshness = ClaudeQuotaSource, "provider_unknown"
						v.RetrievedAt, v.ObservedAt = &at, nil
					}
				}
				break
			}
		}
		summary := SummarizeQuota(v, now)
		v.Summary = &summary
		out[e.ID] = v
	}
	return out
}

func AttachQuota(v *View, readings map[string]QuotaView) {
	for i := range v.Cards {
		if v.Cards[i].Provider == "codex" || v.Cards[i].Provider == "claude-code" {
			q, ok := readings[v.Cards[i].ID]
			if ok {
				v.Cards[i].Quota = &q
			}
		}
	}
}
