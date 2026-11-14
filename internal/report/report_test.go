package report

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

var secret = "whsec_" + base64.StdEncoding.EncodeToString([]byte("fictional-test-secret-0123456789"))

func cfg(url string) config.Report {
	return config.Report{URLEnv: "KG_TEST_URL", SecretEnv: "KG_TEST_SECRET", Source: "example-loop", Scheme: "standard-webhooks", Format: "standard", Events: config.DefaultReportEvents, Timeout: config.Duration{Duration: 5 * time.Second}}
}

// Known-answer test from the Standard Webhooks reference vectors.
func TestSignKnownVector(t *testing.T) {
	h, err := Sign("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if err != nil {
		t.Fatal(err)
	}
	if h["webhook-signature"] != "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=" {
		t.Errorf("signature = %s", h["webhook-signature"])
	}
}

func TestReportSignsAndFilters(t *testing.T) {
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sec, _ := strconv.ParseInt(r.Header.Get("webhook-timestamp"), 10, 64)
		want, _ := Sign(secret, r.Header.Get("webhook-id"), time.Unix(sec, 0), body)
		if r.Header.Get("webhook-signature") != want["webhook-signature"] {
			w.WriteHeader(401)
			return
		}
		var v map[string]any
		json.Unmarshal(body, &v)
		if v["type"] != "keepgoing.handoff" {
			w.WriteHeader(400)
			return
		}
		got.Add(1)
		w.WriteHeader(202)
	}))
	defer srv.Close()
	t.Setenv("KG_TEST_URL", srv.URL)
	t.Setenv("KG_TEST_SECRET", secret)
	w, err := New(cfg(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Report(context.Background(), Event{Type: "handoff", Loop: "example-loop", Text: "moved to codex-main"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Report(context.Background(), Event{Type: "wake", Loop: "example-loop", Text: "not subscribed"}); err != nil {
		t.Fatal(err)
	}
	if got.Load() != 1 {
		t.Errorf("deliveries = %d", got.Load())
	}
}

func TestRetryOnceOn5xxAndTextFormat(t *testing.T) {
	var n atomic.Int32
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if n.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(202)
	}))
	defer srv.Close()
	dir := t.TempDir()
	envFile := filepath.Join(dir, "creds.env")
	os.WriteFile(envFile, []byte("# fictional\nexport KG_TEST_URL="+srv.URL+"\nKG_TEST_SECRET=\""+secret+"\"\n"), 0o600)
	c := cfg(srv.URL)
	c.SecretFile, c.Format = envFile, "text"
	w, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Report(context.Background(), Event{Type: "await_user", Text: "needs a person"}); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 || !strings.Contains(body, `"source":"example-loop"`) || !strings.Contains(body, `"text":"needs a person"`) {
		t.Errorf("n=%d body=%s", n.Load(), body)
	}
}

func TestNewRejectsBadSettings(t *testing.T) {
	t.Setenv("KG_TEST_SECRET", secret)
	t.Setenv("KG_TEST_URL", "http://example.com/hook")
	if _, err := New(cfg("")); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("plain http to a remote host must be rejected: %v", err)
	}
	t.Setenv("KG_TEST_URL", "https://example.com/hook")
	t.Setenv("KG_TEST_SECRET", "not base64!")
	if _, err := New(cfg("")); err == nil {
		t.Error("bad secret must be rejected")
	}
	t.Setenv("KG_TEST_SECRET", "")
	if _, err := New(cfg("")); err == nil {
		t.Error("missing secret must be rejected")
	}
}

// With no names configured, the URL and secret are LOOPTENDER_WEBHOOK_URL
// and LOOPTENDER_WEBHOOK_SECRET, each also read under its earlier name: the
// environment before the file, and within each the current name first. A
// name the configuration chose is read exactly as written.
func TestDefaultNamesAreReadUnderBothNames(t *testing.T) {
	const (
		url, secret       = "LOOPTENDER_WEBHOOK_URL", "LOOPTENDER_WEBHOOK_SECRET"
		oldURL, oldSecret = "KEEPGOING_WEBHOOK_URL", "KEEPGOING_WEBHOOK_SECRET"
	)
	file := func(t *testing.T, lines ...string) string {
		p := filepath.Join(t.TempDir(), "creds.env")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name       string
		env        map[string]string
		file       []string
		url, token string // "" url: refused
	}{
		{name: "the current names", env: map[string]string{url: "https://new.example.com/hook", secret: "new-token"}, url: "https://new.example.com/hook", token: "new-token"},
		{name: "the earlier names", env: map[string]string{oldURL: "https://old.example.com/hook", oldSecret: "old-token"}, url: "https://old.example.com/hook", token: "old-token"},
		{name: "one of each", env: map[string]string{url: "https://new.example.com/hook", oldSecret: "old-token"}, url: "https://new.example.com/hook", token: "old-token"},
		{name: "both: the current names win", env: map[string]string{url: "https://new.example.com/hook", secret: "new-token", oldURL: "https://old.example.com/hook", oldSecret: "old-token"}, url: "https://new.example.com/hook", token: "new-token"},
		{name: "earlier names in the file", file: []string{oldURL + "=https://old.example.com/hook", oldSecret + "=old-token"}, url: "https://old.example.com/hook", token: "old-token"},
		{name: "both in the file: the current names win", file: []string{oldURL + "=https://old.example.com/hook", oldSecret + "=old-token", url + "=https://new.example.com/hook", secret + "=new-token"}, url: "https://new.example.com/hook", token: "new-token"},
		{name: "the environment before the file, whichever name", env: map[string]string{oldURL: "https://old.example.com/hook", oldSecret: "old-token"}, file: []string{url + "=https://new.example.com/hook", secret + "=new-token"}, url: "https://old.example.com/hook", token: "old-token"},
		{name: "neither"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, n := range []string{url, secret, oldURL, oldSecret} {
				t.Setenv(n, tc.env[n])
			}
			c := config.Report{URLEnv: url, SecretEnv: secret, Scheme: "bearer", Format: "text", Events: config.DefaultReportEvents, Timeout: config.Duration{Duration: time.Second}}
			if tc.file != nil {
				c.SecretFile = file(t, tc.file...)
			}
			w, err := New(c)
			if tc.url == "" {
				if err == nil || !strings.Contains(err.Error(), url) {
					t.Fatalf("want a refusal naming %s, got %v", url, err)
				}
				return
			}
			if err != nil || w.url != tc.url || w.secret != tc.token {
				t.Fatalf("New: %v; read %q and %q, want %q and %q", err, w.url, w.secret, tc.url, tc.token)
			}
		})
	}

	// A configuration that names the earlier variables itself reads only
	// them, and one that names its own reads no other.
	for _, n := range []string{url, secret, oldURL, oldSecret} {
		t.Setenv(n, "")
	}
	t.Setenv(url, "https://new.example.com/hook")
	t.Setenv(secret, "new-token")
	for _, names := range [][2]string{{oldURL, oldSecret}, {"EXAMPLE_HOOK_URL", "EXAMPLE_HOOK_SECRET"}} {
		c := config.Report{URLEnv: names[0], SecretEnv: names[1], Scheme: "bearer", Format: "text", Events: config.DefaultReportEvents, Timeout: config.Duration{Duration: time.Second}}
		if _, err := New(c); err == nil {
			t.Fatalf("%s was read from another name", names[0])
		}
	}
}
