package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestWindowsBuildReceiptMarkedAbsenceIsUnavailable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows record marker contract")
	}
	dir := t.TempDir()
	if got, err := loadBuildUpdate(dir); err != nil || got != nil {
		t.Fatalf("never-written receipt: %+v %v", got, err)
	}
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	receipt := buildUpdate{Version: 1, At: at, Backup: at.Format("20060102T150405.000000000Z"), Target: filepath.Join(dir, "example-keepgoing.exe"), Mode: 0700, FromLabel: "v1.0.0", ToLabel: "v2.0.0", FromSHA: strings.Repeat("1", 64), ToSHA: strings.Repeat("2", 64), Identity: state.FileIdentity{Ino: 1, Nlink: 1}}
	// Test the private metadata helper, not Windows build replacement, which
	// remains unavailable and is never invoked by this fixture.
	if err := saveBuildUpdate(dir, receipt); err != nil {
		t.Fatal(err)
	}
	if got, err := loadBuildUpdate(dir); err != nil || got == nil || *got != receipt {
		t.Fatalf("published receipt: %+v %v", got, err)
	}
	if err := os.Remove(filepath.Join(dir, buildUpdateName)); err != nil {
		t.Fatal(err)
	}
	if got, err := loadBuildUpdate(dir); err == nil || errors.Is(err, os.ErrNotExist) || got != nil {
		t.Fatalf("marked absence lost Undo evidence: %+v %v", got, err)
	}
}

func TestBuildBackupPreservesRecordSidecarsWithoutReceipt(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"ordinary-record-sidecars": {"state.json": "state", "state.json.present": "", "lanes.key": "key", "lanes.key.present": ""},
		"excluded-legacy-receipt":  {buildUpdateName: "receipt", buildUpdateName + ".present": ""},
	} {
		t.Run(name, func(t *testing.T) {
			src, dst := t.TempDir(), filepath.Join(t.TempDir(), "backup")
			for file, data := range files {
				if err := os.WriteFile(filepath.Join(src, file), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := buildCopyTree(src, dst, filepath.Join(src, buildBackupsDir)); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dst)
			if err != nil {
				t.Fatal(err)
			}
			if name == "excluded-legacy-receipt" {
				if len(entries) != 0 {
					t.Fatalf("copied excluded receipt authority: %v", entries)
				}
				return
			}
			if len(entries) != len(files) {
				t.Fatalf("lost record or sidecar: %v", entries)
			}
			for file, want := range files {
				if got, err := os.ReadFile(filepath.Join(dst, file)); err != nil || string(got) != want {
					t.Fatalf("backup %s: %q %v", file, got, err)
				}
			}
		})
	}
}
