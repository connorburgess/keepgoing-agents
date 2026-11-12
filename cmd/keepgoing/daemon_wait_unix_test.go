//go:build unix

package main

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestDaemonWaitSignatureDoesNotOpenFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, control.FileName), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		controlSignature(dir)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("control polling blocked opening a FIFO")
	}
}
