package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Build the real entrypoint and a local process fixture. No provider CLI is
// available to this loop. This also exercises Windows detached pipe inheritance.
func TestStartBackgroundProcess(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("detached process integration")
	}
	dir := t.TempDir()
	t.Setenv("BB_THREAD_ID", "thr_start_fixture")
	build := func(name, source string) string {
		t.Helper()
		bin := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, source).CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
		return bin
	}
	bin := build("keepgoing", ".")
	fixtureSource := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(fixtureSource, []byte(`package main
import ("fmt"; "os"; "path/filepath")
func main() {
 if len(os.Args) != 2 { os.Exit(2) }
 mode := os.Args[1]
 if mode != "status" && mode != "stop" && mode != "resume" { os.Exit(2) }
 if err := os.WriteFile(filepath.Join(os.Getenv("START_FIXTURE_DIR"), mode), []byte("called"), 0600); err != nil { os.Exit(1) }
 if mode == "status" { fmt.Println("{\"state\":\"active\"}") }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureBin := build("fixture", fixtureSource)
	quote := func(v string) string { b, _ := json.Marshal(v); return string(b) }
	cfg := filepath.Join(dir, "keepgoing.yaml")
	contents := fmt.Sprintf(`version: 1
loop:
  name: example-start
  state_dir: state
  adapter: process
  process:
    status_command: [%[1]s, status]
    resume_command: [%[1]s, resume]
    stop_command: [%[1]s, stop]
    env: {START_FIXTURE_DIR: %[2]s}
  prompt: Continue the fictional example.
  daemon: {interval: 1h, control_poll: 100ms}
`, quote(fixtureBin), quote(dir))
	if err := os.WriteFile(cfg, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := control.Save(stateDir, control.File{Paused: true, Note: "fictional review"}); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) (int, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append(args, "-c", "keepgoing.yaml")...)
		cmd.Dir = dir // Parent passes an absolute path to the detached child.
		out, err := cmd.CombinedOutput()
		if err == nil {
			return 0, string(out)
		}
		var x *exec.ExitError
		if errors.As(err, &x) {
			return x.ExitCode(), string(out)
		}
		t.Fatalf("invoke: %v %s", err, out)
		return -1, ""
	}
	// A child-side validation failure must return its error, never claim readiness
	// or enable the loop. Restore the valid config before testing detachment.
	if err := os.WriteFile(cfg, []byte(strings.ReplaceAll(contents, quote(fixtureBin), quote(filepath.Join(dir, "absent")))), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := invoke("start", "--background"); code != exitFailed || !strings.Contains(out, "not executable") {
		t.Fatalf("failed startup: %d %s", code, out)
	}
	if ctl, err := control.Load(stateDir); err != nil || ctl.Enabled || ctl.Ownership != nil {
		t.Fatalf("failed startup changed control: %+v %v", ctl, err)
	}
	if err := os.WriteFile(cfg, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := invoke("start", "--background")
	if code != 0 {
		t.Fatalf("start: %d %s", code, out)
	}
	match := regexp.MustCompile(`PID (\d+)`).FindStringSubmatch(out)
	if len(match) != 2 {
		t.Fatalf("no PID: %s", out)
	}
	pid, _ := strconv.Atoi(match[1])
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = proc.Kill()
		_ = proc.Release()
		// Let Windows release inherited files before TempDir cleanup.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			l, err := lock.AcquireNamed(stateDir, lock.Daemon)
			if err == nil {
				l.Release()
				return
			}
		}
		t.Error("daemon did not release its process lock")
	})
	// Only synthetic files created by this test are eligible for failure output.
	// Read before killing the child so live daemon evidence is still available.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		info, err := daemoninfo.Query(ctx, stateDir)
		t.Logf("fixture daemon: %+v; query error: %v", info, err)
		for _, name := range []string{control.FileName, state.FileName, "incidents.json", state.LogName} {
			f, err := os.Open(filepath.Join(stateDir, name))
			if err != nil {
				t.Logf("fixture %s: %v", name, err)
				continue
			}
			b, err := io.ReadAll(io.LimitReader(f, 16*1024))
			f.Close()
			t.Logf("fixture %s (first 16 KiB): %s; read error: %v", name, b, err)
		}
		for _, name := range []string{"status", "stop", "resume"} {
			_, err := os.Stat(filepath.Join(dir, name))
			t.Logf("fixture %s marker: %v", name, err)
		}
	})
	waitFor := func(stage string, budget time.Duration, predicate func() bool) {
		t.Helper()
		started := time.Now()
		for deadline := started.Add(budget); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
			if predicate() {
				t.Logf("stage %s reached after %s", stage, time.Since(started))
				return
			}
		}
		t.Fatalf("background daemon did not reach %s", stage)
	}
	wait := func(stage string, predicate func() bool) {
		t.Helper()
		waitFor(stage, 5*time.Second, predicate)
	}
	query := func() (daemoninfo.Snapshot, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return daemoninfo.Query(ctx, stateDir)
	}
	wait("paused check", func() bool {
		st, _ := state.Load(stateDir)
		return st.LastCheck != nil && st.LastCheck.Reason == "paused"
	})
	before, err := daemoninfo.Query(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := control.Load(stateDir)
	if err != nil || !ctl.Enabled || !ctl.Paused || ctl.Ownership == nil || ctl.Ownership.ThreadID != "thr_start_fixture" {
		t.Fatalf("control: %+v %v", ctl, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "status")); !os.IsNotExist(err) {
		t.Fatal("paused start queried the fixture")
	}
	for _, mode := range []string{"--foreground", "--background"} {
		code, out := invoke("start", mode)
		if code != exitSkipped || !strings.Contains(out, "another daemon") {
			t.Fatalf("duplicate: %d %s", code, out)
		}
		got, err := control.Load(stateDir)
		if err != nil || got.UpdatedAt != ctl.UpdatedAt {
			t.Fatal("duplicate changed control")
		}
	}
	if code, out := invoke("status", "--json"); code != 0 || !strings.Contains(out, `"responsive"`) {
		t.Fatalf("status: %d %s", code, out)
	}
	// The launcher's process and pipes have exited; controls still reach the
	// daemon, and stop overrides the preserved pause within a control cycle.
	// A live daemon can contend for the diagnostic lock after a successful
	// control save. Force that documented partial-success path instead of
	// requiring a race-dependent zero exit. Never replay the control command.
	for _, operation := range []string{"pause", "stop"} {
		var diagnosticLock *lock.Lock
		wait(operation+" diagnostic lock", func() bool {
			var err error
			diagnosticLock, err = lock.AcquireNamed(stateDir, "incident")
			if errors.Is(err, lock.ErrHeld) {
				return false
			}
			if err != nil {
				t.Fatal(err)
			}
			return true
		})
		t.Cleanup(func() { diagnosticLock.Release() })
		code, out := invoke(operation)
		diagnosticLock.Release()
		if code != exitFailed || !strings.Contains(out, errControlHistory.Error()) {
			t.Fatalf("%s with diagnostic contention: %d %s", operation, code, out)
		}
		saved, err := control.Load(stateDir)
		if err != nil || !saved.Enabled || !saved.Paused || (saved.Stop != (operation == "stop")) || saved.Ownership == nil || saved.Ownership.ThreadID != "thr_start_fixture" {
			t.Fatalf("%s did not preserve the saved hold/owner: %+v %v", operation, saved, err)
		}
		if operation == "pause" {
			// Settle the repeated pause before stop, so a later checking phase
			// cannot be the earlier pause pass. The daemon's ordinary interval
			// is one hour; no unrelated pass should start during this test.
			wait("saved pause observed and waiting", func() bool {
				st, err := state.Load(stateDir)
				info, queryErr := query()
				return err == nil && st.LastCheck != nil && st.LastCheck.Reason == "paused" &&
					!st.CheckedAt.Before(saved.UpdatedAt) && queryErr == nil && info.Phase == "waiting"
			})
		}
	}
	// Check scheduling promptly, independently of OS subprocess startup latency.
	// A fast completed stop also proves the control was handled. No provider
	// work runs in the earlier hold.
	wait("stop check started", func() bool {
		info, err := query()
		st, stateErr := state.Load(stateDir)
		return (err == nil && info.Phase == "checking") || (stateErr == nil && st.StopRequestedSent)
	})
	// Observe and stop each have the configured adapter timeout. Completion is
	// a separate assertion from noticing control within the five-second bound.
	waitFor("stop marker", 2*config.DefaultProcessTimeout+5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dir, "stop"))
		return err == nil
	})
	wait("successful stop record", func() bool {
		st, _ := state.Load(stateDir)
		return st.StopRequestedSent && st.LastCheck != nil && st.LastCheck.Outcome == "succeeded"
	})
	if _, err := os.Stat(filepath.Join(dir, "resume")); !os.IsNotExist(err) {
		t.Fatal("unexpected prompt sent")
	}
	after, err := daemoninfo.Query(context.Background(), stateDir)
	if err != nil || !before.StartedAt.Equal(after.StartedAt) {
		t.Fatalf("daemon replaced: %v", err)
	}
}
