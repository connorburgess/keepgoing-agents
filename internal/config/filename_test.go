package config

import (
	"errors"
	"os"
	"testing"
)

// With no -c, a command reads looptender.yaml in the working directory, or
// keepgoing.yaml when only that is there. With both there it reads neither,
// and says which two and what to do. With neither it names the new file.
func TestDefaultPath(t *testing.T) {
	t.Chdir(t.TempDir())
	if got := DefaultPath(); got != FileName {
		t.Fatalf("neither: %q", got)
	}
	if err := os.WriteFile(LegacyFileName, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DefaultPath(); got != LegacyFileName {
		t.Fatalf("the earlier name only: %q", got)
	}
	if err := os.WriteFile(FileName, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := DefaultPath()
	if _, err := Load(got); !errors.Is(err, ErrDefaultAmbiguous) {
		t.Fatalf("both: %q %v", got, err)
	}
	if err := os.Remove(LegacyFileName); err != nil {
		t.Fatal(err)
	}
	if got := DefaultPath(); got != FileName {
		t.Fatalf("the current name only: %q", got)
	}
	// The stand-in for "both" is never a file Load would open.
	if _, err := os.Lstat(ambiguousDefault); err == nil {
		t.Fatal("the stand-in exists as a file")
	}
}
