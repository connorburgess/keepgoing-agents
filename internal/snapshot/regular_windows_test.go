package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRegularOnWindows(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "record.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, noFollow := range []bool{false, true} {
		f, err := OpenRegular(file, noFollow)
		if err != nil {
			t.Fatal(noFollow, err)
		}
		f.Close()
	}
	if _, err := OpenRegular(dir, false); err == nil {
		t.Fatal("a directory was opened as a regular file")
	}
	if _, err := OpenRegular(`\\.\NUL`, false); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("NUL device: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if f, err := OpenRegular(link, false); err != nil {
		t.Fatalf("following a link: %v", err)
	} else {
		f.Close()
	}
	if _, err := OpenRegular(link, true); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("link with noFollow: %v", err)
	}
}
