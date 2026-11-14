package conformance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCrashStageWaitsForExactQuitConsumption(t *testing.T) {
	s := subject(t, "claude")
	r := s.Rig.(turnRig).rig
	gate := filepath.Join(r.dir, "consume-gate")
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(gate) })
	// A receipt from a different generation/agent must not permit clearing quit.
	if err := os.WriteFile(filepath.Join(r.dir, "quit-ack"), []byte("old-stage 999999"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := s.New()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Stage(context.Background(), a, Crashed) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, err := os.Stat(filepath.Join(r.dir, "quit-blocked")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stand-in did not reach delayed consumption boundary")
		}
	}
	select {
	case err := <-done:
		t.Fatalf("crash stage returned before matching consumption receipt: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(r.dir, "quit")); err != nil {
		t.Fatalf("pending signal disappeared: %v", err)
	}
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("matching consumption did not release staging")
	}
	// Immediate reuse is now safe: the old agent consumed its generation's quit.
	if err := r.Stage(context.Background(), a, ExitNonZero); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredCrashReceiptCannotClearOrReuseQuit(t *testing.T) {
	r := &rig{dir: t.TempDir(), pendingQuit: "stage-1 123", pendingDeadline: time.Now().Add(-time.Second)}
	quit := filepath.Join(r.dir, "quit")
	if err := os.WriteFile(quit, []byte(r.pendingQuit), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "quit-ack"), []byte("stage-1 456"), 0600); err != nil {
		t.Fatal(err)
	}
	// The expired staging deadline is retained; a later Stage cannot buy a new
	// timeout or clear the outstanding signal based on another agent's receipt.
	if err := r.Stage(context.Background(), nil, Idle); err == nil {
		t.Fatal("mismatched receipt permitted reuse")
	}
	b, err := os.ReadFile(quit)
	if err != nil || string(b) != r.pendingQuit {
		t.Fatalf("pending quit changed: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "situation")); !os.IsNotExist(err) {
		t.Fatalf("later stage began: %v", err)
	}
}
