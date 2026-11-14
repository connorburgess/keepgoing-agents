//go:build unix

package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBuildSecurityStoppedDaemonRefuses(t *testing.T) {
	if os.Getenv("KEEPGOING_BUILD_DAEMON_FIXTURE") == "1" {
		lk, e := lock.AcquireNamed(os.Getenv("KEEPGOING_BUILD_DAEMON_STATE"), lock.Daemon)
		if e != nil {
			os.Exit(2)
		}
		defer lk.Release()
		fmt.Println("locked")
		time.Sleep(time.Hour)
		return
	}
	w := newBuildWorld(t, "bin")
	r := w.review(t, buildReplaceAction)
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(exe, "-test.run=^TestBuildSecurityStoppedDaemonRefuses$")
	cmd.Env = append(os.Environ(), "KEEPGOING_BUILD_DAEMON_FIXTURE=1", "KEEPGOING_BUILD_DAEMON_STATE="+w.entry.StateDir)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Signal(syscall.SIGCONT); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "locked" {
		t.Fatal("daemon fixture did not lock")
	}
	if e = cmd.Process.Signal(syscall.SIGSTOP); e != nil {
		t.Fatal(e)
	}
	old := read(t, w.target)
	res := w.builds.apply(context.Background(), w.entry, buildReplaceAction, r.digest, noBuildDaemon, func() time.Time { return w.t0 })
	if res.Outcome == "done" || read(t, w.target) != old {
		t.Fatalf("stopped daemon allowed: %+v", res)
	}
}
