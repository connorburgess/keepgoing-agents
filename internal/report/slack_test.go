package report

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSlackLocalContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reply  string
		ok     bool
	}{
		{"accepted", 200, "ok", true}, {"bad reply", 200, "private response", false}, {"oversize", 200, strings.Repeat("x", 65), false}, {"wrong success", 202, "ok", false}, {"redirect", 302, "", false}, {"rejected", 500, "private failure", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.Header.Get("Authorization") != "" || r.Header.Get("Webhook-Signature") != "" {
					t.Error("wrong transport")
				}
				b, _ := io.ReadAll(r.Body)
				var body struct {
					Text   string `json:"text"`
					Blocks []struct {
						Text struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"text"`
					} `json:"blocks"`
				}
				if json.Unmarshal(b, &body) != nil || body.Text != "Looptender notification" || len(body.Blocks) != 1 || body.Blocks[0].Text.Type != "plain_text" || body.Blocks[0].Text.Text != "Loop: <!channel>\nDevice label: example-device\nEvent: await_user" {
					t.Errorf("bad body: %s", b)
				}
				if strings.Contains(string(b), "private task") {
					t.Error("private data leaked")
				}
				w.Header().Set("Location", "/services/other/other/other")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.reply)
			}))
			defer srv.Close()
			t.Setenv("KG_TEST_URL", srv.URL+"/services/example/example/fictional")
			c := cfg("")
			c.Format = "slack"
			c.Scheme = "slack-webhook"
			c.SecretEnv = ""
			c.Source = "example-device"
			sender, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			var d Delivery
			err = sender.report(context.Background(), Event{Type: "await_user", Loop: "<!channel>", Text: "private task", Data: map[string]any{"task": "private task"}}, func(v Delivery) { d = v })
			if (err == nil) != tc.ok || (!tc.ok && !errors.Is(err, ErrNoRetry)) {
				t.Fatalf("wrong outcome: %v", err)
			}
			if calls.Load() != 1 || len(d.Attempts) != 1 || d.Receipt != "unverified" {
				t.Fatalf("wrong attempts/evidence: %+v", d)
			}
			if !validDelivery(d) {
				t.Fatalf("invalid saved evidence: %+v", d)
			}
			raw, _ := json.Marshal(d)
			if strings.Contains(string(raw), "fictional") || (err != nil && strings.Contains(err.Error(), "private")) {
				t.Fatal("private reply/URL leaked")
			}
		})
	}
}

func TestSlackCredentialURLRefusal(t *testing.T) {
	for _, raw := range []string{"https://example.com/services/a/b/c", "https://hooks.slack.com.evil.example/services/a/b/c", "https://hooks.slack.com:443/services/a/b/c", "https://hooks.slack.com/services/a/b/c?", "https://hooks.slack.com/services/a/b/c#x", "https://name:secret@hooks.slack.com/services/a/b/c", "https://hooks.slack.com/services/a/b/%63", "http://hooks.slack.com/services/a/b/c", "https://hooks.slack.com/services/a/b/c/d"} {
		t.Setenv("KG_TEST_URL", raw)
		c := cfg("")
		c.Format = "slack"
		c.Scheme = "slack-webhook"
		c.SecretEnv = ""
		if _, err := New(c); err == nil || strings.Contains(err.Error(), raw) {
			t.Fatalf("bad refusal: %v", err)
		}
	}
	for _, host := range []string{"hooks.slack.com", "hooks.slack-gov.com"} {
		t.Setenv("KG_TEST_URL", "https://"+host+"/services/example/example/fictional")
		c := cfg("")
		c.Format = "slack"
		c.Scheme = "slack-webhook"
		c.SecretEnv = ""
		if _, err := New(c); err != nil {
			t.Fatal(err)
		} // no HTTP invocation
	}
}

func TestSlackHistoryFailureDoesNotReplayAcceptedPOST(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, "ok") }))
	defer srv.Close()
	t.Setenv("KG_TEST_URL", srv.URL+"/services/example/example/fictional")
	c := cfg("")
	c.Format = "slack"
	c.Scheme = "slack-webhook"
	c.SecretEnv = ""
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DeliveryFile), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := NewRecorded(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	err = w.Report(context.Background(), Event{Type: "error", Loop: "example-loop"})
	if !errors.Is(err, ErrNoRetry) || !errors.Is(err, ErrDeliveryHistory) || calls.Load() != 1 {
		t.Fatalf("accepted POST replayable: %v calls=%d", err, calls.Load())
	}
	t.Setenv("KG_TEST_URL", "")
	w, _ = NewRecorded(c, t.TempDir())
	err = w.Report(context.Background(), Event{Type: "error"})
	if err == nil || errors.Is(err, ErrNoRetry) || calls.Load() != 1 {
		t.Fatalf("setup failure changed retry semantics: %v", err)
	}
}
