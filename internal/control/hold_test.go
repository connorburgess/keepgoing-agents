package control

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestHoldMarker(t *testing.T) {
	dir := t.TempDir()
	if h, err := LoadHold(dir); h != nil || err != nil {
		t.Fatalf("no marker: %+v %v", h, err)
	}
	at := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	if _, err := SaveHold(dir, false, true, at); err != nil {
		t.Fatal(err)
	}
	// A stop added later keeps the pause.
	h, err := SaveHold(dir, true, false, at.Add(time.Minute))
	if err != nil || !h.Stop || !h.Paused || !h.At.Equal(at.Add(time.Minute)) {
		t.Fatalf("%+v %v", h, err)
	}
	if got, err := LoadHold(dir); err != nil || *got != h {
		t.Fatalf("reload: %+v %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(dir, HoldFileName)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode: %v %v", info, err)
		}
	}
	f := File{Enabled: true}
	h.Apply(&f)
	if !f.Stop || !f.Paused || f.Summary() != "stop requested" {
		t.Fatalf("applied: %+v %s", f, f.Summary())
	}
	if h.Summary() != "stopped (control record unreadable)" {
		t.Fatal(h.Summary())
	}
	if err := ReleaseHold(dir); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseHold(dir); err != nil {
		t.Fatal("releasing an absent marker is not an error:", err)
	}
	var none *Hold
	none.Apply(&f) // a nil marker changes nothing
}

// A marker that cannot be read holds the loop stopped: it was written to hold
// the loop, and a damaged one must not release it.
func TestADamagedHoldMarkerReadsAsAStop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, HoldFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHold(dir)
	if err == nil || h == nil || !h.Stop {
		t.Fatalf("%+v %v", h, err)
	}
}

// The marker is released only while control.json can be read.
func TestReleaseHoldNeedsAReadableControlRecord(t *testing.T) {
	dir := t.TempDir()
	if _, err := SaveHold(dir, true, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseHold(dir); err == nil {
		t.Fatal("released while control.json is unreadable")
	}
	if h, err := LoadHold(dir); h == nil || err != nil {
		t.Fatalf("the marker is gone: %+v %v", h, err)
	}
}
