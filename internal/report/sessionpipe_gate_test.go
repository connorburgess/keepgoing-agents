package report

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/looptender/looptender/internal/lock"
)

func TestSessionpipeUnauthorizedPersistsUntilExplicitResume(t *testing.T) {
	var calls, posts atomic.Int32
	var authorized atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == "GET" {
			w.Write([]byte(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"}}`))
			return
		}
		posts.Add(1)
		if !authorized.Load() {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(202)
		w.Write([]byte(pipeAck))
	}))
	defer srv.Close()
	c := pipeConfig(t, srv.URL)
	dir := t.TempDir()
	send := func() error {
		r, err := NewRecorded(c, dir)
		if err != nil {
			t.Fatal(err)
		}
		return r.Report(context.Background(), Event{Type: "await_user"})
	}
	if err := send(); !errors.Is(err, ErrNoRetry) {
		t.Fatalf("401: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "sessionpipe.json"))
	if err != nil {
		t.Fatal(err)
	}
	authorized.Store(true)
	t.Setenv("KG_TEST_SECRET", "replacement-fictional-token")
	for i := 0; i < 3; i++ {
		if err := send(); !errors.Is(err, ErrSessionpipePaused) || !errors.Is(err, ErrNoRetry) {
			t.Fatalf("restart: %v", err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "sessionpipe.json"))
	if string(before) != string(after) || calls.Load() != 2 || posts.Load() != 1 {
		t.Fatalf("paused request or reservation: calls=%d posts=%d", calls.Load(), posts.Load())
	}
	if g, err := LoadSessionpipeGate(dir); err != nil || g.Status != "unauthorized" {
		t.Fatalf("gate %+v %v", g, err)
	}
	if err := ResumeSessionpipe(dir); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("resume sent a request")
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(filepath.Join(dir, "sessionpipe.json"))
	var a, b pipeState
	// The resumed call uses the same opaque identity and the next reserved pair.
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil || a.ID != b.ID || a.Next != 3 || b.Next != 5 {
		t.Fatalf("identity/sequence changed: %s %s", before, after)
	}
	if posts.Load() != 2 {
		t.Fatal("resume replayed a report")
	}
}

func TestSessionpipeInvalidGatePreservedWithoutNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := pipeConfig(t, srv.URL)
	for _, body := range []string{`{}`, `{"version":null,"status":"ready"}`, `{"version":1,"status":null}`, `{"version":1,"status":"ready","extra":1}`, `{"version":1,"status":"unknown"}`, `invalid`, `{"version":1,"status":"unauthorized","status":"ready"}`, `{"version":1,"status":"unauthorized","Status":"ready"}`, `{"version":1,"version":1,"status":"ready"}`, `{"Version":1,"status":"ready"}`} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, SessionpipeGateFile)
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			r, err := NewRecorded(c, dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Report(context.Background(), Event{Type: "await_user"}); err == nil {
				t.Fatal("invalid gate sent")
			}
			if err := ResumeSessionpipe(dir); err == nil {
				t.Fatal("invalid gate reset")
			}
			after, _ := os.ReadFile(path)
			if string(after) != body {
				t.Fatal("evidence changed")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("network before gate validation")
	}
}

func TestSessionpipePendingAndConcurrentDeliveryRefused(t *testing.T) {
	dir := t.TempDir()
	if err := savePipeGate(dir, "pending"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("network while blocked") }))
	defer srv.Close()
	c := pipeConfig(t, srv.URL)
	r, err := NewRecorded(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Report(context.Background(), Event{Type: "await_user"}); !errors.Is(err, ErrSessionpipePaused) {
		t.Fatal(err)
	}
	guard, err := lock.AcquireNamed(dir, SessionpipeLock)
	if err != nil {
		t.Fatal(err)
	}
	if err := ResumeSessionpipe(dir); err == nil {
		t.Fatal("resume raced delivery")
	}
	if err := r.Report(context.Background(), Event{Type: "await_user"}); err == nil {
		t.Fatal("concurrent delivery")
	}
	guard.Release()
	if err := ResumeSessionpipe(dir); err != nil {
		t.Fatal(err)
	}
}

func TestSessionpipeOtherRejectionsDoNotAuthenticationPause(t *testing.T) {
	for _, status := range []int{403, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					w.Write([]byte(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"}}`))
					return
				}
				posts.Add(1)
				w.WriteHeader(status)
			}))
			defer srv.Close()
			c := pipeConfig(t, srv.URL)
			dir := t.TempDir()
			for i := 0; i < 2; i++ {
				r, err := NewRecorded(c, dir)
				if err != nil {
					t.Fatal(err)
				}
				if err = r.Report(context.Background(), Event{Type: "error"}); !errors.Is(err, ErrNoRetry) {
					t.Fatal(err)
				}
			}
			g, err := LoadSessionpipeGate(dir)
			if err != nil || g.Status != "ready" || posts.Load() != 2 {
				t.Fatalf("%+v %v posts=%d", g, err, posts.Load())
			}
		})
	}
}

func TestSessionpipeInFlightGateCannotBeResumed(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write([]byte(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"}}`))
			return
		}
		posts.Add(1)
		close(started)
		<-release
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c := pipeConfig(t, srv.URL)
	dir := t.TempDir()
	first, err := NewRecorded(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRecorded(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- first.Report(context.Background(), Event{Type: "await_user"}) }()
	<-started
	if err := ResumeSessionpipe(dir); err == nil {
		t.Error("resume bypassed in-flight 401")
	}
	if err := second.Report(context.Background(), Event{Type: "error"}); err == nil {
		t.Error("overlapping send")
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrNoRetry) {
		t.Fatal(err)
	}
	if g, err := LoadSessionpipeGate(dir); err != nil || g.Status != "unauthorized" || posts.Load() != 1 {
		t.Fatalf("%+v %v posts=%d", g, err, posts.Load())
	}
}

func TestSessionpipeGateNonregularAndOversizedEvidence(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, SessionpipeGateFile)
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(dir, "example-target.json")
				if err := os.WriteFile(target, []byte(`{"version":1,"status":"ready"}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "oversized":
				if err := os.WriteFile(path, make([]byte, 1025), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSessionpipeGate(dir); err == nil {
				t.Fatal("unsafe gate read")
			}
			if err := ResumeSessionpipe(dir); err == nil {
				t.Fatal("unsafe gate replaced")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
				t.Fatalf("gate evidence changed: %v", err)
			}
		})
	}
}
