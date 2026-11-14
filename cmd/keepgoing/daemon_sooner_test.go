package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// soonerLoop is a loop whose daemon waits an hour between passes and polls
// its controls every few milliseconds, with its connectivity check replaced.
func soonerLoop(t *testing.T, online func(context.Context) bool) *config.Config {
	t.Helper()
	cfg := &config.Config{Loop: config.Loop{StateDir: t.TempDir()}}
	cfg.Loop.Daemon.Interval.Duration = time.Hour
	cfg.Loop.Daemon.ControlPoll.Duration = 5 * time.Millisecond
	oldEvery, oldCheck := networkRecheck, connectivity
	networkRecheck = 20 * time.Millisecond
	connectivity = func(*config.Config) func(context.Context) bool { return online }
	t.Cleanup(func() { networkRecheck, connectivity = oldEvery, oldCheck })
	return cfg
}

func noWake(context.Context) bool { return false }

// wait runs the daemon's wait and says how long it took to end, failing the
// test if it ran to its two-second limit.
func wait(t *testing.T, cfg *config.Config) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if !waitForNextWithPoll(ctx, cfg, controlSignature(cfg.Loop.StateDir), nil, noWake) || ctx.Err() != nil {
		t.Fatalf("the wait did not end early: %v", ctx.Err())
	}
	return time.Since(start)
}

// While the last check found the network down, the daemon's wait checks
// connectivity, no more often than its bound, and the next pass starts as
// soon as it passes, not at the end of the interval.
func TestDaemonChecksAsSoonAsTheNetworkIsBack(t *testing.T) {
	var calls atomic.Int32
	back := time.Now().Add(150 * time.Millisecond)
	cfg := soonerLoop(t, func(context.Context) bool { calls.Add(1); return time.Now().After(back) })
	if err := state.Save(cfg.Loop.StateDir, state.File{NetworkDownSince: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	took := wait(t, cfg)
	// Down for 150ms, checked every 20ms at most: a handful of checks, the
	// last of them passing, and the pass started within a poll or two.
	if n := calls.Load(); n < 2 || n > 10 || took > time.Second {
		t.Fatalf("%d connectivity checks, %s", n, took)
	}
}

// With the network up at the last check, the wait makes no connectivity
// check at all; while it stays down, no more than one per networkRecheck.
func TestDaemonChecksConnectivityOnlyWhileDown(t *testing.T) {
	var calls atomic.Int32
	cfg := soonerLoop(t, func(context.Context) bool { calls.Add(1); return false })
	cfg.Loop.Daemon.Interval.Duration = 300 * time.Millisecond
	wait(t, cfg) // the network was not down: the interval ends the wait
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d connectivity checks with the network up", n)
	}
	if err := state.Save(cfg.Loop.StateDir, state.File{NetworkDownSince: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	wait(t, cfg) // still down: the interval ends it
	if n := calls.Load(); n < 5 || n > 16 {
		t.Fatalf("%d connectivity checks in 300ms at most one per 20ms", n)
	}
}

// A connectivity check is made under the tick lock: while a check holds it,
// none is made, and none passes.
func TestDaemonConnectivityCheckWaitsForTheTickLock(t *testing.T) {
	var calls atomic.Int32
	cfg := soonerLoop(t, func(context.Context) bool { calls.Add(1); return true })
	cfg.Loop.Daemon.Interval.Duration = 200 * time.Millisecond
	if err := state.Save(cfg.Loop.StateDir, state.File{NetworkDownSince: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	took := wait(t, cfg)
	l.Release()
	if n := calls.Load(); n != 0 || took < 180*time.Millisecond {
		t.Fatalf("%d checks made beside a held lock; the wait ended after %s", n, took)
	}
}

// A provider limit the last check waited on, with a structured reset inside
// the interval, ends the wait at that reset. One beyond the interval, or in
// the past, changes nothing.
func TestDaemonChecksAtAKnownLimitReset(t *testing.T) {
	cfg := soonerLoop(t, nil)
	if err := state.Save(cfg.Loop.StateDir, state.File{LimitResetsAt: time.Now().Add(100 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	if took := wait(t, cfg); took < 90*time.Millisecond || took > time.Second {
		t.Fatalf("the wait ended after %s, the reset was at 100ms", took)
	}
	now := time.Now()
	for _, reset := range []time.Time{now.Add(2 * time.Hour), now.Add(-time.Minute)} {
		if err := state.Save(cfg.Loop.StateDir, state.File{LimitResetsAt: reset}); err != nil {
			t.Fatal(err)
		}
		if p := planWait(cfg, now); !p.deadline.Equal(now.Add(time.Hour)) {
			t.Fatalf("reset %s moved the deadline to %s", reset.Sub(now), p.deadline.Sub(now))
		}
	}
}
