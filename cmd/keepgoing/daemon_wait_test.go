package main

import (
	"context"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestDaemonWaitHandlesPollResultsBeforeOrdinaryInterval(t *testing.T) {
	for _, mode := range []string{"notification", "later notification", "control change", "cancelled notification"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{Loop: config.Loop{StateDir: t.TempDir()}}
			cfg.Loop.Daemon.Interval.Duration = time.Hour
			cfg.Loop.Daemon.ControlPoll.Duration = time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			calls := 0
			poll := func(context.Context) bool {
				calls++
				switch mode {
				case "later notification":
					return calls == 2
				case "control change":
					if _, err := control.Update(cfg.Loop.StateDir, func(f *control.File) { f.Stop = true }); err != nil {
						t.Fatal(err)
					}
					return false
				case "cancelled notification":
					cancel()
				}
				return true
			}
			got := waitForNextWithPoll(ctx, cfg, controlSignature(cfg.Loop.StateDir), nil, poll)
			want, wantCalls := mode != "cancelled notification", 1
			if mode == "later notification" {
				wantCalls = 2
			}
			if got != want || calls != wantCalls {
				t.Fatalf("next=%t polls=%d; want next=%t polls=%d", got, calls, want, wantCalls)
			}
			if mode != "cancelled notification" && ctx.Err() != nil {
				t.Fatalf("wait reached test deadline: %v", ctx.Err())
			}
		})
	}
}
