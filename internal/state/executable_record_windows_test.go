package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsCheckExecutableMarkedAbsenceIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	if got, err := LoadCheckExecutable(dir); err != nil || got != nil {
		t.Fatalf("never-written record: %+v %v", got, err)
	}
	record := CheckExecutable{Version: 1, Path: filepath.Join(dir, "example-keepgoing.exe"), Label: "v1.0.0", At: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Identity: FileIdentity{Dev: 1, Ino: 1, Size: 1, ModNs: 1, Nlink: 1}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	// The metadata reader is portable even though Windows cannot record a
	// running image identity or replace a build through the panel.
	if err := saveAllowance(dir, CheckExecutableName, data); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadCheckExecutable(dir); err != nil || got == nil || *got != record {
		t.Fatalf("published record: %+v %v", got, err)
	}
	if err := os.Remove(filepath.Join(dir, CheckExecutableName)); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadCheckExecutable(dir); err == nil || errors.Is(err, os.ErrNotExist) || got != nil {
		t.Fatalf("marked absence lost executable evidence: %+v %v", got, err)
	}
}
