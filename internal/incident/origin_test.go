package incident

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A control change from the keepgoing panel is recorded in the operation's
// value. It survives a write and a read, and a binary from before those
// values reads the same journal without refusing it: the record's fields
// are unchanged, and its value list turns an operation it does not know
// into "unavailable".
func TestPanelOriginIsAnOperationValueOlderReadersNormalise(t *testing.T) {
	dir := t.TempDir()
	on, paused := &Controls{Enabled: true}, &Controls{Enabled: true, Paused: true}
	for _, op := range []string{"panel_pause", "panel_stop", "panel_resume", "pause"} {
		if err := Append(dir, Record{Kind: "control", CheckStartedAt: time.Now().UTC(), ControlBefore: on, ControlAfter: paused, ControlChange: &ControlChange{Operation: op, SaveObservedAt: time.Now().UTC()}}); err != nil {
			t.Fatal(err)
		}
	}
	j, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"panel_pause", "panel_stop", "panel_resume", "pause"} {
		if got := j.Records[i].ControlChange.Operation; got != want {
			t.Errorf("record %d: %q", i, got)
		}
	}
	// An older binary: the same strict decoder over the same structs (no
	// field was added), and the value list as it was before.
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	var old Journal
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&old); err != nil {
		t.Fatalf("an older strict decoder refused the journal: %v", err)
	}
	before := []string{"enable", "disable", "pause", "resume", "stop", "complete", "await-user", "start", "daemon_takeover", "transfer_begin", "transfer_finish"}
	for i, want := range []string{"unavailable", "unavailable", "unavailable", "pause"} {
		r := old.Records[i]
		if got := oneOf(r.ControlChange.Operation, "unavailable", before...); got != want || r.ControlAfter == nil || !r.ControlAfter.Paused {
			t.Errorf("record %d read by an older binary: %q %+v", i, got, r.ControlAfter)
		}
	}
	// An operation outside both lists is still not stored as written.
	if err := Append(dir, Record{Kind: "control", CheckStartedAt: time.Now().UTC(), ControlBefore: on, ControlAfter: on, ControlChange: &ControlChange{Operation: "panel_enable", SaveObservedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	if j, err = Load(dir); err != nil || j.Records[len(j.Records)-1].ControlChange.Operation != "unavailable" {
		t.Errorf("%v %q", err, j.Records[len(j.Records)-1].ControlChange.Operation)
	}
}
