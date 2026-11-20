package report

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pipeGateTransport func(*http.Request) (*http.Response, error)

func (f pipeGateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionpipeFinalGatePublicationFailureStaysClosed(t *testing.T) {
	c := pipeConfig(t, "http://127.0.0.1")
	w, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	w.stateDir = dir
	path := filepath.Join(dir, SessionpipeGateFile)
	retained := filepath.Join(dir, "retained-pending.json")
	posts := 0
	w.client = &http.Client{Transport: pipeGateTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"}}`)), Header: make(http.Header)}, nil
		}
		posts++
		if g, err := LoadSessionpipeGate(dir); err != nil || g.Status != "pending" {
			t.Fatalf("POST before pending: %+v %v", g, err)
		}
		// A directory replacing the destination deterministically prevents final
		// publication without relying on the test user's directory permissions.
		if err := os.Rename(path, retained); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	if err := w.Report(context.Background(), Event{Type: "await_user"}); !errors.Is(err, ErrNoRetry) {
		t.Fatal(err)
	}
	if err := w.Report(context.Background(), Event{Type: "error"}); err == nil {
		t.Fatal("sent after failed gate publication")
	}
	if err := ResumeSessionpipe(dir); err == nil {
		t.Fatal("resume replaced invalid evidence")
	}
	if posts != 1 {
		t.Fatalf("POSTs=%d", posts)
	}
	b, err := os.ReadFile(retained)
	if err != nil || string(b) != `{"version":1,"status":"pending"}` {
		t.Fatalf("pending evidence: %s %v", b, err)
	}
}
