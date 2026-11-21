package report

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNtfyRedactedSingleAttempt(t *testing.T) {
	for _, status := range []int{202, 302, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls, redirects atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1); w.WriteHeader(202) }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				b, _ := io.ReadAll(r.Body)
				if string(b) != "Loop: example-loop\nDevice label: example-device\nEvent: await_user" {
					t.Errorf("unexpected payload: %s", b)
				}
				if r.Header.Get("Content-Type") != "text/plain; charset=utf-8" || r.Header.Get("Authorization") != "Bearer fictional-token" {
					t.Error("wrong request headers")
				}
				for _, h := range []string{"Actions", "Click", "Attach", "Title"} {
					if r.Header.Get(h) != "" {
						t.Error("unexpected interactive header")
					}
				}
				w.Header().Set("Location", target.URL+"/other")
				w.WriteHeader(status)
			}))
			defer srv.Close()
			t.Setenv("KG_TEST_URL", srv.URL+"/private-topic")
			t.Setenv("KG_TEST_SECRET", "fictional-token")
			c := cfg("")
			c.Format = "ntfy"
			c.Scheme = "bearer"
			c.Source = "example-device"
			w, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			var d Delivery
			err = w.report(context.Background(), Event{Type: "await_user", Loop: "example-loop", Text: "private transcript", Data: map[string]any{"account": "private account", "path": "/private/example/task"}}, func(v Delivery) { d = v })
			if (err == nil) != (status == 202) {
				t.Fatalf("wrong result: %v", err)
			}
			if calls.Load() != 1 || redirects.Load() != 0 || len(d.Attempts) != 1 || d.Receipt != "unverified" {
				t.Fatalf("delivery calls=%d redirects=%d evidence=%+v", calls.Load(), redirects.Load(), d)
			}
			b, _ := json.Marshal(d)
			for _, s := range []string{"private-topic", "fictional-token", "private transcript", "private account", "/private/example/task"} {
				if strings.Contains(string(b), s) {
					t.Fatal("private evidence leaked")
				}
			}
		})
	}
}

func TestNtfyURLAndSchemeRefusal(t *testing.T) {
	t.Setenv("KG_TEST_SECRET", "fictional-token")
	for _, url := range []string{"https://example.com", "https://example.com/", "https://example.com/a/b", "https://example.com/%74opic", "https://example.com/topic?", "https://example.com/topic?secret=x", "https://example.com/topic#fragment", "https://name:secret@example.com/topic", "http://example.com/topic"} {
		t.Setenv("KG_TEST_URL", url)
		c := cfg("")
		c.Format = "ntfy"
		c.Scheme = "bearer"
		if _, err := New(c); err == nil {
			t.Fatalf("accepted invalid topic URL: %s", url)
		} else if strings.Contains(err.Error(), url) {
			t.Fatal("URL leaked")
		}
	}
	t.Setenv("KG_TEST_URL", "https://example.com/example-topic")
	c := cfg("")
	c.Format = "ntfy"
	if _, err := New(c); err == nil {
		t.Fatal("accepted signing scheme")
	}
	c.Scheme = "bearer"
	t.Setenv("KG_TEST_SECRET", "")
	if _, err := New(c); err == nil {
		t.Fatal("accepted anonymous publish")
	}
}

func TestNtfyBodyBoundsAndUnknownEvent(t *testing.T) {
	b := ntfyBody(Event{Type: "private type", Loop: strings.Repeat("界", 200) + "\nprivate-tail", Text: "private text", Data: map[string]any{"private": "data"}}, "device\n\x00\u202eexample")
	if len(b) > 1100 || strings.Contains(string(b), "private") || !strings.HasSuffix(string(b), "Event: unknown") || !strings.Contains(string(b), "Device label: deviceexample") {
		t.Fatalf("unsafe body: %s", b)
	}
}

func TestNtfyTransportUncertaintyIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		c, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		c.Close()
	}))
	defer srv.Close()
	t.Setenv("KG_TEST_URL", srv.URL+"/example-topic")
	t.Setenv("KG_TEST_SECRET", "fictional-token")
	c := cfg("")
	c.Format = "ntfy"
	c.Scheme = "bearer"
	w, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Report(context.Background(), Event{Type: "error", Loop: "example-loop"}); err == nil || err.Error() != "report: request failed" {
		t.Fatalf("unexpected transport result: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("retried ambiguous delivery: %d", calls.Load())
	}
}
