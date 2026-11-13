package state

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// Deterministic form of the polling case: a reader opened the way Load opens
// the file is still holding it when the next save replaces it.
func TestStateSnapshotSaveReplacesFileHeldByReader(t *testing.T) {
	// Accented Latin and CJK directory names exercise the Windows path encoding.
	dir := filepath.Join(t.TempDir(), "café-项目")
	if err := Save(dir, File{LastState: "first"}); err != nil {
		t.Fatal(err)
	}
	held, err := snapshot.Open(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := Save(dir, File{LastState: "second"}); err != nil {
		t.Fatalf("save blocked by an open reader: %v", err)
	}
	if f, err := Load(dir); err != nil || f.LastState != "second" {
		t.Fatalf("replacement not visible: %+v %v", f, err)
	}
	// The held handle is the old snapshot, complete and unchanged.
	old, err := io.ReadAll(held)
	if err != nil || !strings.Contains(string(old), `"lastState":"first"`) {
		t.Fatalf("held snapshot: %q %v", old, err)
	}
}
