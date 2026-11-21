package daemoninfo

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestShutdownInvocationAndLegacy(t *testing.T) {
	dir := t.TempDir()
	s, err := Start(dir, "fixture-build", true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err := Query(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if Shutdown(context.Background(), dir, old, "binding", "thr_owner") == nil {
		t.Fatal("legacy must refuse")
	}
	var calls atomic.Int32
	s.EnableShutdown(func(r ShutdownRequest) error {
		calls.Add(1)
		if r.Binding != "binding" || r.Caller != "thr_owner" {
			return errors.New("binding")
		}
		return nil
	})
	snap, err := Query(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"start", "build", "state", "invocation"} {
		wrong := snap
		switch change {
		case "invocation":
			wrong.ShutdownID = strings.Repeat("b", 64)
		case "start":
			wrong.StartedAt = wrong.StartedAt.Add(-time.Second)
		case "build":
			wrong.Version = "older"
		case "state":
			wrong.StateID = "other"
		}
		if Shutdown(context.Background(), dir, wrong, "binding", "thr_owner") == nil {
			t.Fatal(change)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("mismatch reached canceller")
	}
	copied := t.TempDir()
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	os.WriteFile(filepath.Join(copied, FileName), b, 0600)
	if _, err := Query(context.Background(), copied); err == nil {
		t.Fatal("copied endpoint accepted")
	}
	if Shutdown(context.Background(), copied, snap, "binding", "thr_owner") == nil {
		t.Fatal("copied shutdown accepted")
	}
	if err := Shutdown(context.Background(), dir, snap, "binding", "thr_owner"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestShutdownOldInvocationCannotCancelReplacement(t *testing.T) {
	dir := t.TempDir()
	first, err := Start(dir, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	first.EnableShutdown(func(ShutdownRequest) error { return nil })
	old, err := Query(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	second, err := Start(dir, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var called atomic.Bool
	second.EnableShutdown(func(ShutdownRequest) error { called.Store(true); return nil })
	if Shutdown(context.Background(), dir, old, "binding", "") == nil || called.Load() {
		t.Fatal("old request cancelled replacement")
	}
}

func TestShutdownLostReplyIsUnknown(t *testing.T) {
	dir := t.TempDir()
	s, err := Start(dir, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var accepted atomic.Bool
	s.EnableShutdown(func(ShutdownRequest) error { accepted.Store(true); time.Sleep(250 * time.Millisecond); return nil })
	snap, err := Query(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	err = Shutdown(context.Background(), dir, snap, "binding", "")
	if err == nil || !strings.Contains(err.Error(), "unknown") || !accepted.Load() {
		t.Fatalf("want unknown accepted outcome, got %v", err)
	}
}

func TestShutdownUnsignedRequestNeverReachesValidator(t *testing.T) {
	dir := t.TempDir()
	s, err := Start(dir, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var called atomic.Bool
	s.EnableShutdown(func(ShutdownRequest) error { called.Store(true); return nil })
	c, err := net.Dial("tcp4", s.endpoint.Address)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(c, `{"request":{"nonce":"`+strings.Repeat("a", 64)+`"},"mac":"forged"}`+"\n")
	io.ReadAll(c)
	c.Close()
	if called.Load() {
		t.Fatal("unsigned request reached validator")
	}
}
