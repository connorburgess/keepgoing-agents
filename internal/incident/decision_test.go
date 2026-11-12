package incident

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

func TestDecisionPrivacyBoundsAndClockDelta(t *testing.T) {
	var h Decisions
	in := decide.Input{Obs: decide.Observation{State: "SECRET_MARKER", ErrorClass: "SECRET_MARKER", AwaitingChildren: []string{"SECRET_MARKER"}, Evidence: evidence.Detail{Code: "SECRET_MARKER"}}, SinceWake: -time.Minute}
	for i := 0; i < MaxEvaluations+3; i++ {
		in.Nudges = i
		h.Add("SECRET_MARKER", in, decide.Decision{Action: "SECRET_MARKER", Reason: "SECRET_MARKER"})
	}
	// Exercise the disk storage boundary as well as the capture boundary.
	h.Evaluations[0].Cause = "SECRET_MARKER"
	r := sample()
	r.Decisions = h
	dir := t.TempDir()
	if err := Append(dir, r); err != nil {
		t.Fatal(err)
	}
	j, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h = j.Records[0].Decisions
	if len(h.Evaluations) != MaxEvaluations || h.Dropped != 3 || h.Evaluations[0].Input.Nudges != 3 || h.Evaluations[0].Input.SinceWake != -time.Minute || h.Evaluations[0].Input.AwaitingChildren != 1 {
		t.Fatal(h)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(b), "SECRET_MARKER") {
		t.Fatal("private decision content leaked")
	}
}

func TestLegacyDecisionHistoryUnavailableUntilNextTick(t *testing.T) {
	for _, version := range []int{1, 2} {
		dir := t.TempDir()
		r := sample()
		r.Sequence = 4
		// Even a purported trace in old schema cannot assert capture support.
		r.Decisions = Decisions{Availability: "captured"}
		j := Journal{SchemaVersion: version, NextSequence: 5, Dropped: 3, Records: []Record{r}}
		b, _ := json.Marshal(j)
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(dir)
		if err != nil || loaded.SchemaVersion != version || loaded.Records[0].Decisions.Availability != "unavailable" {
			t.Fatal(loaded, err)
		}
		after, _ := os.ReadFile(path)
		if string(b) != string(after) {
			t.Fatal("load mutated legacy history")
		}
		r = sample()
		r.Decisions = Decisions{Availability: "captured"}
		if err := Append(dir, r); err != nil {
			t.Fatal(err)
		}
		loaded, err = Load(dir)
		if err != nil || loaded.SchemaVersion != 5 || loaded.Dropped != 3 || loaded.NextSequence != 6 || loaded.Records[0].Decisions.Availability != "unavailable" || loaded.Records[1].Decisions.Availability != "captured" {
			t.Fatal(loaded, err)
		}
	}
}

func TestDecisionHeavyHistoryPrunesByBytes(t *testing.T) {
	dir := t.TempDir()
	r := sample()
	for i := 0; i < MaxEvaluations; i++ {
		r.Decisions.Add("normal", decide.Input{Control: decide.Control{Enabled: true}, Obs: decide.Observation{State: decide.Active}, SinceWake: time.Hour}, decide.Decision{Action: decide.None, Reason: "active"})
	}
	for i := 0; i < 80; i++ {
		if err := Append(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	j, err := Load(dir)
	if err != nil || j.Dropped == 0 || len(j.Records) >= 80 || j.NextSequence != 81 || j.Records[0].Sequence != j.Dropped+1 {
		t.Fatal(j, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil || len(b) > MaxBytes {
		t.Fatal(len(b), err)
	}
	if len(j.Records[len(j.Records)-1].Decisions.Evaluations) != MaxEvaluations {
		t.Fatal("record pruned partially")
	}
}
