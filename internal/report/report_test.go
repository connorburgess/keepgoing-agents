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
