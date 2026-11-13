package snapshot

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
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
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
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
		if strings.Contains(string(body), "SkipSyncInTests") {
			rel, _ := filepath.Rel(root, path)
			found = append(found, filepath.ToSlash(rel))
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
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	bin := filepath.Join(t.TempDir(), "flushprobe"+exe)
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/flushprobe").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
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
