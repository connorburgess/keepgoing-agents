//go:build unix

package bb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/testexec"
)

// A self-signalled command counts as a Unix signal exit, without claiming a
// source or cause. Adapter timeout/cancellation and ordinary exit do not.
func TestSignalExitOfABBCommandIsCounted(t *testing.T) {
	dir := t.TempDir()
	script := func(name, body string) *Adapter {
		path := filepath.Join(dir, name)
		testexec.Script(t, path, "#!/bin/sh\n"+body+"\n")
		return New(config.BB{Thread: "thr_example123", CLI: path})
	}
	killed := script("killed", "kill -9 $$")
	if _, err := killed.exec(context.Background(), "", "thread", "show", "thr_example123", "--json"); err == nil || killed.SignalExits() != 1 {
		t.Fatalf("%v, %d signal exits", err, killed.SignalExits())
	}
	var _ adapter.SignalExitReporter = killed

	failed := script("failed", "exit 3")
	if _, err := failed.exec(context.Background(), "", "thread", "show"); err == nil || failed.SignalExits() != 0 {
		t.Fatalf("%v, %d signal exits", err, failed.SignalExits())
	}
	slow := script("slow", "sleep 30")
	slow.Timeout = 200 * time.Millisecond
	if _, err := slow.exec(context.Background(), "", "thread", "show"); !errors.Is(err, errStopped) || slow.SignalExits() != 0 {
		t.Fatalf("a command stopped at the adapter's own limit was counted as a signal exit: %v, %d", err, slow.SignalExits())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := killed.exec(ctx, "", "thread", "show"); !errors.Is(err, errStopped) || killed.SignalExits() != 1 {
		t.Fatalf("cancelled execution changed the signal count: %v, %d", err, killed.SignalExits())
	}

}
