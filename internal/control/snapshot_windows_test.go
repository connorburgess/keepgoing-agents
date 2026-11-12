package control

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWindowsControlSnapshotLegacyShareBlocksReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "café-项目")
	if err := Save(dir, File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly reproduce the former sharing mode, rather than assuming every
	// future Go release's os.Open must omit DELETE sharing.
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = syscall.CloseHandle(handle)
		}
	}()
	if err := Save(dir, File{Enabled: true, Paused: true}); err == nil {
		t.Fatal("legacy handle did not exclude replacement")
	}
	assertNoSnapshotTemps(t, dir)
	got, err := Load(dir)
	if err != nil || got.Paused {
		t.Fatalf("rejected replacement changed control: %+v %v", got, err)
	}
	if err := syscall.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	closed = true
	if err := Save(dir, File{Enabled: true, Paused: true}); err != nil {
		t.Fatalf("replacement after close: %v", err)
	}
	got, err = Load(dir)
	if err != nil || !got.Paused {
		t.Fatalf("replacement not visible: %+v %v", got, err)
	}
}

func TestWindowsControlSnapshotOpenFailureDoesNotCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nul\x00path")
	if f, err := OpenSnapshot(dir); err == nil || f != nil {
		t.Fatalf("invalid path: %v %v", f, err)
	}
	// An existing directory at control.json cannot become a regular snapshot.
	dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, FileName), 0700); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenSnapshot(dir); err == nil || f != nil {
		t.Fatalf("directory opened as snapshot: %v %v", f, err)
	}
}

func assertNoSnapshotTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("staging file not cleaned: %s", entry.Name())
		}
	}
}

func TestWindowsControlSnapshotReadOnlyTargetPreserved(t *testing.T) {
	dir := t.TempDir()
	original := File{Enabled: true, Note: "original control"}
	if err := Save(dir, original); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if err := Save(dir, File{Paused: true}); err == nil {
		t.Fatal("replaced read-only control")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed save changed control: %v", err)
	}
	assertNoSnapshotTemps(t, dir)
}
