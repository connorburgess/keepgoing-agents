//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func controlHistory(t *testing.T, f *fixture) incident.Journal {
	t.Helper()
	j, err := incident.Load(filepath.Join(f.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestControlHistoryAcrossCommandsAndHeldTicks(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_private_fixture_owner")
	start := time.Now()
	commands := []string{"enable", "pause", "stop", "await-user", "complete", "disable", "resume", "enable", "enable"}
	for _, cmd := range commands {
		if code, _, errs := f.run(cmd, "--note", "PRIVATE_CONTROL_NOTE"); code != 0 {
			t.Fatal(cmd, errs)
		}
		if cmd == "pause" {
			f.tick()
		} // No provider call while held.
	}
	j := controlHistory(t, f)
	if len(j.Records) != len(commands)+1 {
		t.Fatal(j)
	}
	for i, r := range j.Records {
		if i == 2 {
			if r.Kind != "tick" || r.ControlChange != nil || !r.ControlBefore.Paused {
				t.Fatal(r)
			}
			continue
		}
		cmdIndex := i
		if i > 2 {
			cmdIndex--
		}
		if r.Kind != "control" || r.ControlChange == nil || r.ControlChange.Operation != commands[cmdIndex] || r.Outcome != "control_saved" || r.CheckStartedAt.Before(start) || r.ControlChange.SaveObservedAt.Before(r.CheckStartedAt) || r.ControlChange.SaveObservedAt.After(time.Now()) || r.Observation.Availability != "not_observed" || r.CountersAvailable || len(r.Decisions.Evaluations) != 0 {
			t.Fatal(r)
		}
	}
	pause, resume, noop := j.Records[1], j.Records[7], j.Records[9]
	if pause.ControlBefore.Paused || !pause.ControlAfter.Paused {
		t.Fatal(pause)
	}
	if !resume.ControlBefore.Paused || !resume.ControlBefore.Stop || !resume.ControlBefore.Completed || !resume.ControlBefore.AwaitingUser || resume.ControlAfter.Enabled || resume.ControlAfter.Paused || resume.ControlAfter.Stop || resume.ControlAfter.Completed || resume.ControlAfter.AwaitingUser {
		t.Fatal(resume)
	}
	if *noop.ControlBefore != *noop.ControlAfter || noop.ControlChange.OwnershipChanged {
		t.Fatal(noop)
	}
	claim := j.Records[0].ControlChange
	if claim.OwnerPresentBefore || !claim.OwnerPresentAfter || !claim.OwnershipChanged {
		t.Fatal(claim)
	}
	b, _ := os.ReadFile(filepath.Join(f.dir, "state", incident.FileName))
	for _, secret := range []string{"PRIVATE_CONTROL_NOTE", "thr_private_fixture_owner", f.dir, "Continue the fictional loop"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("private control content leaked")
		}
	}
	if f.read("calls") != "" || f.read("probes") != "" {
		t.Fatal("controls queried providers")
	}
}

func TestControlHistoryFailurePreservesHoldsAndReports(t *testing.T) {
	for _, kind := range []string{"locked", "corrupt"} {
		for _, cmd := range []string{"pause", "stop", "complete", "await-user"} {
			t.Run(kind+"/"+cmd, func(t *testing.T) {
				f := newFixture(t)
				subscribeCompletion(t, f)
				dir := filepath.Join(f.dir, "state")
				var release func()
				if kind == "locked" {
					l, err := lock.AcquireNamed(dir, "incident")
					if err != nil {
						t.Fatal(err)
					}
					release = func() { l.Release() }
					defer release()
				} else {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					write(t, filepath.Join(dir, incident.FileName), "PRIVATE_BROKEN_HISTORY", 0600)
					release = func() { os.Remove(filepath.Join(dir, incident.FileName)) }
				}
				code, _, errs := f.run(cmd)
				if code != exitFailed || !strings.Contains(errs, errControlHistory.Error()) || strings.Contains(errs, "PRIVATE_BROKEN_HISTORY") {
					t.Fatal(code, errs)
				}
				c := ownerControl(t, f)
				if cmd == "pause" && !c.Paused || cmd == "stop" && !c.Stop || cmd == "complete" && !c.Completed || cmd == "await-user" && !c.AwaitingUser {
					t.Fatal(c)
				}
				want := ""
				if cmd == "complete" {
					want = "keepgoing.complete"
				}
				if cmd == "await-user" {
					want = "keepgoing.await_user"
				}
				if f.hooks.types() != want {
					t.Fatal("diagnostic failure changed reports", f.hooks.types())
				}
				if kind == "corrupt" {
					b, _ := os.ReadFile(filepath.Join(dir, incident.FileName))
					if string(b) != "PRIVATE_BROKEN_HISTORY" {
						t.Fatal("corrupt history overwritten")
					}
				}
				release()
				if cmd == "complete" {
					if code, _, errs := f.run("complete"); code != 0 {
						t.Fatal(errs)
					}
					if f.hooks.types() != want {
						t.Fatal("completion replayed")
					}
					if r := controlHistory(t, f).Records[0]; !r.ControlBefore.Completed || !r.ControlAfter.Completed {
						t.Fatal("backfilled failed record", r)
					}
				}
			})
		}
	}
}

func TestControlHistoryRejectsUncommittedChange(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.dir, "state")
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	before, _ := os.ReadFile(filepath.Join(dir, incident.FileName))
	_, err := updateControlWithHistory(dir, "pause", func(c *control.File) error { c.Paused = true; return errors.New("rejected") })
	if err == nil || ownerControl(t, f).Paused {
		t.Fatal("rejected mutation persisted")
	}
	if err := os.Remove(filepath.Join(dir, control.FileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, control.FileName), 0700); err != nil {
		t.Fatal(err)
	}
	// With control.json unreadable, a pause is kept in the hold marker
	// instead, and no control change is recorded in the history.
	if code, _, errs := f.run("pause"); code != 0 || !strings.Contains(errs, control.HoldFileName) {
		t.Fatal(code, errs)
	}
	after, _ := os.ReadFile(filepath.Join(dir, incident.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("invented successful control save")
	}
}

func TestStartHistoryFailureKeepsSavedControlButDoesNotStart(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Save(cfg.Loop.StateDir, control.File{Paused: true}); err != nil {
		t.Fatal(err)
	}
	l, err := lock.AcquireNamed(cfg.Loop.StateDir, "incident")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	var errs bytes.Buffer
	code := startForeground(env{context.Background(), io.Discard, &errs}, cfg, f.cfg, func(control.File) error { t.Error("acknowledged startup"); return nil })
	if code != exitFailed || !strings.Contains(errs.String(), "control saved") || !strings.Contains(errs.String(), "daemon was not started") {
		t.Fatal(code, &errs)
	}
	c := ownerControl(t, f)
	if !c.Enabled || !c.Paused || f.read("calls") != "" || f.read("probes") != "" {
		t.Fatal(c)
	}
}

func TestDaemonTakeoverHistoryAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		f := newFixture(t)
		dir := filepath.Join(f.dir, "state")
		if err := control.Save(dir, control.File{Paused: true, Ownership: &control.Ownership{ThreadID: "thr_private_old", Generation: 1}}); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BB_THREAD_ID", "thr_private_new")
		if fail {
			l, err := lock.AcquireNamed(dir, "incident")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Release()
		}
		_, err := bindDaemonOwner(dir, true)
		if fail != errors.Is(err, errControlHistory) || (!fail && err != nil) {
			t.Fatal(err)
		}
		c := ownerControl(t, f)
		if !c.Paused || c.Ownership.ThreadID != "thr_private_new" || c.Ownership.Generation != 2 {
			t.Fatal(c)
		}
		if !fail {
			r := controlHistory(t, f).Records[0]
			if r.ControlChange.Operation != "daemon_takeover" || !r.ControlChange.OwnershipChanged || !r.ControlChange.OwnerPresentBefore || !r.ControlChange.OwnerPresentAfter || *r.ControlBefore != *r.ControlAfter {
				t.Fatal(r)
			}
			b, _ := os.ReadFile(filepath.Join(dir, incident.FileName))
			if bytes.Contains(b, []byte("thr_private")) {
				t.Fatal("owner ID leaked")
			}
		}
	}
}

func TestControlHistorySaveFailureDoesNotRecordSuccess(t *testing.T) {
	dir := t.TempDir()
	if err := control.Save(dir, control.File{}); err != nil {
		t.Fatal(err)
	}
	_, err := updateControlWithHistory(dir, "pause", func(c *control.File) error {
		c.Paused = true
		// A non-cooperating writer makes the atomic replacement fail after load.
		if err := os.Remove(filepath.Join(dir, control.FileName)); err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(dir, control.FileName), 0700)
	})
	if err == nil || errors.Is(err, errControlHistory) {
		t.Fatal("misclassified failed save", err)
	}
	if _, err := os.Stat(filepath.Join(dir, incident.FileName)); !os.IsNotExist(err) {
		t.Fatal("recorded failed save")
	}
}
