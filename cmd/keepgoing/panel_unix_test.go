//go:build unix

package main

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A server that is suspended holds its lock and does not answer. Status says
// exactly that, the commands fail with a plain message and no file to delete,
// and everything works again when it resumes.
func TestPanelSuspendedServerIsRunningButNotAnswering(t *testing.T) {
	w := newPanelProcessWorld(t)
	if code, out, errs := w.open(); code != exitOK {
		t.Fatalf("open: %d %s %s", code, out, errs)
	}
	rec := w.record()
	process, err := os.FindProcess(rec.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.SIGSTOP); err != nil {
		t.Skipf("cannot suspend the server: %v", err)
	}
	resumed := false
	resume := func() {
		if !resumed {
			_ = process.Signal(syscall.SIGCONT)
			resumed = true
		}
	}
	defer resume()
	code, out, _ := w.run("panel", "status", "--panel-directory", w.dir)
	if code != exitOK || !strings.Contains(out, "running but not answering") || strings.Contains(out, "not running") {
		t.Fatalf("status while suspended: %d %s", code, out)
	}
	if code, _, errs := w.run("panel", "stop", "--panel-directory", w.dir); code != exitFailed || !strings.Contains(errs, "running but not answering") || strings.Contains(errs, "delete") {
		t.Fatalf("stop while suspended: %d %s", code, errs)
	}
	started := time.Now()
	code, _, errs := w.open("--no-open")
	if code != exitFailed || !strings.Contains(errs, "running but not answering") || strings.Contains(errs, "exited while starting") {
		t.Fatalf("open while suspended: %d %s", code, errs)
	}
	if waited := time.Since(started); waited > 30*time.Second {
		t.Fatalf("open waited %s", waited)
	}
	if n := w.serverProcesses(); n != 1 {
		t.Fatalf("%d server processes: a second one was started beside the suspended one", n)
	}
	resume()
	if code, out, errs := w.open("--no-open"); code != exitOK || !strings.HasPrefix(out, "Open this address") {
		t.Fatalf("open after it resumed: %d %s %s", code, out, errs)
	}
	if again := w.record(); again != rec {
		t.Fatalf("a different server after resuming: %+v", again)
	}
}
