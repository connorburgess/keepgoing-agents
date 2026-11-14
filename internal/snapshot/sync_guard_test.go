package snapshot

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/testexec"
)

// The packages whose tests skip the flush, each with the file that says so.
// Adding one is a decision: a package that tests durability, or that runs
// the built binary, must not be here. Update "Slow test packages" in
// docs/testing.md with it.
var skipsTheFlush = []string{
	"cmd/keepgoing/sync_test.go",
	"internal/supervise/sync_test.go",
}

// Only the listed _test.go files may call SkipSyncInTests. No file that is
// compiled into a command may mention it.
func TestOnlyNamedTestFilesSkipTheFlush(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := map[string]bool{
		// The definition, its own tests, and the probe that shows it panics.
		"internal/snapshot/sync.go":                     true,
		"internal/snapshot/sync_test.go":                true,
		"internal/snapshot/sync_guard_test.go":          true,
		"internal/snapshot/testdata/flushprobe/main.go": true,
		"internal/scenario/built_binary_test.go":        true,
	}
	for _, f := range skipsTheFlush {
		allowed[f] = true
	}
	flagFiles := map[string]bool{
		"internal/snapshot/sync.go":              true,
		"internal/snapshot/sync_test.go":         true,
		"internal/snapshot/sync_guard_test.go":   true,
		"internal/scenario/built_binary_test.go": true,
	}
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			// Another copy of the source, such as a test bundle unpacked
			// inside the checkout, is a module of its own and is not built
			// from here.
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.Contains(string(body), "SkipSyncInTests") {
			found = append(found, rel)
		}
		// The flag itself has one writer. Naming it anywhere else, for
		// example in a go:linkname directive, is a way round the function.
		if strings.Contains(string(body), "skipSync") && !flagFiles[rel] {
			t.Errorf("%s names the skipSync flag. Only internal/snapshot/sync.go may read or set it.", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	seen := map[string]bool{}
	for _, f := range found {
		seen[f] = true
		if !allowed[f] {
			t.Errorf("%s mentions SkipSyncInTests. Only the test packages listed in skipsTheFlush may skip the flush, and never a file compiled into a command.", f)
		}
	}
	for _, f := range skipsTheFlush {
		if !seen[f] {
			t.Errorf("%s is listed as skipping the flush but does not; remove it from the list and from docs/testing.md", f)
		}
		if !strings.HasSuffix(f, "_test.go") {
			t.Errorf("%s is not a test file", f)
		}
	}
}

// An ordinary build, like the keepgoing command's, cannot skip the flush:
// the call panics, and the flush is attempted before and after it. The
// environment and arguments a test binary would see make no difference.
func TestFlushCannotBeSkippedOutsideATestBinary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("not run: building an ordinary program needs the Go toolchain, which is not on PATH (as in the test bundle for machines without Go)")
	}
	// Built and run once through testexec, so the host's first-run check of
	// a new file is passed before the cases below depend on it.
	d, err := testexec.NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin, err := d.Build("flushprobe", "./testdata/flushprobe", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "open file flushed: true\nflush attempted before skip: true\nskip refused: true\nflush attempted after skip: true\n"
	for _, tc := range []struct {
		name string
		args []string
		env  []string
	}{
		{"plain", nil, nil},
		{"with a test flag and test-like environment", []string{"-test.v", "-test.run=x"}, []string{"GOFLAGS=-test.v", "GO_TEST=1", "KEEPGOING_SKIP_SYNC=1", "KEEPGOING_TEST_BINARY=" + bin}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			cmd.Env = append(os.Environ(), tc.env...)
			out, err := cmd.CombinedOutput()
			if got := strings.ReplaceAll(string(out), "\r\n", "\n"); err != nil || got != want {
				t.Fatalf("%v\n%s\nwant:\n%s", err, got, want)
			}
		})
	}
}
