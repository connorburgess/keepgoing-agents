package config

import (
	"errors"
	"os"
	"testing"
)

func wantDefault(t *testing.T, path string, err error) {
	t.Helper()
	got, gotErr := DefaultPath()
	if got != path || !errors.Is(gotErr, err) || (err == nil && gotErr != nil) {
		t.Fatalf("DefaultPath = %q %v, want %q %v", got, gotErr, path, err)
	}
}

// With no -c, a command reads looptender.yaml in the working directory, or
// keepgoing.yaml when only that is there. With both there as two files it
// reads neither. With neither it names the new file.
func TestDefaultPath(t *testing.T) {
	t.Chdir(t.TempDir())
	wantDefault(t, FileName, nil)
	if err := os.WriteFile(LegacyFileName, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantDefault(t, LegacyFileName, nil)
	if err := os.WriteFile(FileName, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantDefault(t, "", ErrDefaultAmbiguous)
	// A file whose name once stood for "both" is just a file.
	if err := os.WriteFile(FileName+"|"+LegacyFileName, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wantDefault(t, "", ErrDefaultAmbiguous)
	if err := os.Remove(LegacyFileName); err != nil {
		t.Fatal(err)
	}
	wantDefault(t, FileName, nil)
}

// Both names as one file are one configuration, read by the name that is
// the file itself: keepgoing.yaml as a link to looptender.yaml (the
// arrangement the guide recommends), the other way round, and a hard link.
// A link that leads nowhere is not the same file as anything.
func TestDefaultPathWithLinks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func() error
		want  string
		err   error
	}{
		{"keepgoing.yaml links to looptender.yaml", func() error {
			if err := os.WriteFile(FileName, nil, 0o600); err != nil {
				return err
			}
			return os.Symlink(FileName, LegacyFileName)
		}, FileName, nil},
		{"looptender.yaml links to keepgoing.yaml", func() error {
			if err := os.WriteFile(LegacyFileName, nil, 0o600); err != nil {
				return err
			}
			return os.Symlink(LegacyFileName, FileName)
		}, LegacyFileName, nil},
		{"a hard link", func() error {
			if err := os.WriteFile(LegacyFileName, nil, 0o600); err != nil {
				return err
			}
			return os.Link(LegacyFileName, FileName)
		}, FileName, nil},
		{"a link to nothing beside a file", func() error {
			if err := os.WriteFile(FileName, nil, 0o600); err != nil {
				return err
			}
			return os.Symlink("absent.yaml", LegacyFileName)
		}, "", ErrDefaultAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := tc.setup(); err != nil {
				t.Skipf("links are not available here: %v", err)
			}
			wantDefault(t, tc.want, tc.err)
		})
	}
}
