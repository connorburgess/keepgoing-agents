package accounts

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"time"
)

// Separate cache so Claude/Codex readers never see Antigravity evidence and a
// refresh cannot overwrite their files. Retrieval timestamps are not claimed as
// provider observation times.
const AntigravityQuotaName = "antigravity-subscription-quota.json"
const AntigravityQuotaSource = "agy:/usage"

type antigravityQuotaAttempt struct {
	Details     *QuotaDetails `json:"details,omitempty"`
	RetrievedAt time.Time     `json:"retrievedAt"`
	Reason      string        `json:"reason,omitempty"`
}
type antigravityQuotaEntry struct {
	ID       string                   `json:"id"`
	Revision string                   `json:"revision"`
	Context  string                   `json:"context"`
	Success  *antigravityQuotaAttempt `json:"lastSuccess,omitempty"`
	Attempt  antigravityQuotaAttempt  `json:"lastAttempt"`
}
type antigravityQuotaFile struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Entries       []antigravityQuotaEntry `json:"entries"`
}

func LoadAntigravityQuota(dir string) (QuotaFile, error) {
	return loadQuotaFile(dir, AntigravityQuotaName, decodeAntigravityQuotaFile)
}

func decodeAntigravityQuotaFile(raw []byte) (QuotaFile, error) {
	empty := QuotaFile{SchemaVersion: 2, Entries: []QuotaEntry{}}
	var wire antigravityQuotaFile
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
	if !validAntigravityQuota(q) {
		return empty, ErrInvalid
	}
	return q, nil
}

func validAntigravityQuota(q QuotaFile) bool {
	if !validQuota(q) {
		return false
	}
	for _, e := range q.Entries {
		for _, a := range []*QuotaAttempt{&e.Attempt, e.Success} {
			if a == nil {
				continue
			}
			if !quotaTime(a.At) || len(a.Windows) != 0 {
				return false
			}
			if a.Reason != "" {
				continue
			}
			d := a.Details
			if d == nil || len(d.Buckets) == 0 || len(d.Buckets) > 4 || d.OrdinaryUsageAllowed != nil || d.Omitted != 0 {
				return false
			}
			for _, b := range d.Buckets {
				minutes := int64(0)
				switch b.Key {
				case "gemini-weekly", "3p-weekly":
					minutes = 10080
				case "gemini-5h", "3p-5h":
					minutes = 300
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

func PublishAntigravityQuota(dir string, selected Entry, bound string, a QuotaAttempt, env []string, verify func() error) error {
	if selected.Provider != "antigravity" || !validAntigravityQuota(QuotaFile{SchemaVersion: 2, Entries: []QuotaEntry{{ID: selected.ID, Revision: selected.Revision, Context: bound, Attempt: a, Success: successAntigravityAttempt(a)}}}) {
		return ErrInvalid
	}
	return publishQuotaFile(dir, AntigravityQuotaName, selected, bound, a, env, verify, LoadAntigravityQuota, encodeAntigravityQuotaFile)
}

func successAntigravityAttempt(a QuotaAttempt) *QuotaAttempt {
	if a.Reason != "" {
		return nil
	}
	return &a
}

func encodeAntigravityQuotaFile(q QuotaFile) ([]byte, error) {
	if !validAntigravityQuota(q) {
		return nil, ErrInvalid
	}
	wire := antigravityQuotaFile{SchemaVersion: 1, Entries: []antigravityQuotaEntry{}}
	for _, x := range q.Entries {
		e := antigravityQuotaEntry{ID: x.ID, Revision: x.Revision, Context: x.Context, Attempt: antigravityQuotaAttempt{RetrievedAt: x.Attempt.At, Reason: x.Attempt.Reason, Details: x.Attempt.Details}}
		if x.Success != nil {
			e.Success = &antigravityQuotaAttempt{RetrievedAt: x.Success.At, Reason: x.Success.Reason, Details: x.Success.Details}
		}
		wire.Entries = append(wire.Entries, e)
	}
	return json.Marshal(wire)
}
