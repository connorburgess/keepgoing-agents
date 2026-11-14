package lock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// Held says whether a lock is held without creating anything, and leaves a
// free lock free.
func TestHeld(t *testing.T) {
	dir := t.TempDir()
	// Nothing there: not held, and nothing made.
	if held, err := Held(filepath.Join(dir, "absent"), Tick); held || err != nil {
		t.Fatalf("a missing directory: %v %v", held, err)
	}
	if held, err := Held(dir, Tick); held || err != nil {
		t.Fatalf("a missing lock file: %v %v", held, err)
	}
	if list, _ := os.ReadDir(dir); len(list) != 0 {
		t.Fatalf("asking created %v", list)
	}
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if held, err := Held(dir, Tick); !held || err != nil {
		t.Fatalf("a held lock: %v %v", held, err)
	}
	if held, err := HeldFile(filepath.Join(dir, Tick+".lock")); !held || err != nil {
		t.Fatalf("a held lock by its path: %v %v", held, err)
	}
	// Another lock in the same directory is its own.
	if held, err := Held(dir, Daemon); held || err != nil {
		t.Fatalf("another lock: %v %v", held, err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if held, err := Held(dir, Tick); held || err != nil {
		t.Fatalf("a released lock: %v %v", held, err)
	}
	// Asking did not leave it taken.
	l, err = Acquire(dir)
	if err != nil {
		t.Fatalf("asking left the lock held: %v", err)
	}
	l.Release()
}

func TestSharedReadersExcludeWritersAndReleaseIndependently(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireNamedShared(dir, "turn")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := AcquireNamedShared(dir, "turn")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if writer, err := AcquireNamed(dir, "turn"); !errors.Is(err, ErrHeld) {
		if writer != nil {
			writer.Release()
		}
		t.Fatalf("writer passed readers: %v", err)
	}
	first.Release()
	if writer, err := AcquireNamed(dir, "turn"); !errors.Is(err, ErrHeld) {
		if writer != nil {
			writer.Release()
		}
		t.Fatalf("writer passed remaining reader: %v", err)
	}
	second.Release()
	writer, err := AcquireNamed(dir, "turn")
	if err != nil {
		t.Fatal(err)
	}
	if reader, err := AcquireNamedShared(dir, "turn"); !errors.Is(err, ErrHeld) {
		if reader != nil {
			reader.Release()
		}
		t.Fatalf("reader passed writer: %v", err)
	}
	if err := writer.Release(); err != nil {
		t.Fatal(err)
	}
	reader, err := AcquireNamedShared(dir, "turn")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Release(); err != nil {
		t.Fatal(err)
	}
	// Failed contenders must close their descriptors; Windows removal verifies
	// this in addition to the writer's ability to acquire after reader release.
	if err := os.Remove(filepath.Join(dir, "turn.lock")); err != nil {
		t.Fatal(err)
	}
}
