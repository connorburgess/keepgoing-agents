//go:build unix

package testexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, path string, args ...string) string {
	t.Helper()
	out, err := exec.Command(path, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return string(out)
}

func TestScriptRunsBodyNormallyAndNeverDuringInstallation(t *testing.T) {
	for _, install := range []struct {
		name string
		fn   func(TB, string, string)
	}{{"script", Script}, {"shared", Shared}} {
		t.Run(install.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "ran")
			// Unique content per run, so a shared copy from an earlier run cannot hide a first installation.
			content := "#!/bin/sh\necho \"$1\" >> " + marker + "\necho body \"$1\"\n"
			path := filepath.Join(dir, "fake")
			removeShared(t, content)
			install.fn(t, path, content)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("installation ran the script body")
			}
			if out := run(t, path, "first"); out != "body first\n" {
				t.Fatalf("output %q", out)
			}
			// A second fixture gets the same behaviour and its own path.
			second := filepath.Join(dir, "second")
			install.fn(t, second, content)
			if out := run(t, second, "second"); out != "body second\n" {
				t.Fatalf("output %q", out)
			}
			if b, _ := os.ReadFile(marker); string(b) != "first\nsecond\n" {
				t.Fatalf("marker %q", b)
			}
			// Replacing one fixture's script must not change another's, even
			// when both were hard links to one shared file.
			Script(t, second, "#!/bin/sh\necho replaced\n")
			if out := run(t, second); out != "replaced\n" {
				t.Fatalf("replacement output %q", out)
			}
			if out := run(t, path, "third"); out != "body third\n" {
				t.Fatalf("replacement changed another fixture: %q", out)
			}
			// A later installation of the original content is unaffected too.
			third := filepath.Join(dir, "third")
			install.fn(t, third, content)
			if out := run(t, third, "fourth"); out != "body fourth\n" {
				t.Fatalf("output after replacement %q", out)
			}
		})
	}
}

func TestSharedIgnoresATamperedSharedCopy(t *testing.T) {
	dir := t.TempDir()
	content := "#!/bin/sh\necho genuine " + filepath.Base(dir) + "\n"
	first := filepath.Join(dir, "first")
	removeShared(t, content)
	Shared(t, first, content)
	// Writing through a link changes the shared file; the next fixture must
	// not inherit that.
	if err := os.WriteFile(first, []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "second")
	Shared(t, second, content)
	if out := run(t, second); !strings.HasPrefix(out, "genuine ") {
		t.Fatalf("fixture inherited a changed shared script: %q", out)
	}
}

// These tests use content unique to one run; do not leave its shared copy.
func removeShared(t *testing.T, content string) {
	t.Helper()
	_, master := sharedPath(guarded(t, content))
	t.Cleanup(func() { os.Remove(master) })
}

type recorder struct{ failed string }

func (r *recorder) Helper() {}
func (r *recorder) Fatalf(format string, args ...any) {
	r.failed = format
	panic(r)
}

func TestScriptRequiresShellInterpreterLine(t *testing.T) {
	r := &recorder{}
	func() {
		defer func() { recover() }()
		Script(r, filepath.Join(t.TempDir(), "fake"), "echo no interpreter\n")
	}()
	if r.failed == "" {
		t.Fatal("script without an interpreter line accepted")
	}
}
