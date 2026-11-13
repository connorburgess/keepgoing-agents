package control

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWritePrivateFileReplacesUnderAnOpenSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	if err := WritePrivateFile(path, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenPrivateSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := WritePrivateFile(path, []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	info, _ := os.Stat(path)
	if err != nil || string(b) != "second\n" || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("%q %v %v", b, info, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
	if err := WritePrivateFile(filepath.Join(dir, "absent", "record.json"), nil); err == nil {
		t.Fatal("a missing directory was created")
	}
}
