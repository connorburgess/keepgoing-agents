// Package testexec installs the shell-script stand-ins that tests execute in
// place of provider CLIs. It exists because of one host behaviour: macOS
// assesses a file the first time it is executed, serialises those assessments
// across the whole machine, and so makes the first run of a brand-new script
// take from half a second to many seconds while later runs take milliseconds.
// A test that writes a script and runs it under a deadline is then timing the
// host's queue, not the code under test.
//
// The helpers pay that cost while the fixture is being built, outside any
// deadline, by running the script once in a mode where it exits immediately.
// On other systems they only write the file. It is imported by tests only.
package testexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// TB is the part of testing.TB used here, so this package does not import
// testing into non-test code.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

const shebang = "#!/bin/sh\n"

// warmVariable makes a script installed by this package exit before its body.
const warmVariable = "KEEPGOING_TESTEXEC_WARM"

// guarded inserts the early exit after the interpreter line. The body is
// otherwise unchanged and runs normally whenever the variable is unset.
func guarded(t TB, content string) []byte {
	t.Helper()
	if !strings.HasPrefix(content, shebang) {
		t.Fatalf("testexec: script must start with %q", shebang)
	}
	return []byte(shebang + `[ -z "${` + warmVariable + `:-}" ] || exit 0` + "\n" + content[len(shebang):])
}

// Script writes an executable /bin/sh script to path, replacing any file
// there, and returns once the host will run it without a first-use delay.
func Script(t TB, path, content string) {
	t.Helper()
	body := guarded(t, content)
	// Remove first: the path may be a hard link shared with other fixtures.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("testexec: %v", err)
	}
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatalf("testexec: %v", err)
	}
	warm(t, path)
}

// Shared is Script for content that is identical across many fixtures. On
// macOS the script is assessed once per content and then hard-linked into each
// fixture, which costs nothing. The assessed copy lives in the user's temporary
// directory under a name derived from the content, so it is reused by later
// test runs and its number is bounded by the number of distinct scripts.
// Replace a path installed this way with Script, never by writing through it.
func Shared(t TB, path, content string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		Script(t, path, content)
		return
	}
	body := guarded(t, content)
	dir, master := sharedPath(body)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("testexec: %v", err)
	}
	// Trust the shared copy only while it still holds exactly this content.
	if existing, err := os.ReadFile(master); err == nil && bytes.Equal(existing, body) && os.Link(master, path) == nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("testexec: %v", err)
	}
	staging, err := os.CreateTemp(dir, ".staging-*")
	if err != nil {
		t.Fatalf("testexec: %v", err)
	}
	name := staging.Name()
	defer os.Remove(name)
	_, err = staging.Write(body)
	if err == nil {
		err = staging.Chmod(0o755)
	}
	if closeErr := staging.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("testexec: %v", err)
	}
	warm(t, name)
	// Publishing by rename keeps the assessed file and is atomic for other
	// test processes. The fixture gets its own link to the same file.
	if err := os.Rename(name, master); err != nil {
		t.Fatalf("testexec: %v", err)
	}
	if err := os.Link(master, path); err != nil {
		t.Fatalf("testexec: %v", err)
	}
}

func sharedPath(body []byte) (dir, master string) {
	sum := sha256.Sum256(body)
	dir = filepath.Join(os.TempDir(), "keepgoing-testexec")
	return dir, filepath.Join(dir, hex.EncodeToString(sum[:]))
}

// warm runs the script once with the early exit selected. It has no deadline
// on purpose: waiting here is the point.
func warm(t TB, path string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(), warmVariable+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("testexec: first run of %s failed: %v %s", filepath.Base(path), err, out)
	}
}
