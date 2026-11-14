//go:build windows

package runner

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// A held delete-sharing reader must retain the old record without blocking
// publication. This isolates run.json from wrapper startup and turn cleanup.
func TestRunRecordReplacementWithHeldReader(t *testing.T) {
	old := Record{StopKey: "previous-turn", PID: 0}
	d := writeRecord(t, old)
	held, err := snapshot.Open(d.path("run.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	next := Record{StopKey: "next-turn", PID: 0}
	if err := d.saveRecord(next); err != nil {
		t.Fatalf("publish run.json with held delete-sharing reader: %T %v", err, err)
	}
	b, err := io.ReadAll(held)
	if err != nil {
		t.Fatal(err)
	}
	var before Record
	if err := json.Unmarshal(b, &before); err != nil || before.StopKey != old.StopKey {
		t.Fatalf("held snapshot changed: %q %v", b, err)
	}
	got, err := d.record()
	if err != nil || got.StopKey != next.StopKey {
		t.Fatalf("new record = %+v, %v", got, err)
	}
	if _, err := os.Stat(d.path("run.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("staging file remains: %v", err)
	}
}

func TestRunRecordRefusedReplacementPreservesPrior(t *testing.T) {
	d := Dir(t.TempDir())
	old := Record{StopKey: "previous-turn"}
	if err := d.saveRecord(old); err != nil {
		t.Fatal(err)
	}
	// Model an older binary's reader, which does not share deletion.
	held, err := os.Open(d.path("run.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	next := Record{StopKey: "next-turn"}
	if err := d.saveRecord(next); err == nil {
		t.Fatal("replacement passed a legacy reader")
	}
	got, err := d.record()
	if err != nil || got.StopKey != old.StopKey {
		t.Fatalf("refused save changed record: %+v %v", got, err)
	}
	if _, err := os.Stat(d.path("run.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("failed save left staging: %v", err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.saveRecord(next); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedRunRecordUnavailableDoesNotReadNeverRun(t *testing.T) {
	d := Dir(t.TempDir())
	if err := d.saveRecord(Record{StopKey: "previous-turn"}); err != nil {
		t.Fatal(err)
	}
	// A published record's name can be unavailable during replacement. Its
	// marker must prevent that from becoming evidence that no turn ever ran.
	if err := os.Remove(d.path("run.json")); err != nil {
		t.Fatal(err)
	}
	if st, err := d.Status(); err == nil || st.Phase == NeverRun || os.IsNotExist(err) {
		t.Fatalf("published record became absent: %+v %v", st, err)
	}
	if _, err := d.record(); err == nil || os.IsNotExist(err) {
		t.Fatalf("wrapper record became absent: %v", err)
	}
	never := Dir(t.TempDir())
	if st, err := never.Status(); err != nil || st.Phase != NeverRun {
		t.Fatalf("never-published record: %+v %v", st, err)
	}
}
