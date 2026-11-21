package report

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBBPushCLIBindingWithoutExecution(t *testing.T) {
	dir := t.TempDir()
	selected := filepath.Join(dir, "configured-cli")
	other := filepath.Join(dir, "other-cli")
	// A successful invocation would fail this test's contract: this helper only
	// resolves metadata and is safe for construction and dry-run paths.
	for _, path := range []string{selected, other} {
		if err := os.WriteFile(path, []byte("not an executable image\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := filepath.EvalSymlinks(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, inherited := range []string{"", selected} {
		got, err := resolveBBPushCLI(selected, inherited)
		if err != nil || got != want {
			t.Fatalf("matching configured runtime refused: %q %v", got, err)
		}
	}
	for _, tc := range [][2]string{
		{selected, other}, {selected, "relative-cli"}, {"relative-cli", ""},
		{selected, filepath.Join(dir, "missing")}, {dir, ""}, {"", ""},
	} {
		got, err := resolveBBPushCLI(tc[0], tc[1])
		if got != "" || err != errBBPushCLI {
			t.Fatalf("unsafe binding accepted or raw path error exposed: %q %v", got, err)
		}
	}
}
