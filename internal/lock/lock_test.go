package lock

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestSecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	// flock is per open file description, so the contender must be another process.
	if os.Getenv("LOCK_CHILD") == "1" {
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestChildAcquire")
	cmd.Env = append(os.Environ(), "LOCK_CHILD=1", "LOCK_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child acquired a held lock:\n%s", out)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(os.Args[0], "-test.run=TestChildAcquire")
	cmd.Env = append(os.Environ(), "LOCK_CHILD=1", "LOCK_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child could not acquire a released lock: %v\n%s", err, out)
	}
}

func TestChildAcquire(t *testing.T) {
	if os.Getenv("LOCK_CHILD") != "1" {
		t.Skip("helper for TestSecondAcquireFails")
	}
	l, err := Acquire(os.Getenv("LOCK_DIR"))
	if errors.Is(err, ErrHeld) {
		os.Exit(3)
	}
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
}
