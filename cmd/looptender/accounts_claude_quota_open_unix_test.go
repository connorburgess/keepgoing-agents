//go:build darwin || linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestClaudeQuotaAdmissionRefusesFIFOAndSymlink(t *testing.T) {
	for _, kind := range []string{"fifo", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "selected")
			if kind == "fifo" {
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(dir, "regular")
				if err := os.WriteFile(target, []byte("fictional image"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, cleanup, err := accountClaudeQuotaStage(ctx, path, accountClaudeQuotaDigest)
				if cleanup != nil {
					cleanup()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("admitted special/aliased image")
				}
			case <-time.After(200 * time.Millisecond):
				// Release an old blocking read-open so the negative control never leaks.
				if kind == "fifo" {
					fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK, 0)
					if err == nil {
						unix.Close(fd)
					}
				}
				t.Fatal("file admission exceeded deadline before process launch")
			}
		})
	}
}
