//go:build unix

package main

import (
	"bytes"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// execHolder starts a child that holds an flock on path until killed.
func execHolder(t *testing.T, path, ready string) *exec.Cmd {
	t.Helper()
	os.MkdirAll(dirOf(path), 0o700)
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldLock")
	cmd.Env = append(os.Environ(), "HOLD_LOCK="+path, "HOLD_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(ready); return err == nil }, "lock holder")
	return cmd
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

func loadFixture(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
