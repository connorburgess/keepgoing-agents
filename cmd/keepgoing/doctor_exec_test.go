//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorExecUsesAccountEnvironmentAndDiscardsStderr(t *testing.T) {
	p := filepath.Join(t.TempDir(), "version-cli")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nprintf '%s' \"$EXAMPLE_ACCOUNT\"\nprintf 'private stderr' >&2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXAMPLE_ACCOUNT", "inherited")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := doctorExec(ctx, p, []string{"--version"}, map[string]string{"EXAMPLE_ACCOUNT": "selected"})
	if err != nil || string(out) != "selected" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestDoctorExecBoundsOutputAndTime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "version-cli")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nhead -c 70000 /dev/zero\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	out, err := doctorExec(ctx, p, []string{"--version"}, nil)
	cancel()
	if err == nil || len(out) != 0 || !strings.Contains(err.Error(), "output limit") {
		t.Fatalf("unbounded output: %d %v", len(out), err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec sleep 20\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = doctorExec(ctx, p, []string{"--version"}, nil)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("version command did not time out")
	}
}
