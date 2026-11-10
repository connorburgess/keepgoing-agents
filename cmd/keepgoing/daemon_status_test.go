package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestDaemonStatusLifecycleAndStateDirReload(t *testing.T) {
	f := newDoctorFixture(t)
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
	go func() { done <- daemon(env{ctx, io.Discard, io.Discard}, cfg, f.config, false) }()
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
	f.loop["state_dir"] = other
	f.save(t)
	if _, err := control.Update(f.state, func(c *control.File) { c.Paused = true }); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		after, err := state.Load(f.state)
		if err != nil {
			t.Fatal(err)
		}
		if after.CheckedAt.After(before.CheckedAt) && after.LastCheck != nil && after.LastCheck.Reason == "paused" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control change did not trigger check")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("daemon moved to another state directory")
	}
	cancel()
	// Deferred cleanup joins the daemon; query absence is checked separately.
}
