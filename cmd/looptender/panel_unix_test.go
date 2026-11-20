//go:build unix

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	// The stop was reported as failed, so it must not happen later: resumed
	// at once, the server finds that request too old and keeps running.
	if err := process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if again, ok := panelLive(context.Background(), w.dir); !ok || again != rec || w.serverProcesses() != 1 {
		t.Fatalf("a stop that was reported as failed was carried out on resume: %+v %v", again, ok)
	}
	if err := process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
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

// The control socket is private from the instant it exists, whatever the
// process's umask was: it takes its mode at bind, and is not made private
// afterwards.
func TestPanelSocketIsPrivateAtBind(t *testing.T) {
	previous := syscall.Umask(0) // the most permissive a caller could leave
	defer syscall.Umask(previous)
	path := filepath.Join(panelShortDir(t), "panel.sock")
	l, info, err := panelListenPrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if info.Mode().Perm()&0o077 != 0 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket created as %v", info.Mode())
	}
	// The process's umask is put back, so nothing else it creates changes.
	if now := syscall.Umask(0); now != 0 {
		t.Fatalf("umask left at %o", now)
	}
	// Closing does not remove the file by name; the server does that itself,
	// and only for a socket it created.
	_ = l.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("closing removed the socket file: %v", err)
	}
}

// Deleting the lock file under a running server lets a second one start. The
// two must not persist, and the first must not take the second's socket with
// it when it goes.
func TestPanelServerWhoseLockFileIsDeletedExitsAndLeavesTheNewOneWorking(t *testing.T) {
	w := newPanelProcessWorld(t)
	if code, out, errs := w.open("--no-open"); code != exitOK {
		t.Fatalf("open: %d %s %s", code, out, errs)
	}
	first := w.record()
	if err := os.Remove(filepath.Join(w.dir, panelServerLock+".lock")); err != nil {
		t.Fatal(err)
	}
	code, out, errs := w.open("--no-open")
	if code != exitOK || !strings.Contains(out, "Open this address") {
		t.Fatalf("open after the lock file was deleted: %d %s %s", code, out, errs)
	}
	second := w.record()
	if second.PID == first.PID || second.Instance == first.Instance {
		t.Fatalf("no second server: %+v", second)
	}
	// The first notices within its next check that its lock is not the file
	// at that path any more, and exits without touching the second's files.
	deadline := time.Now().Add(15 * time.Second)
	for w.serverProcesses() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("%d servers persist", w.serverProcesses())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if process, err := os.FindProcess(first.PID); err == nil && process.Signal(syscall.Signal(0)) == nil {
		t.Fatal("the first server is still running")
	}
	time.Sleep(300 * time.Millisecond)
	if again, ok := panelLive(context.Background(), w.dir); !ok || again != second {
		t.Fatalf("the second server stopped answering after the first exited: %+v %v", again, ok)
	}
	for _, name := range []string{panelSocketName, panelRecordName} {
		if _, err := os.Lstat(filepath.Join(w.dir, name)); err != nil {
			t.Fatalf("the first server removed the second's %s: %v", name, err)
		}
	}
	page, c := panelSession(t, w.dir, second)
	if resp, _ := dockRequest(t, http.MethodGet, page+dockAPIPath, withCookie(c)); resp.StatusCode != http.StatusOK {
		t.Fatalf("the second server's page: %d", resp.StatusCode)
	}
	if code, out, _ := w.run("panel", "status", "--panel-directory", w.dir); code != exitOK || !strings.Contains(out, fmt.Sprintf("port %d", second.Port)) {
		t.Fatalf("status: %d %s", code, out)
	}
}
