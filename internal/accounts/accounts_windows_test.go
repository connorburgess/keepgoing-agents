package accounts

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/snapshot"
)

// Deterministic regression for the 8de59438 reader: os.Lstat says missing
// after the primary is removed, while strict replacement's marker says that
// its absence is unavailable. Before correction Load returns a new empty file.
func TestWindowsAccountsMarkedMissingNeverResetsCatalog(t *testing.T) {
	dir, _ := fixture(t)
	path := filepath.Join(dir, FileName)
	if _, err := os.Stat(path + ".present"); err != nil {
		t.Fatal("writer did not publish marker", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	} // deliberately retain marker
	if f, err := Load(dir); err == nil {
		t.Fatalf("marked-missing catalog became empty: %+v", f)
	}
	view := ReadView(dir, time.Now())
	if view.Complete || view.State != "unavailable" {
		t.Fatal("lost catalog projected as empty")
	}
	called := false
	err := Update(dir, func(*File) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("marked-missing catalog entered update/new-file path")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing primary was replaced")
	}
	if _, err := os.Stat(path + ".present"); err != nil {
		t.Fatal("marker was removed")
	}
}
func TestWindowsAccountsNeverCreatedIsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "catalog")
	f, err := Load(dir)
	if err != nil || len(f.Entries) != 0 || f.SchemaVersion != 1 {
		t.Fatalf("genuine empty: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created storage")
	}
}
func TestWindowsAccountsHeldReaderAllowsReplacement(t *testing.T) {
	dir, _ := fixture(t)
	path := filepath.Join(dir, FileName)
	held, err := snapshot.OpenRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	before, err := io.ReadAll(held)
	if err != nil {
		t.Fatal(err)
	}
	if err = Update(dir, func(f *File) error { f.Entries[0].Label = "Example replacement"; return nil }); err != nil {
		t.Fatal("replacement blocked by reader", err)
	}
	if _, err = held.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old, err := io.ReadAll(held)
	if err != nil || string(old) != string(before) {
		t.Fatal("held reader lost old snapshot")
	}
	current, err := Load(dir)
	if err != nil || current.Entries[0].Label != "Example replacement" {
		t.Fatal("replacement not readable")
	}
}
