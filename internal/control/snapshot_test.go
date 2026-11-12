package control

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlSnapshotAllowsAtomicReplacement(t *testing.T) {
	for _, name := range []string{"unicode", "long-unicode", "relative"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "café-项目")
			if name == "relative" {
				t.Chdir(base)
				dir = "café-项目"
			}
			if name == "long-unicode" {
				for len(dir) < 300 {
					dir = filepath.Join(dir, strings.Repeat("part", 8))
				}
			}
			original := File{Enabled: true, Note: "fictional snapshot"}
			if err := Save(dir, original); err != nil {
				t.Fatal(err)
			}
			snapshot, err := OpenSnapshot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close()
			changed := original
			changed.Paused = true
			// The production reader remains open throughout the actual atomic writer.
			// With the old Windows os.Open sharing mode this replacement is rejected.
			if err := Save(dir, changed); err != nil {
				t.Fatalf("reader blocked replacement: %v", err)
			}
			body, err := io.ReadAll(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var old File
			if err := json.Unmarshal(body, &old); err != nil {
				t.Fatal(err)
			}
			if !old.Enabled || old.Paused || old.Note != original.Note {
				t.Fatalf("opened snapshot changed: %+v", old)
			}
			current, err := Load(dir)
			if err != nil || !current.Enabled || !current.Paused || current.Note != original.Note {
				t.Fatalf("new snapshot: %+v %v", current, err)
			}
			if _, err := snapshot.Write([]byte("must not write")); err == nil {
				t.Fatal("snapshot handle has write access")
			}
		})
	}
}

func TestControlSnapshotMissingDoesNotCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if f, err := OpenSnapshot(dir); !errors.Is(err, os.ErrNotExist) || f != nil {
		t.Fatalf("missing open: %v %v", f, err)
	}
	if f, err := Load(dir); err != nil || f.Enabled {
		t.Fatalf("missing load: %+v %v", f, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reader created directory: %v", err)
	}
}
