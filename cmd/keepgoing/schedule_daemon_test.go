package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// onePass runs a daemon pass's phases on the server: it begins, then ends.
func onePass(t *testing.T, s *daemoninfo.Server) {
	t.Helper()
	s.Update("checking", nil)
	time.Sleep(20 * time.Millisecond)
	next := time.Now().Add(time.Minute)
	s.Update("waiting", &next)
}

// The daemon says when its latest completed pass began and ended, status
// reads both, and the overdue rule takes a pass that began after the last
// check finished, and recorded nothing, for checks running but not recorded.
// A check that finished after the pass began was that pass's own: slept.
func TestDaemonStatusSaysWhenItsLastPassBegan(t *testing.T) {
	dir := t.TempDir()
	s, err := daemoninfo.Start(dir, version, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	started := time.Now()
	onePass(t, s)
	d := inspectDaemon(context.Background(), dir, version, false)
	if d.State != "responsive" || d.LastPassStartedAt == nil || d.LastPassAt == nil || d.LastPassStartedAt.Before(started) || !d.LastPassAt.After(*d.LastPassStartedAt) {
		t.Fatalf("%+v", d)
	}
	loop := config.Loop{StateDir: dir}
	ctl := control.File{Enabled: true}
	gaps := []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	// The pass that recorded the last check, its reports running on after
	// the save for longer than any slack: the computer slept since.
	own := state.File{CheckedAt: *d.LastPassStartedAt, CheckFinishedAt: d.LastPassStartedAt.Add(time.Millisecond), ScheduleGaps: gaps}
	if sch := describeSchedule(&loop, own, ctl, &d, time.Now().Add(4*time.Hour)); sch.NotRecorded || !sch.Slept {
		t.Fatalf("its own pass: %+v", sch)
	}
	// A pass that began after the last check finished recorded nothing,
	// from a daemon started since that check too (after an upgrade, say).
	earlier := state.File{CheckedAt: started.Add(-time.Hour), CheckFinishedAt: started.Add(-time.Hour), ScheduleGaps: gaps}
	if sch := describeSchedule(&loop, earlier, ctl, &d, time.Now().Add(4*time.Hour)); !sch.NotRecorded || sch.Slept || !sch.Overdue {
		t.Fatalf("a later pass: %+v", sch)
	}
}

// The daemon hands each pass what it knows of itself, and the pass's check
// gets it: when the daemon started and when its last completed pass began
// and ended.
func TestDaemonHandsItsPassesToItsChecks(t *testing.T) {
	dir := t.TempDir()
	s, err := daemoninfo.Start(dir, version, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	onePass(t, s)
	s.Update("checking", nil) // the next pass begins
	e := env{ctx: daemonPassContext(context.Background(), s), stdout: io.Discard, stderr: io.Discard}
	opts := tickOptions(e, &config.Config{Loop: config.Loop{StateDir: dir}}, false)
	if opts.Daemon.StartedAt.IsZero() || opts.Daemon.LastPassStartedAt.IsZero() || !opts.Daemon.LastPassStartedAt.Equal(s.LastPassStartedAt()) || !opts.Daemon.LastPassAt.Equal(s.LastPassAt()) {
		t.Fatalf("%+v", opts.Daemon)
	}
	if outside := tickOptions(env{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard}, &config.Config{Loop: config.Loop{StateDir: dir}}, false); !outside.Daemon.StartedAt.IsZero() {
		t.Fatalf("outside a daemon: %+v", outside.Daemon)
	}
}
