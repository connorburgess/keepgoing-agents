package config

import (
	"errors"
	"os"
	"testing"
)

// With no -c, a command reads looptender.yaml in the working directory, or
// keepgoing.yaml when only that is there. With both there as two files it
// reads neither. Both there as one file, through a symbolic or a hard link,
// is one configuration, read by its current name. With neither it names the
// new file.
func TestDefaultPath(t *testing.T) {
	t.Chdir(t.TempDir())
	want := func(path string, err error) {
		t.Helper()
		got, gotErr := DefaultPath()
		if got != path || !errors.Is(gotErr, err) || (err == nil && gotErr != nil) {
			t.Fatalf("DefaultPath = %q %v, want %q %v", got, gotErr, path, err)
		}
	}
	want(FileName, nil)
	if err := os.WriteFile(LegacyFileName, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	want(LegacyFileName, nil)
	if err := os.WriteFile(FileName, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	want("", ErrDefaultAmbiguous)
	// A file whose name once stood for "both" is just a file.
	if err := os.WriteFile(FileName+"|"+LegacyFileName, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	want("", ErrDefaultAmbiguous)
	if err := os.Remove(FileName); err != nil {
		t.Fatal(err)
	}
	// One file under both names: a symbolic link, either way round, and a
	// hard link.
	for _, link := range []func() error{
		func() error { return os.Symlink(LegacyFileName, FileName) },
		func() error { return os.Link(LegacyFileName, FileName) },
	} {
		if err := link(); err != nil {
			t.Skipf("links are not available here: %v", err)
		}
		want(FileName, nil)
		if err := os.Remove(FileName); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(LegacyFileName, FileName); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(FileName, LegacyFileName); err != nil {
		t.Fatal(err)
	}
	want(FileName, nil)
	if err := os.Remove(LegacyFileName); err != nil {
		t.Fatal(err)
	}
	want(FileName, nil)
	// A link that leads nowhere is not the same file as anything.
	if err := os.Symlink("absent.yaml", LegacyFileName); err != nil {
		t.Fatal(err)
	}
	want("", ErrDefaultAmbiguous)
}
