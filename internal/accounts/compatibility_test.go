package accounts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Frozen pre-plan wire types: deliberately independent of today's Observation,
// Entry, File and plan validator. Old Load uses strict decoding and schema 1.
type legacyAccountObservation struct {
	Revision string    `json:"revision"`
	Context  string    `json:"context"`
	At       time.Time `json:"observedAt"`
	Auth     string    `json:"auth"`
	Reason   string    `json:"reason"`
	Version  string    `json:"cliVersion"`
	Plan     string    `json:"plan"`
	Caller   string    `json:"caller"`
}
type legacyAccountEntry struct {
	ID        string                    `json:"id"`
	Label     string                    `json:"label"`
	Provider  string                    `json:"provider"`
	Mode      string                    `json:"selection"`
	CLI       string                    `json:"executable"`
	Directory string                    `json:"accountDirectory"`
	Workdir   string                    `json:"workdir"`
	Revision  string                    `json:"revision"`
	Context   string                    `json:"context"`
	UpdatedAt time.Time                 `json:"updatedAt"`
	UpdatedBy string                    `json:"updatedBy"`
	Last      *legacyAccountObservation `json:"lastObservation"`
	Attempt   *legacyAccountObservation `json:"lastAttempt"`
}
type legacyAccountCatalog struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Salt          string               `json:"salt"`
	Entries       []legacyAccountEntry `json:"entries"`
}

func readLegacyAccountCatalog(raw []byte) (legacyAccountCatalog, error) {
	var f legacyAccountCatalog
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return f, err
	}
	if d.Decode(new(any)) != io.EOF || f.SchemaVersion != 1 {
		return f, fmt.Errorf("legacy schema refused")
	}
	for _, e := range f.Entries {
		for _, o := range []*legacyAccountObservation{e.Last, e.Attempt} {
			if o != nil {
				switch o.Plan {
				case "", "max", "pro", "team", "enterprise", "free":
				default:
					return f, fmt.Errorf("legacy plan refused")
				}
			}
		}
	}
	return f, nil
}
func TestCatalogLegacyClaudeWrites(t *testing.T) {
	dir, e := fixture(t)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	obs := &legacyAccountObservation{Revision: e.Revision, Context: e.Context, At: at, Auth: "reported_present", Version: "2.1.318", Plan: "pro", Caller: "thr_example_owner"}
	old := legacyAccountCatalog{SchemaVersion: 1, Salt: "0123456789abcdef0123456789abcdef", Entries: []legacyAccountEntry{{ID: e.ID, Label: e.Label, Provider: e.Provider, Mode: e.Mode, CLI: e.CLI, Workdir: e.Workdir, Revision: e.Revision, Context: e.Context, UpdatedAt: e.UpdatedAt, UpdatedBy: e.UpdatedBy, Last: obs, Attempt: obs}}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(dir); err != nil {
		t.Fatalf("new reader refused legacy input: %v", err)
	}
	for _, op := range []string{"no-op", "claude-check", "unset-plan"} {
		t.Run(op, func(t *testing.T) {
			if err := Update(dir, func(f *File) error {
				if op == "claude-check" || op == "unset-plan" {
					o := &Observation{Revision: e.Revision, Context: e.Context, At: at.Add(time.Hour), Auth: "reported_present", Version: "2.1.318", Plan: "max", Caller: "thr_example_owner"}
					if op == "unset-plan" {
						o.Plan = ""
						o.Auth = "reported_absent"
					}
					f.Entries[0].Last = o
					f.Entries[0].Attempt = o
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			written, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := readLegacyAccountCatalog(written)
			if err != nil {
				t.Fatalf("old reader refused %s rewrite: %v", op, err)
			}
			if op == "no-op" && !reflect.DeepEqual(decoded, old) {
				t.Fatal("no-op changed legacy metadata")
			}
			if op == "claude-check" && (decoded.Entries[0].Last.Plan != "max" || !decoded.Entries[0].Last.At.Equal(at.Add(time.Hour))) {
				t.Fatal("Claude write lost evidence")
			}
			if op == "unset-plan" && decoded.Entries[0].Last.Plan != "" {
				t.Fatal("unset plan retained a value")
			}
			if bytes.Contains(written, []byte("planObservedAt")) || bytes.Contains(written, []byte("planReason")) {
				t.Fatal("new fields leaked into legacy format")
			}
		})
	}
}

func TestCatalogPlanSchemaBoundary(t *testing.T) {
	for _, kind := range []string{"plus", "pro-time", "failure-without-plan", "null-account", "unknown-tier"} {
		t.Run(kind, func(t *testing.T) {
			dir, e := fixture(t)
			at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			o := &Observation{Revision: e.Revision, Context: e.Context, At: at, Auth: "reported_present", Plan: "pro"}
			switch kind {
			case "plus":
				o.Plan = "plus"
			case "pro-time":
				o.PlanAt = &at
			case "failure-without-plan":
				o.Plan = ""
				o.PlanReason = "timeout"
			case "null-account":
				o.Plan = ""
				o.PlanReason = "no_account"
			case "unknown-tier":
				o.Plan = ""
				o.PlanReason = "not_recognized"
			}
			if err := Update(dir, func(f *File) error {
				f.Entries[0].Provider = "codex"
				f.Entries[0].Last = o
				f.Entries[0].Attempt = o
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			f, err := Load(dir)
			if err != nil || f.SchemaVersion != 2 {
				t.Fatalf("new reader/schema: %+v %v", f, err)
			}
			if !reflect.DeepEqual(f.Entries[0].Attempt, o) {
				t.Fatal("promotion lost plan time or reason")
			}
			path := filepath.Join(dir, FileName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = readLegacyAccountCatalog(raw); err == nil {
				t.Fatal("old reader accepted schema 2")
			}
			if err = Update(dir, func(f *File) error {
				f.SchemaVersion = 1
				f.Entries[0].Last = nil
				f.Entries[0].Attempt = nil
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			f, err = Load(dir)
			if err != nil || f.SchemaVersion != 2 {
				t.Fatal("schema promotion silently reversed")
			}
			// New fields/tiers cannot be mislabeled as schema 1, even by an external writer.
			var mislabeled map[string]json.RawMessage
			if err = json.Unmarshal(raw, &mislabeled); err != nil {
				t.Fatal(err)
			}
			mislabeled["schemaVersion"] = json.RawMessage("1")
			raw, err = json.Marshal(mislabeled)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = Load(dir); err == nil {
				t.Fatal("schema 1 accepted schema 2 content")
			}
			if err = Update(dir, func(*File) error { return nil }); err == nil {
				t.Fatal("mislabeled catalog rewritten")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(raw, after) {
				t.Fatal("mislabeled catalog mutated")
			}
		})
	}
}

func TestCatalogFutureSchemaRefusedUnchanged(t *testing.T) {
	for _, version := range []int{0, 3, 99} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			dir, _ := fixture(t)
			f, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			f.SchemaVersion = version
			raw, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, FileName)
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = Load(dir); err == nil {
				t.Fatal("unsupported schema accepted")
			}
			called := false
			if err = Update(dir, func(*File) error { called = true; return nil }); err == nil || called {
				t.Fatal("unsupported schema update reached mutation")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(raw, after) {
				t.Fatal("unsupported schema modified")
			}
		})
	}
}
