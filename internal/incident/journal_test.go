package incident

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func sample() Record {
	return Record{Kind: "tick", CheckStartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Build: "v0.1.2", Adapter: "bb", TargetKey: TargetKey("primary"), Action: "none", Cause: "retrying", Outcome: "succeeded",
		Observation: Observed(decide.Observation{State: decide.Active, ErrorClass: decide.ErrNetwork, Retrying: true}, "bb", false)}
}

func TestJournalRetentionRestartAndPrivacy(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < MaxRecords+4; i++ {
		r := sample()
		r.CheckStartedAt = r.CheckStartedAt.Add(time.Duration(i) * time.Minute)
		if err := Append(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	j, err := Load(dir)
	// Identical checks are collapsed, not pruned: the run keeps its first and
	// latest check and accounts for every one between.
	if err != nil || j.Dropped != 0 || j.Collapsed != MaxRecords+2 || len(j.Records) != 2 || j.NextSequence != MaxRecords+5 || j.Records[0].Sequence != 1 || j.Records[1].Sequence != MaxRecords+4 {
		t.Fatalf("%+v %v", j, err)
	}
	base := sample().CheckStartedAt
	if c := j.Records[1].Collapsed; j.Records[0].Collapsed != nil || c == nil || c.Count != MaxRecords+2 || !c.FirstAt.Equal(base.Add(time.Minute)) || !c.LastAt.Equal(base.Add((MaxRecords+2)*time.Minute)) {
		t.Fatalf("%+v", j.Records[1].Collapsed)
	}
	r := sample()
	r.Build = "SECRET_PAYLOAD"
	r.Adapter = "SECRET_PAYLOAD"
	r.TargetKey = "SECRET_PAYLOAD"
	r.Action = "SECRET_PAYLOAD"
	r.Cause = "SECRET_PAYLOAD"
	r.Outcome = "SECRET_PAYLOAD"
	r.Observation.State = "SECRET_PAYLOAD"
	r.Observation.ErrorClass = "SECRET_PAYLOAD"
	r.Observation.Retry = "SECRET_PAYLOAD"
	r.Observation.SourceTime = "SECRET_PAYLOAD"
	r.Observation.Source = "SECRET_PAYLOAD"
	r.Observation.Category = "SECRET_PAYLOAD"
	r.Observation.ProviderCode = "SECRET_PAYLOAD"
	if err := Append(dir, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxBytes || strings.Contains(string(b), "SECRET_PAYLOAD") {
		t.Fatal("history exceeded bound or leaked raw fields")
	}
	info, _ := os.Stat(filepath.Join(dir, FileName))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	j, err = Load(dir)
	if err != nil || j.Dropped != 0 || len(j.Records) != 3 {
		t.Fatal(j.Dropped, err)
	}
	last := j.Records[len(j.Records)-1]
	if last.Build != "unavailable" || last.Observation.ProviderCode != "unavailable" {
		t.Fatal(last)
	}
}

func TestJournalRefusesInvalidHistoryWithoutReplacement(t *testing.T) {
	for _, input := range []string{`{`, `{"schemaVersion":5}`, strings.Repeat("x", MaxBytes+1), `{"schemaVersion":1,"nextSequence":3,"dropped":0,"records":[]}`, `{"schemaVersion":1,"nextSequence":1,"dropped":0,"records":[],"secret":"hidden"}`} {
		t.Run(input[:min(len(input), 20)], func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, FileName)
			if err := os.WriteFile(p, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Append(dir, sample()); !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(p)
			if string(after) != input {
				t.Fatal("replaced invalid history")
			}
		})
	}
}

func TestJournalContentionAndAlias(t *testing.T) {
	dir := t.TempDir()
	l, err := lock.AcquireNamed(dir, "incident")
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, sample()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	l.Release()
	if err := Append(dir, sample()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, FileName)
	b, _ := os.ReadFile(p)
	other := filepath.Join(t.TempDir(), "original.json")
	if err := os.WriteFile(other, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, p); err != nil {
		t.Skip("symlink unavailable")
	}
	if err := Append(dir, sample()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(other)
	if string(after) != string(b) {
		t.Fatal("changed alias destination")
	}
}

func TestObservationAvailability(t *testing.T) {
	for _, adapter := range []string{"bb", "claude", "codex", "process"} {
		for _, failed := range []bool{false, true} {
			o := Observed(decide.Observation{State: decide.Active, ErrorClass: decide.ErrNetwork, Retrying: true}, adapter, failed)
			r := clean(Record{Observation: o})
			if failed && (r.Observation.Availability != "unavailable" || r.Observation.Retry != "unavailable") {
				t.Fatal(r)
			}
			if !failed && adapter != "bb" && r.Observation.Retry != "unavailable" {
				t.Fatal("invented retry support", r)
			}
		}
	}
	r := clean(Record{Observation: Observed(decide.Observation{State: decide.Error, ErrorClass: decide.ErrNetwork, RetryUnverified: true}, "bb", false)})
	if r.Observation.ErrorClass != "unavailable" || r.Observation.Retry != "unverified" {
		t.Fatal(r)
	}
	// The storage schema contains no maps or free text for provider output.
	b, _ := json.Marshal(clean(sample()))
	if !strings.Contains(string(b), `"sourceTime":"unavailable"`) {
		t.Fatal(string(b))
	}
}

func TestChildEscalationDoesNotInventParentRetryEvidence(t *testing.T) {
	r := clean(Record{Observation: Observed(decide.Observation{State: decide.Active, AwaitingChildren: []string{"thr_child"}}, "bb", false)})
	if r.Observation.Retry != "unavailable" || r.Observation.ErrorClass != "unavailable" {
		t.Fatal(r)
	}
}

func TestMissingRetryIntentIsUnavailable(t *testing.T) {
	for _, known := range []bool{false, true} {
		r := clean(Record{Observation: Observed(decide.Observation{State: decide.Error, ErrorClass: decide.ErrNetwork, RetryKnown: known}, "bb", false)})
		want := "unavailable"
		if known {
			want = "not_retrying"
		}
		if r.Observation.Retry != want {
			t.Fatal(r)
		}
	}
}

func TestSchemaOneUpgradePreservesHistoryAndUnavailableEvidence(t *testing.T) {
	dir := t.TempDir()
	r := sample()
	r.Sequence = 1
	j := Journal{SchemaVersion: 1, NextSequence: 2, Records: []Record{r}}
	b, _ := json.Marshal(j)
	p := filepath.Join(dir, FileName)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.SchemaVersion != 1 || loaded.Records[0].Observation.Source != "unavailable" {
		t.Fatal(loaded, err)
	}
	before, _ := os.ReadFile(p)
	if string(before) != string(b) {
		t.Fatal("read upgraded on disk")
	}
	if err := Append(dir, sample()); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(dir)
	if err != nil || loaded.SchemaVersion != 6 || len(loaded.Records) != 2 || loaded.Records[0].Sequence != 1 || loaded.NextSequence != 3 {
		t.Fatal(loaded, err)
	}
	if loaded.Records[0].Observation.ProviderCode != "unavailable" {
		t.Fatal("invented legacy evidence")
	}
}

func TestJournalBuildLabelsSurviveRestartWithoutPrivateSuffixes(t *testing.T) {
	accepted := []string{"dev", "v1.2.3", "1.2.3", "abcdef1", "smoke-abcdef1-dirty", "v1.2.3-dev.abcdef1", "1.2.3-dev." + strings.Repeat("a", 40), "v1.2.3-rc.1", "1.2.3-rc.12"}
	rejected := []string{"", "unavailable", "v1.2.3-dev.short", "v1.2.3-dev." + strings.Repeat("a", 41), "v1.2.3-dev.private-task", "v1.2.3-rc.private-task", "v1.2.3-rc.1+private-task", "v1.2.3-dev.abcdef1/private-task", "v1.2.3-beta.1", "v1.2.3-rc.1\nprivate-task"}
	for _, kind := range []string{"tick", "control"} {
		for _, label := range append(append([]string{}, accepted...), rejected...) {
			t.Run(kind+"/"+label, func(t *testing.T) {
				dir := t.TempDir()
				r := sample()
				r.Kind, r.Build = kind, label
				if kind == "control" {
					r.ControlChange = &ControlChange{Operation: "pause", SaveObservedAt: r.CheckStartedAt}
					r.ControlBefore, r.ControlAfter = &Controls{}, &Controls{Paused: true}
				}
				if err := Append(dir, r); err != nil {
					t.Fatal(err)
				}
				want := "unavailable"
				if slices.Contains(accepted, label) {
					want = label
				}
				j, err := Load(dir)
				if err != nil || len(j.Records) != 1 || j.Records[0].Build != want {
					t.Fatalf("build lost or unsafe suffix retained: %+v %v", j, err)
				}
				// A later process append cleans older records again. Neither
				// recognized labels nor earlier redactions may change on rewrite.
				if err := Append(dir, sample()); err != nil {
					t.Fatal(err)
				}
				j, err = Load(dir)
				if err != nil || j.Records[0].Build != want {
					t.Fatal("rewrite changed recorded build", err)
				}
				data, err := os.ReadFile(filepath.Join(dir, FileName))
				if err != nil || strings.Contains(string(data), "private-task") {
					t.Fatal("private build suffix entered storage", err)
				}
			})
		}
	}
}
