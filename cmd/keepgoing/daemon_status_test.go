package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestDaemonDiagnosticsRedactAndSeparateLiveFromHistory(t *testing.T) {
	dir := t.TempDir()
	s, err := daemoninfo.Start(dir, doctorPrivate, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	due := time.Now().Add(time.Minute)
	s.Update("waiting", &due)
	d := inspectDaemon(context.Background(), dir, "example-build", false)
	b, _ := json.Marshal(d)
	if d.State != "responsive" || d.BinaryComparison != "different" || d.NextCheckAt == nil || !d.NextCheckAt.Equal(due) || !d.DryRun || strings.Contains(string(b), doctorPrivate) {
		t.Fatalf("%s", b)
	}
	if offline := inspectDaemon(context.Background(), dir, "example-build", true); offline.State != "skipped" || offline.NextCheckAt != nil {
		t.Fatalf("%+v", offline)
	}
	s.Close()
	if stopped := inspectDaemon(context.Background(), dir, "example-build", false); stopped.State != "not_recorded" || stopped.NextCheckAt != nil {
		t.Fatalf("%+v", stopped)
	}
}

// The helper records any provider access; it never invokes a live provider.
func TestSnapshotDaemonProviderHelper(t *testing.T) {
	marker := os.Getenv("EXAMPLE_SNAPSHOT_PROVIDER")
	if marker == "" {
		t.Skip("subprocess helper")
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(8)
	}
	fmt.Fprintln(f, os.Args[len(os.Args)-1])
	f.Close()
	fmt.Println(`{"state":"active"}`)
	os.Exit(0)
}

type snapshotRejectionNotice chan struct{}

func (n snapshotRejectionNotice) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("no tick performed")) {
		select {
		case n <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func TestDaemonStatusLifecycleAndConfigReload(t *testing.T) {
	for _, change := range []string{"state-dir", "broken"} {
		t.Run(change, func(t *testing.T) { testDaemonSnapshotRecovery(t, change) })
	}
}

func testDaemonSnapshotRecovery(t *testing.T, change string) {
	f := newDoctorFixture(t)
	marker := filepath.Join(f.root, "provider-calls")
	delete(f.loop, "bb")
	f.loop["adapter"] = "process"
	command := func(op string) []string {
		return []string{f.executable, "-test.run=^TestSnapshotDaemonProviderHelper$", "--", op}
	}
	f.loop["process"] = map[string]any{"status_command": command("observe"), "resume_command": command("send"), "stop_command": command("stop"), "env": map[string]string{"EXAMPLE_SNAPSHOT_PROVIDER": marker}}
	f.loop["daemon"] = map[string]any{"interval": "10s", "control_poll": "100ms"}
	f.save(t)
	if err := control.Save(f.state, control.File{Enabled: true, Completed: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(f.config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	rejected := make(snapshotRejectionNotice, 8)
	go func() { done <- daemon(env{ctx, io.Discard, rejected}, cfg, f.config, false) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("daemon failed to stop")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d := inspectDaemon(context.Background(), f.state, version, false)
		if d.State == "responsive" && d.Phase == "waiting" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not begin waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var out bytes.Buffer
	if status(env{context.Background(), &out, &out}, cfg, time.Now(), true, defaultStaleAfter) != 0 {
		t.Fatal(out.String())
	}
	var result statusOut
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Daemon.State != "responsive" || result.ControlState != "completed" || result.Daemon.NextCheckAt == nil {
		t.Fatal(out.String())
	}
	_, report, _ := f.run(t, successfulDoctorRun)
	if report.Daemon == nil || report.Daemon.State != "responsive" || doctorCheckByID(t, report, "scheduler").Status != "ok" {
		t.Fatalf("%+v", report)
	}
	before, err := state.Load(f.state)
	if err != nil {
		t.Fatal(err)
	}
	// Changing state_dir must not leave a running daemon outside its lock
	// and diagnostic endpoint. A control change triggers the reload promptly.
	other := filepath.Join(f.root, "other-state")
	if change == "state-dir" {
		f.loop["state_dir"] = other
		f.save(t)
	} else if err := os.WriteFile(f.config, []byte("loop: [broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Update(f.state, func(c *control.File) { c.Paused = true; c.Stop = true; c.Completed = false }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rejected:
	case <-time.After(3 * time.Second):
		t.Fatal("control change did not trigger guarded check")
	}
	after, err := state.Load(f.state)
	if err != nil {
		t.Fatal(err)
	}
	if !after.CheckedAt.Equal(before.CheckedAt) {
		t.Fatal("rejected check rewrote recorded history")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unverified target received provider work")
	}
	d := inspectDaemon(context.Background(), f.state, version, false)
	if d.State != "responsive" {
		t.Fatalf("lost diagnostic binding: %+v", d)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("daemon moved to another state directory")
	}
	// Repair in place and change control metadata to request an ordinary reload.
	// The retained stop must win over pause once the target can be verified.
	f.loop["state_dir"] = f.state
	f.save(t)
	if _, err := control.Update(f.state, func(c *control.File) { c.Note = "fictional repair" }); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		after, err := state.Load(f.state)
		if err != nil {
			t.Fatal(err)
		}
		if after.CheckedAt.After(before.CheckedAt) && after.StopRequestedSent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("repaired config did not process pending stop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "observe\nstop\n" {
		t.Fatalf("provider calls after repair: %q, %v", calls, err)
	}
	ctl, err := control.Load(f.state)
	if err != nil || !ctl.Paused || !ctl.Stop {
		t.Fatalf("lost control holds: %+v, %v", ctl, err)
	}
	cancel()
	// Deferred cleanup joins the daemon; query absence is checked separately.
}
