package accounts

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"time"
)

// This separate file has its own strict schema. Old binaries ignore it, and a
// Claude refresh cannot promote, overwrite or invalidate the Codex cache. Native
// get_usage strips cache age/error/seed provenance: timestamps are retrievals,
// never provider observations. Source/freshness are fixed by this codec, not
// trusted from a persisted string.
type claudeQuotaAttempt struct {
	Details     *QuotaDetails `json:"details,omitempty"`
	RetrievedAt time.Time     `json:"retrievedAt"`
	Reason      string        `json:"reason,omitempty"`
}
type claudeQuotaEntry struct {
	ID       string              `json:"id"`
	Revision string              `json:"revision"`
	Context  string              `json:"context"`
	Success  *claudeQuotaAttempt `json:"lastSuccess,omitempty"`
	Attempt  claudeQuotaAttempt  `json:"lastAttempt"`
}
type claudeQuotaFile struct {
	SchemaVersion int                `json:"schemaVersion"`
	Entries       []claudeQuotaEntry `json:"entries"`
}

func LoadClaudeQuota(dir string) (QuotaFile, error) {
	return loadQuotaFile(dir, ClaudeQuotaName, decodeClaudeQuotaFile)
}

func decodeClaudeQuotaFile(raw []byte) (QuotaFile, error) {
	empty := QuotaFile{SchemaVersion: 2, Entries: []QuotaEntry{}}
	var wire claudeQuotaFile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !QuotaJSONUnique(raw) || !quotaCanonicalFields(raw, reflect.TypeOf(wire)) || d.Decode(&wire) != nil || d.Decode(new(any)) != io.EOF || wire.SchemaVersion != 1 || wire.Entries == nil {
		return empty, ErrInvalid
	}
	q := empty
	for _, e := range wire.Entries {
		x := QuotaEntry{ID: e.ID, Revision: e.Revision, Context: e.Context, Attempt: QuotaAttempt{At: e.Attempt.RetrievedAt, Reason: e.Attempt.Reason, Details: e.Attempt.Details}}
		if e.Success != nil {
			x.Success = &QuotaAttempt{At: e.Success.RetrievedAt, Reason: e.Success.Reason, Details: e.Success.Details}
		}
		q.Entries = append(q.Entries, x)
	}
	if !validClaudeQuota(q) {
		return empty, ErrInvalid
	}
	return q, nil
}

func validClaudeQuota(q QuotaFile) bool {
	if !validQuota(q) {
		return false
	}
	for _, e := range q.Entries {
		for _, a := range []*QuotaAttempt{&e.Attempt, e.Success} {
			if a == nil {
				continue
			}
			if !quotaTime(a.At) {
				return false
			}
			if len(a.Windows) != 0 {
				return false
			}
			if a.Reason != "" {
				continue
			}
			d := a.Details
			if d == nil || len(d.Buckets) == 0 || len(d.Buckets) > 5 || d.OrdinaryUsageAllowed != nil || d.Omitted != 0 {
				return false
			}
			for _, b := range d.Buckets {
				minutes := int64(10080)
				switch b.Key {
				case "five_hour":
					minutes = 300
				case "seven_day", "seven_day_oauth_apps", "seven_day_opus", "seven_day_sonnet":
				default:
					return false
				}
				if len(b.Windows) != 1 || b.Windows[0].Name != "primary" || b.Windows[0].DurationMinutes == nil || *b.Windows[0].DurationMinutes != minutes || b.Credits != nil || b.Individual != nil || b.SpendControlReached != nil {
					return false
				}
			}
		}
	}
	return true
}

func PublishClaudeQuota(dir string, selected Entry, bound string, a QuotaAttempt, env []string, verify func() error) error {
	if selected.Provider != "claude-code" || !validClaudeQuota(QuotaFile{SchemaVersion: 2, Entries: []QuotaEntry{{ID: selected.ID, Revision: selected.Revision, Context: bound, Attempt: a, Success: successClaudeAttempt(a)}}}) {
		return ErrInvalid
	}
	return publishQuotaFile(dir, ClaudeQuotaName, selected, bound, a, env, verify, LoadClaudeQuota, encodeClaudeQuotaFile)
}

func successClaudeAttempt(a QuotaAttempt) *QuotaAttempt {
	if a.Reason != "" {
		return nil
	}
	return &a
}

func encodeClaudeQuotaFile(q QuotaFile) ([]byte, error) {
	if !validClaudeQuota(q) {
		return nil, ErrInvalid
	}
	wire := claudeQuotaFile{SchemaVersion: 1, Entries: []claudeQuotaEntry{}}
	for _, x := range q.Entries {
		e := claudeQuotaEntry{ID: x.ID, Revision: x.Revision, Context: x.Context, Attempt: claudeQuotaAttempt{RetrievedAt: x.Attempt.At, Reason: x.Attempt.Reason, Details: x.Attempt.Details}}
		if x.Success != nil {
			e.Success = &claudeQuotaAttempt{RetrievedAt: x.Success.At, Reason: x.Success.Reason, Details: x.Success.Details}
		}
		wire.Entries = append(wire.Entries, e)
	}
	return json.Marshal(wire)
}
