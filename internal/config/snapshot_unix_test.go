//go:build unix

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSourceReplacementWithFIFOIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keepgoing.yaml")
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// Opening a FIFO for reading would block waiting for a writer. The
	// source guard must reject a non-regular replacement without opening it.
	if cfg.CheckSources() == nil {
		t.Fatal("FIFO accepted")
	}
}
