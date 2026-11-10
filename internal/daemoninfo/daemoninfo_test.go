package daemoninfo

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLiveSnapshotAndStaleEndpoint(t *testing.T) {
	dir := t.TempDir()
	s, err := Start(dir, "example-build", true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := filepath.Join(dir, FileName)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	copied := t.TempDir()
	if err := os.WriteFile(filepath.Join(copied, FileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Query(context.Background(), copied); err == nil {
		t.Fatal("accepted another loop's endpoint")
	}
	if info, _ := os.Stat(p); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal("endpoint is not private")
	}
	next := time.Now().Add(time.Minute).UTC()
	s.Update("waiting", &next)
	got, err := Query(context.Background(), dir)
	if err != nil || got.Version != "example-build" || got.Phase != "waiting" || got.NextCheckAt == nil || !got.NextCheckAt.Equal(next) || !got.DryRun {
		t.Fatalf("%+v %v", got, err)
	}
	s.Update("checking", nil)
	got, err = Query(context.Background(), dir)
	if err != nil || got.Phase != "checking" || got.NextCheckAt != nil {
		t.Fatalf("%+v %v", got, err)
	}
	s.Close()
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("endpoint not removed")
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Query(context.Background(), dir); err == nil {
		t.Fatal("stale endpoint counted as live")
	}
}

func TestWrongKeyDoesNotVerifyAndClosePreservesReplacement(t *testing.T) {
	dir := t.TempDir()
	s, err := Start(dir, "example-build", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ep := s.endpoint
	ep.Key = strings.Repeat("01", 32)
	b, _ := json.Marshal(ep)
	if err := os.WriteFile(s.path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Query(context.Background(), dir); err == nil {
		t.Fatal("wrong responder verified")
	}
	s.Close()
	after, err := os.ReadFile(s.path)
	if err != nil || string(after) != string(b) {
		t.Fatal("replacement record removed")
	}
}

func TestQueryRejectsUnsafeOrMissingRecordsWithoutCreatingState(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Query(context.Background(), missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created state")
	}
	for _, content := range []string{
		"private invalid data", strings.Repeat("x", 2048),
		`{"address":"example.com:80","key":"` + strings.Repeat("00", 32) + `"}`,
		`{"address":"127.0.0.1:0","key":"` + strings.Repeat("00", 32) + `"}`,
		`{"address":"127.0.0.1:1234","key":"private key"}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Query(context.Background(), dir); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("%v", err)
		}
	}
}

func TestQueryDeadlineAndCancellation(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	dir := t.TempDir()
	b, _ := json.Marshal(endpoint{l.Addr().String(), strings.Repeat("00", 32)})
	if err := os.WriteFile(filepath.Join(dir, FileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Query(ctx, dir); done <- err }()
	var c net.Conn
	select {
	case c = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("connection was not accepted")
	}
	defer c.Close()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("query did not cancel")
	}
}

func TestServerBoundsIdleConnections(t *testing.T) {
	s, err := Start(t.TempDir(), "test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := net.Dial("tcp4", s.endpoint.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	s.Close()
	if time.Since(start) > time.Second {
		t.Fatal("idle connection delayed shutdown")
	}
}

func TestOptionalProcessIdentityIsAuthenticatedAndRedacted(t *testing.T) {
	id := "1234567890abcdef1234567890abcdef"
	t.Setenv("INVOCATION_ID", id)
	dir := t.TempDir()
	s, err := Start(dir, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := Query(context.Background(), dir)
	if err != nil || got.ProcessID != os.Getpid() {
		t.Fatalf("process metadata unavailable: %v", err)
	}
	want := ""
	if runtime.GOOS == "linux" {
		want = id
	}
	if got.InvocationID != want {
		t.Fatal("unexpected invocation metadata")
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(b), id) || strings.Contains(string(b), "processID") {
		t.Fatal("identity persisted in endpoint")
	}
	// A newer client still accepts responsiveness from an older responder, and
	// invalid optional values cannot verify a service identity.
	for _, bad := range []string{"", "PRIVATE", strings.Repeat("0", 32)} {
		s.mu.Lock()
		s.snapshot.ProcessID = 0
		s.snapshot.InvocationID = bad
		s.mu.Unlock()
		got, err = Query(context.Background(), dir)
		if err != nil || got.ProcessID != 0 || got.InvocationID != "" {
			t.Fatalf("optional metadata not cleared: %v", err)
		}
	}
	for _, bad := range []string{"PRIVATE", strings.ToUpper(id), strings.Repeat("0", 32), id + "0"} {
		if ValidInvocationID(bad) {
			t.Fatal("invalid invocation accepted")
		}
		t.Setenv("INVOCATION_ID", bad)
		if localInvocationID() != "" {
			t.Fatal("raw environment retained")
		}
	}
}
