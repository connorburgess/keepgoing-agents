//go:build unix

package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// openWithin fails the test if OpenRegular blocks; it returns at once when
// it works as it should, so the bound only matters for a broken build.
func openWithin(t *testing.T, path string, noFollow bool) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		f, err := OpenRegular(path, noFollow)
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("OpenRegular(%s) blocked", filepath.Base(path))
		return nil
	}
}

func TestOpenRegularRefusesWhatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "record.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	pipeLink := filepath.Join(dir, "pipe-link")
	if err := os.Symlink(pipe, pipeLink); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path     string
		noFollow bool
		regular  bool
	}{
		{file, true, true}, {file, false, true},
		{link, false, true}, {link, true, false},
		{pipe, false, false}, {pipe, true, false},
		{pipeLink, false, false}, {pipeLink, true, false},
		{dir, false, false}, {"/dev/null", false, false},
	} {
		err := openWithin(t, tc.path, tc.noFollow)
		if tc.regular && err != nil {
			t.Errorf("%s (noFollow %v): %v", filepath.Base(tc.path), tc.noFollow, err)
		}
		if !tc.regular && !errors.Is(err, ErrNotRegular) {
			t.Errorf("%s (noFollow %v): got %v, want ErrNotRegular", filepath.Base(tc.path), tc.noFollow, err)
		}
	}
	if err := openWithin(t, filepath.Join(dir, "missing"), true); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

// A regular file opened this way reads normally.
func TestOpenRegularReadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(path, []byte(`{"example":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRegular(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, 64)
	n, err := f.Read(b)
	if err != nil || string(b[:n]) != `{"example":true}` {
		t.Fatal(n, err)
	}
}
