package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/report"
)

func TestReportResumePreservesHoldsAndChecksOwnership(t *testing.T) {
	path, _ := sessionResetFixture(t, func(text string) string {
		return strings.Replace(text, "state_dir: state", "state_dir: state\n  report: {format: sessionpipe, scheme: bearer}", 1)
	})
	runCommand := func(args ...string) (int, string, string) { return runKeepgoing(append(args, "-c", path)...) }
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = control.Update(cfg.Loop.StateDir, func(c *control.File) {
		c.AwaitingUser = true
		c.Ownership = &control.Ownership{ThreadID: "thr_example_owner", Generation: 1}
	})
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(cfg.Loop.StateDir, report.SessionpipeGateFile)
	paused := []byte(`{"version":1,"status":"unauthorized"}`)
	if err = os.WriteFile(gate, paused, 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
	if code, out, errs := runCommand("status"); code != 0 || !strings.Contains(out, "Sessionpipe reports: unauthorized") {
		t.Fatalf("status %d %s %s", code, out, errs)
	}
	var status bytes.Buffer
	printDeliveryStatus(&status, report.InspectDeliveries(cfg.Loop.StateDir))
	if !strings.Contains(status.String(), "unauthorized") {
		t.Fatal(status.String())
	}

	t.Setenv("BB_THREAD_ID", "thr_example_outsider")
	if code, _, _ := runCommand("report", "resume"); code == 0 {
		t.Fatal("foreign caller resumed")
	}
	after, _ := os.ReadFile(gate)
	if !bytes.Equal(after, paused) {
		t.Fatal("foreign caller changed gate")
	}
	t.Setenv("BB_THREAD_ID", "")
	guard, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCommand("report", "resume"); code == 0 {
		t.Fatal("resumed during check")
	}
	guard.Release()
	for _, args := range [][]string{{"report", "resume", "--dry-run"}, {"report", "resume", "extra"}} {
		if code, _, _ := runCommand(args...); code == 0 {
			t.Fatal("invalid resume accepted")
		}
	}
	if code, out, errs := runCommand("report", "resume"); code != 0 || !strings.Contains(out, "no report sent or replayed") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	after, _ = os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("resume changed holds/ownership")
	}
	g, err := report.LoadSessionpipeGate(cfg.Loop.StateDir)
	if err != nil || g.Status != "ready" {
		t.Fatalf("%+v %v", g, err)
	}
}
