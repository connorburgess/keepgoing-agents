package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exampleSession = "00000000-0000-4000-8000-000000000001"

func TestInspectSessionOnlyReadsBoundedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, content  string
		missing, valid bool
	}{
		{"missing", "", true, false},
		{"saved", exampleSession + "\n", false, true},
		{"empty", "", false, false},
		{"prose", "PRIVATE_SESSION_CONTENT", false, false},
		{"multiline", exampleSession + "\nPRIVATE_SESSION_CONTENT", false, false},
		{"oversize", strings.Repeat("x", 1024), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Dir(t.TempDir())
			// Turn files are deliberately unreadable as files. Inspection must
			// neither consult them nor recover an ID from provider output.
			for _, name := range []string{"stdout", "stderr", "prompt", "run.json"} {
				if err := os.Mkdir(d.path(name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.missing {
				if err := os.WriteFile(d.path("session"), []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			id, err := d.InspectSession()
			if tc.valid {
				if err != nil || id != exampleSession {
					t.Fatalf("%q %v", id, err)
				}
			} else if tc.missing {
				if err != nil || id != "" {
					t.Fatalf("%q %v", id, err)
				}
				if _, err := os.Stat(d.path("session")); !os.IsNotExist(err) {
					t.Fatal("created session")
				}
			} else if err == nil || id != "" || strings.Contains(err.Error(), "PRIVATE_SESSION_CONTENT") {
				t.Fatalf("unexpected result %q %v", id, err)
			}
			if !tc.missing {
				b, err := os.ReadFile(d.path("session"))
				if err != nil || string(b) != tc.content {
					t.Fatal("changed saved ID")
				}
				if d.Session() != strings.TrimSpace(tc.content) {
					t.Fatal("diagnostics changed runner behavior")
				}
			}
		})
	}
}

func TestInspectSessionRejectsNonRegularRecords(t *testing.T) {
	d := Dir(t.TempDir())
	if err := os.Mkdir(d.path("session"), 0o700); err != nil {
		t.Fatal(err)
	}
	if id, err := d.InspectSession(); id != "" || err == nil {
		t.Fatal("accepted directory")
	}
	if err := os.Remove(d.path("session")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(target, []byte(exampleSession), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, d.path("session")); err != nil {
		t.Skip("symlink unavailable")
	}
	if id, err := d.InspectSession(); id != "" || err == nil {
		t.Fatal("followed session symlink")
	}
}
