package report

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
)

func pushoverFixture(t *testing.T, endpoint string) config.Report {
	t.Helper()
	t.Setenv("EXAMPLE_PUSHOVER_ENDPOINT", endpoint)
	t.Setenv("EXAMPLE_PUSHOVER_TOKEN", strings.Repeat("a", 30))
	t.Setenv("EXAMPLE_PUSHOVER_USER", strings.Repeat("b", 30))
	return config.Report{Format: "pushover", Scheme: "pushover", URLEnv: "EXAMPLE_PUSHOVER_ENDPOINT", SecretEnv: "EXAMPLE_PUSHOVER_TOKEN", UserEnv: "EXAMPLE_PUSHOVER_USER", Timeout: config.Duration{Duration: time.Second}, Events: []string{"all"}, Source: "example-device"}
}

func TestPushoverHTTPAndPrivatePreview(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		code        int
		success     bool
	}{
		{"accepted", `{"status":1,"request":"example-request"}`, 200, true},
		{"rejected", `{"status":0}`, 200, false},
		{"alias", `{"STATUS":1}`, 200, false},
		{"duplicate", `{"status":0,"status":1}`, 200, false},
		{"null", `{"status":1,"status":null}`, 200, false},
		{"string", `{"status":"1"}`, 200, false},
		{"trailing", `{"status":1}{}`, 200, false},
		{"oversize", `{"status":1,"other":"` + strings.Repeat("x", maxPushoverReply) + `"}`, 200, false},
		{"empty", ``, 204, false},
		{"redirect", ``, 302, false},
		{"failure", `{"status":0}`, 500, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				if r.Method != "POST" || r.URL.RequestURI() != "/1/messages.json" {
					t.Errorf("unexpected request")
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Webhook-Signature") != "" {
					t.Error("unexpected credential header")
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if len(r.PostForm) != 3 || r.PostForm.Get("token") != strings.Repeat("a", 30) || r.PostForm.Get("user") != strings.Repeat("b", 30) {
					t.Error("wrong credential transport")
				}
				if strings.Contains(r.PostForm.Get("message"), "private-text") || strings.Contains(r.PostForm.Get("message"), "private-data") {
					t.Error("private payload leaked")
				}
				w.Header().Set("Location", "/unexpected")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.reply))
			}))
			defer srv.Close()
			cfg := pushoverFixture(t, srv.URL+"/1/messages.json")
			sender, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			event := Event{Type: "error", Loop: "example-loop", Text: "private-text", Data: map[string]any{"other": "private-data"}}
			preview, err := sender.Body(event)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(preview), strings.Repeat("a", 30)) || strings.Contains(string(preview), strings.Repeat("b", 30)) {
				t.Fatal("preview leaked credentials")
			}
			var delivery Delivery
			err = sender.report(context.Background(), event, func(d Delivery) { delivery = d })
			if (err == nil) != tc.success || posts != 1 {
				t.Fatalf("success=%v posts=%d err=%v", tc.success, posts, err)
			}
			if !tc.success && !errors.Is(err, ErrNoRetry) {
				t.Fatalf("missing no-retry marker: %v", err)
			}
			if tc.success && (delivery.Outcome != "http_accepted" || delivery.Receipt != "unverified") {
				t.Fatalf("bad evidence: %+v", delivery)
			}
		})
	}
}

func TestPushoverCredentialAndEndpointBinding(t *testing.T) {
	for _, endpoint := range []string{"https://example.com/1/messages.json", "https://api.pushover.net:443/1/messages.json", "https://api.pushover.net/1/messages.json?", "https://api.pushover.net/1/messages.json?token=example", "https://api.pushover.net/1/messages.xml", "https://api.pushover.net/1/%6dessages.json"} {
		cfg := pushoverFixture(t, endpoint)
		if _, err := New(cfg); err == nil {
			t.Fatalf("accepted endpoint %s", endpoint)
		}
	}
	cfg := pushoverFixture(t, "")
	w, err := New(cfg)
	if err != nil || w.url != "https://api.pushover.net/1/messages.json" {
		t.Fatal("default endpoint unavailable")
	}
	for _, key := range []string{"", strings.Repeat("a", 29), strings.Repeat("a", 30) + "," + strings.Repeat("b", 30), strings.Repeat("a", 29) + "!"} {
		t.Setenv(cfg.UserEnv, key)
		if _, err := New(cfg); err == nil {
			t.Fatal("accepted invalid user key")
		}
	}
}

func TestPushoverHistoryFailureAndSetup(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{"status":1}`)) }))
	defer srv.Close()
	c := pushoverFixture(t, srv.URL+"/1/messages.json")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DeliveryFile), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := NewRecorded(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	err = w.Report(context.Background(), Event{Type: "error", Loop: "example-loop"})
	if !errors.Is(err, ErrNoRetry) || !errors.Is(err, ErrDeliveryHistory) || calls != 1 {
		t.Fatalf("accepted POST replayable: %v calls=%d", err, calls)
	}
	t.Setenv(c.UserEnv, "")
	w, _ = NewRecorded(c, t.TempDir())
	err = w.Report(context.Background(), Event{Type: "error"})
	if err == nil || errors.Is(err, ErrNoRetry) || calls != 1 {
		t.Fatalf("setup semantics changed: %v", err)
	}
}
