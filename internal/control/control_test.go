package control

import (
	"testing"
	"time"
)

func TestSaveAdvancesLoadedTimestamp(t *testing.T) {
	for _, offset := range []time.Duration{0, -time.Hour, time.Second} {
		t.Run(offset.String(), func(t *testing.T) {
			dir := t.TempDir()
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := saveAt(dir, File{Paused: true, UpdatedAt: at}, at.Add(offset)); err != nil {
				t.Fatal(err)
			}
			got, err := Load(dir)
			want := at.Add(offset)
			if offset <= 0 {
				want = at.Add(time.Nanosecond)
			}
			if err != nil || !got.Paused || !got.UpdatedAt.Equal(want) {
				t.Fatalf("saved control: %+v, error: %v; want timestamp %s", got, err, want)
			}
		})
	}
}

func TestMissingFileIsDisabled(t *testing.T) {
	f, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if f.Enabled {
		t.Error("missing control file must mean disabled")
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := Update(dir, func(f *File) { f.Enabled = true; f.Paused = true; f.Note = "lunch" }); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Enabled || !f.Paused || f.Note != "lunch" || f.UpdatedAt.IsZero() {
		t.Errorf("unexpected %+v", f)
	}
	if f.Summary() != "paused" {
		t.Errorf("summary = %q", f.Summary())
	}
	g := f.Gate()
	if !g.Enabled || !g.Paused {
		t.Errorf("gate = %+v", g)
	}
}

func TestCompletionPersistsAndPreservesHigherPriorityHolds(t *testing.T) {
	dir := t.TempDir()
	if _, err := Update(dir, func(f *File) { f.Enabled, f.Completed = true, true; f.Note = "Example task finished" }); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil || !f.Completed || !f.Gate().Completed || f.Summary() != "completed" {
		t.Fatalf("completion not persisted: %+v %v", f, err)
	}
	f.Paused = true
	if f.Summary() != "paused" {
		t.Fatal("completion hid pause")
	}
	f.Stop = true
	if f.Summary() != "stop requested" {
		t.Fatal("completion hid stop")
	}
}
