//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestServiceBinaryDiskInspection(t *testing.T) {
	dir := t.TempDir()
	exe, copyPath := filepath.Join(dir, "recorded"), filepath.Join(dir, "current")
	marker := filepath.Join(dir, "must-not-run")
	contents := "#!/bin/sh\ntouch '" + marker + "'\n# PRIVATE_BINARY_CONTENT\n"
	write(t, exe, contents, 0700)
	write(t, copyPath, contents, 0700)
	ctx := context.Background()
	check := func(path, cli, state, comparison string) {
		t.Helper()
		r := inspectServiceBinary(ctx, path, cli)
		if r.State != state || r.Comparison != comparison {
			t.Fatalf("%+v", r)
		}
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), dir) || strings.Contains(string(b), "PRIVATE") {
			t.Fatal("private detail leaked", string(b))
		}
	}
	check(exe, copyPath, "available", "match")
	write(t, copyPath, "different fictional file", 0700)
	check(exe, copyPath, "available", "different")
	check(exe, filepath.Join(dir, "missing-cli"), "available", "unknown")
	check(filepath.Join(dir, "missing"), copyPath, "missing", "unknown")
	check(dir, copyPath, "non_regular", "unknown")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	check(link, exe, "available", "match")
	if err := os.Chmod(exe, 0600); err != nil {
		t.Fatal(err)
	}
	check(link, exe, "no_execute_bits", "unknown")
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	check(link, copyPath, "missing", "unknown")
	large, err := os.OpenFile(exe, os.O_CREATE|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(serviceBinaryLimit + 1); err != nil {
		t.Fatal(err)
	}
	large.Close()
	check(exe, copyPath, "unavailable", "unknown")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if r := inspectServiceBinary(cancelled, copyPath, copyPath); r.State != "unavailable" {
		t.Fatal(r)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("recorded executable ran", err)
	}
}

func TestServiceBinaryRejectsFIFOAndChangedIdentity(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0700); err != nil {
		t.Fatal(err)
	}
	if r := inspectServiceBinary(context.Background(), fifo, fifo); r.State != "non_regular" {
		t.Fatal(r)
	}
	// Exercise the nonblocking open used if a regular path is replaced by a FIFO
	// between stat and open. No writer exists, so a blocking open would hang.
	f, err := openServiceBinary(fifo)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "binary")
	write(t, path, "original", 0700)
	before, _ := os.Stat(path)
	if sameServiceBinary(before, fi) {
		t.Fatal("accepted FIFO")
	}
	write(t, filepath.Join(dir, "replacement"), "original", 0700)
	if err := os.Rename(filepath.Join(dir, "replacement"), path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if sameServiceBinary(before, after) {
		t.Fatal("accepted replacement")
	}
	before = after
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	after, _ = os.Stat(path)
	if sameServiceBinary(before, after) {
		t.Fatal("accepted changed mode")
	}
	before = after
	write(t, path, "longer changed contents", 0600)
	after, _ = os.Stat(path)
	if sameServiceBinary(before, after) {
		t.Fatal("accepted changed size")
	}
}

func TestServiceBinaryStatusPreservesHoldsAndFiles(t *testing.T) {
	for _, backend := range []string{"launchd", "systemd"} {
		t.Run(backend, func(t *testing.T) {
			f := newServiceFixture(t)
			if backend == "systemd" {
				f, _ = newSystemdFixture(t)
			}
			t.Setenv("BB_THREAD_ID", "thr_binary_fixture")
			ctl := control.File{Enabled: true, Paused: true, Stop: true, Completed: true, AwaitingUser: true, Ownership: &control.Ownership{ThreadID: "thr_binary_fixture", Generation: 2}}
			if err := control.Save(f.cfg.Loop.StateDir, ctl); err != nil {
				t.Fatal(err)
			}
			f.installed(t)
			p := f.plan(t)
			paths := []string{filepath.Join(p.StateDir, control.FileName), filepath.Join(p.StateDir, f.host.recordName()), p.Definition, p.Executable}
			before := make([][]byte, len(paths))
			for i, path := range paths {
				before[i], _ = os.ReadFile(path)
			}
			// Simulate a CLI invoked from a different installation.
			f.host.executable = filepath.Join(f.f.dir, "another-cli")
			write(t, f.host.executable, "fictional second installation", 0700)
			c, o, e := f.call(t, "status", "--json")
			var r serviceReport
			if c != 0 || json.Unmarshal([]byte(o), &r) != nil || r.Executable == nil || r.Executable.Comparison != "different" || !strings.Contains(o, "executable_differs") {
				t.Fatalf("%d %s %s", c, o, e)
			}
			if c, o, e := f.call(t, "status"); c != 0 || !strings.Contains(o, "on-disk CLI comparison: different") {
				t.Fatalf("%d %s %s", c, o, e)
			}
			for i, path := range paths {
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before[i], after) {
					t.Fatal("file changed", path)
				}
			}
			if f.f.read("calls") != "" || f.f.read("probes") != "" {
				t.Fatal("provider called")
			}
			if err := os.Remove(p.Executable); err != nil {
				t.Fatal(err)
			}
			c, o, e = f.call(t, "status", "--json")
			if c != 0 || json.Unmarshal([]byte(o), &r) != nil || r.Executable.State != "missing" || !strings.Contains(o, "executable_missing") {
				t.Fatalf("%d %s %s", c, o, e)
			}
			if c, o, e := f.call(t, "remove"); c != 0 {
				t.Fatalf("missing executable blocked removal: %d %s %s", c, o, e)
			}
		})
	}
}

func TestServiceBinaryCanReadNativeExecutable(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := inspectServiceBinary(context.Background(), path, path)
	if r.State != "available" || r.Comparison != "match" {
		t.Fatalf("%+v", r)
	}
}

func TestServiceBinaryUnreadableIsUnavailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can bypass read permissions")
	}
	path := filepath.Join(t.TempDir(), "unreadable")
	write(t, path, "PRIVATE_CONTENT", 0100)
	if r := inspectServiceBinary(context.Background(), path, path); r.State != "unavailable" {
		t.Fatalf("%+v", r)
	}
}
