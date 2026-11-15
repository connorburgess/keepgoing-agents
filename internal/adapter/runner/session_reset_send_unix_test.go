//go:build unix

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
)

// Unlike the synthetic generation fixtures, these execute a real detached
// wrapper and a fictional local CLI, and inspect its actual argv.
func resetSendFixture(t *testing.T) (*Headless, string) {
	return exampleRunner(t, `cat >/dev/null; printf '%s\n' "$@" > "$PWD/args"; echo '`+exampleLine("ok", "")+`'`, nil)
}

func TestSessionResetSendRechecksFenceBeforeArgv(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(map[bool]string{true: "first-start", false: "completed"}[first], func(t *testing.T) {
			h, root := resetSendFixture(t)
			if !first {
				if err := h.Send(context.Background(), "fictional first task", adapter.ModeAuto); err != nil {
					t.Fatal(err)
				}
				waitPhase(t, h.dir, Finished)
			}
			beforeSessionCommit = func() {
				beforeSessionCommit = nil
				if _, err := h.ResetSession(); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { beforeSessionCommit = nil })
			if err := h.Send(context.Background(), "fictional second task", adapter.ModeAuto); err != nil {
				t.Fatal(err)
			}
			waitPhase(t, h.dir, Finished)
			args, err := os.ReadFile(filepath.Join(root, "args"))
			if err != nil || strings.Contains(string(args), "--session") {
				t.Fatalf("send undid reset: %q %v", args, err)
			}
		})
	}
}

func TestSessionResetCannotCrossSendArgvAndSetup(t *testing.T) {
	h, _ := resetSendFixture(t)
	if err := h.Send(context.Background(), "fictional first task", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Finished)
	afterSessionCommit = func() {
		// The exact point between building session-derived argv and entering the
		// existing setup used to have no guard at all.
		if had, err := h.ResetSession(); had || !errors.Is(err, ErrTurnRunning) {
			t.Errorf("reset crossed send argv: %v %v", had, err)
		}
	}
	t.Cleanup(func() { afterSessionCommit = nil })
	if err := h.Send(context.Background(), "fictional second task", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	afterSessionCommit = nil
	waitPhase(t, h.dir, Finished)
	if _, err := h.ResetSession(); err != nil {
		t.Fatalf("reset after completed competing send: %v", err)
	}
	if _, err := h.Observe(context.Background()); err != nil || h.dir.Session() != "" {
		t.Fatalf("post-send reset undone: %v %q", err, h.dir.Session())
	}
}

func TestSessionResetFirstStartCompletesBeforeWaitingSend(t *testing.T) {
	h, root := resetSendFixture(t)
	sampled := make(chan struct{})
	sent := make(chan error, 1)
	beforeSessionCommit = func() { close(sampled) }
	afterResetSample = func() {
		go func() { sent <- h.Send(context.Background(), "fictional first task", adapter.ModeAuto) }()
		select {
		case <-sampled:
		case <-time.After(3 * time.Second):
			t.Fatal("first send did not sample NeverRun")
		}
	}
	t.Cleanup(func() { afterResetSample = nil; beforeSessionCommit = nil })
	if _, err := h.ResetSession(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first send deadlocked after reset")
	}
	afterResetSample = nil
	beforeSessionCommit = nil
	waitPhase(t, h.dir, Finished)
	args, err := os.ReadFile(filepath.Join(root, "args"))
	if err != nil || strings.Contains(string(args), "--session") {
		t.Fatalf("first start resumed: %q %v", args, err)
	}
}
