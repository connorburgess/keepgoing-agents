//go:build unix

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func TestDaemonChecksChildCompletionBeforeInterval(t *testing.T) {
	for _, status := range []string{"completed", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			f.run("enable")
			// The normal tick establishes the cursor while child work prevents a wake.
			write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child"}]`, 0600)
			write(t, filepath.Join(f.bbDir, "thr_child.show"), `{"thread":{"id":"thr_child","parentThreadId":"thr_primary","status":"active"}}`, 0600)
			if r := f.tick(); r["action"] != "none" {
				t.Fatalf("%v", r)
			}
			write(t, filepath.Join(f.bbDir, "thr_child.show"), `{"thread":{"id":"thr_child","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
			write(t, filepath.Join(f.bbDir, "thr_primary.log"), fmt.Sprintf(`[{"seq":1,"type":"client/turn/requested","data":{"initiator":"system","systemMessageKind":"child-%s","systemMessageSubject":{"kind":"thread","threadId":"thr_child","outcomes":[{"threadId":"thr_child","status":%q}]}}}]`, status, status), 0600)
			cfg := loadFixture(t, f.cfg)
			cfg.Loop.Daemon.ControlPoll.Duration = 500 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			start := time.Now()
			if !waitForNext(ctx, cfg, controlSignature(cfg.Loop.StateDir), nil) {
				t.Fatal("missed early check; ordinary interval is 30 seconds")
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("completion waited too long")
			}
			if r := f.tick(); r["action"] != "wake" {
				t.Fatalf("%v", r)
			}
			if got := strings.Count(f.read("calls"), "thread tell thr_primary "); got != 1 {
				t.Fatalf("parent prompts=%d", got)
			}
			// A fresh waiter and fresh adapters use the persisted cursor, even with the
			// fixture parent left idle. No extra check is caused by the same event.
			again, cancelAgain := context.WithTimeout(context.Background(), 1100*time.Millisecond)
			defer cancelAgain()
			if waitForNext(again, cfg, controlSignature(cfg.Loop.StateDir), nil) {
				t.Fatal("notification replayed")
			}
			if got := strings.Count(f.read("calls"), "thread tell thr_primary "); got != 1 {
				t.Fatalf("duplicate prompt count=%d", got)
			}
			if strings.Contains(f.read("calls"), "thread tell thr_child") {
				t.Fatal("child mutated")
			}
		})
	}
}

func TestDaemonNotificationPollHonorsHoldsAndTickLock(t *testing.T) {
	for _, hold := range []string{"disabled", "pause", "stop", "complete", "await-user", "lock"} {
		t.Run(hold, func(t *testing.T) {
			f := newFixture(t)
			if hold != "disabled" {
				f.run("enable")
			}
			if hold != "disabled" && hold != "lock" {
				f.run(hold)
			}
			cfg := loadFixture(t, f.cfg)
			if hold == "lock" {
				l, err := lock.Acquire(cfg.Loop.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Release()
			}
			before := f.read("calls")
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			if waitForNext(ctx, cfg, controlSignature(cfg.Loop.StateDir), nil) {
				t.Fatal("held loop woke")
			}
			if f.read("calls") != before {
				t.Fatal("held loop queried provider")
			}
		})
	}
}

func TestDaemonSlowNotificationPollIsBounded(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.dir, "bin", "bb"), "#!/bin/sh\nexec sleep 5\n", 0755)
	cfg := loadFixture(t, f.cfg)
	start := time.Now()
	if pollWake(context.Background(), cfg) {
		t.Fatal("timed out poll authorized check")
	}
	if time.Since(start) > time.Second {
		t.Fatal("poll blocked control handling")
	}
	// Lock must be released after cancellation.
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if _, err := os.Stat(filepath.Join(cfg.Loop.StateDir, "targets", "primary", "bb-cursor.json")); !os.IsNotExist(err) {
		t.Fatal("failed scan wrote cursor")
	}
}
