package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsMissingMarkedLaneKeyIsNotRegenerated(t *testing.T) {
	dir := t.TempDir()
	if _, err := LaneKey(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, LaneKeyName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := LaneKey(dir); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marked key absence must refuse regeneration: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("regenerated a different lane identity key: %v", err)
	}
}
