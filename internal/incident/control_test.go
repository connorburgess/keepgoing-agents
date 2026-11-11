package incident

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlRecordDiscardsTickFieldsAndPrivateOperation(t *testing.T) {
	r := sample()
	r.Kind = "control"
	r.ControlChange = &ControlChange{Operation: "PRIVATE_OPERATION", SaveObservedAt: r.CheckStartedAt}
	r.ControlBefore = &Controls{Paused: true}
	r.ControlAfter = &Controls{Completed: true}
	r.CountersAvailable = true
	r.Nudges = 17
	r.LastActiveRecoveryAt = &r.CheckStartedAt
	r.Decisions = Decisions{Availability: "captured"}
	dir := t.TempDir()
	if err := Append(dir, r); err != nil {
		t.Fatal(err)
	}
	j, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := j.Records[0]
	if got.Kind != "control" || got.ControlChange.Operation != "unavailable" || got.Outcome != "control_saved" || !got.ControlBefore.Paused || !got.ControlAfter.Completed || got.Adapter != "unavailable" || got.TargetKey != "unavailable" || got.Observation.Availability != "not_observed" || got.CountersAvailable || got.Nudges != 0 || got.LastActiveRecoveryAt != nil || got.Decisions.Availability != "unavailable" {
		t.Fatal(got)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(b), "PRIVATE_OPERATION") {
		t.Fatal("operation leaked")
	}
}

func TestOldJournalsUpgradeAsTicksWithoutControlBackfill(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		dir := t.TempDir()
		r := sample()
		r.Sequence = 4
		// Legacy schemas cannot claim that control changes were captured.
		r.Kind = "control"
		r.ControlChange = &ControlChange{Operation: "pause", SaveObservedAt: r.CheckStartedAt}
		r.Decisions = Decisions{Availability: "captured"}
		b, _ := json.Marshal(Journal{SchemaVersion: version, NextSequence: 5, Dropped: 3, Records: []Record{r}})
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		j, err := Load(dir)
		if err != nil || j.SchemaVersion != version || j.Records[0].Kind != "tick" || j.Records[0].ControlChange != nil {
			t.Fatal(j, err)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(b) {
			t.Fatal("read mutated journal")
		}
		r.Kind = "control"
		r.ControlBefore, r.ControlAfter = &Controls{}, &Controls{Paused: true}
		if err := Append(dir, r); err != nil {
			t.Fatal(err)
		}
		j, err = Load(dir)
		if err != nil || j.SchemaVersion != 4 || j.Dropped != 3 || j.NextSequence != 6 || j.Records[0].Kind != "tick" || j.Records[1].Kind != "control" {
			t.Fatal(j, err)
		}
		if version == 3 && j.Records[0].Decisions.Availability != "captured" {
			t.Fatal("lost schema3 decision evidence")
		}
	}
}

func TestIncompleteControlRecordIsRefused(t *testing.T) {
	dir := t.TempDir()
	r := sample()
	r.Kind = "control"
	if err := Append(dir, r); err == nil {
		t.Fatal("invented control save")
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatal("wrote invalid record")
	}
}
