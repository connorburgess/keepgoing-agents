//go:build !windows

package state

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckExecutableIdentityAndStrictLoader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keepgoing")
	if e := os.WriteFile(path, []byte("fixture"), 0700); e != nil {
		t.Fatal(e)
	}
	at := time.Now()
	if e := SaveCheckExecutable(dir, path, "v1.0.0", at); e != nil {
		t.Fatal(e)
	}
	record := filepath.Join(dir, CheckExecutableName)
	original, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	if e = SaveCheckExecutable(dir, path, "v1.0.0", at.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	same, _ := os.ReadFile(record)
	if !bytes.Equal(original, same) {
		t.Fatal("unchanged identity rewrote record")
	}
	for _, bad := range [][]byte{
		bytes.Replace(original, []byte(`"path":`), []byte(`"Path":`), 1),
		bytes.Replace(original, []byte(`"dev":`), []byte(`"Dev":`), 1),
		bytes.Replace(original, []byte(`"ino":`), []byte(`"ino":1,"ino":`), 1),
		bytes.Replace(original, []byte(`"ino":`), []byte(`"Ino":1,"ino":`), 1),
		append(append([]byte{}, original...), []byte(` {}`)...),
	} {
		if e = os.WriteFile(record, bad, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = LoadCheckExecutable(dir); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	for _, bad := range []string{path + "\x00", path + "\n", filepath.Dir(path) + "/./keepgoing"} {
		if CleanPath(bad) {
			t.Fatalf("clean: %q", bad)
		}
	}
}
