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

func TestDiscordDestinationBounds(t *testing.T) {
	for _, prefix := range []string{"https://discord.com/api/webhooks/", "https://discord.com/api/v10/webhooks/", "http://127.0.0.1:12345/api/webhooks/"} {
		dst, id, ok := discordDestination(prefix + "123456789012345678/fictional-token")
		if !ok || id != "123456789012345678" || !strings.HasSuffix(dst, "/api/v10/webhooks/123456789012345678/fictional-token?wait=true") {
			t.Fatalf("bad canonical destination %q", dst)
		}
	}
	for _, raw := range []string{"https://discord.com/api/webhooks/123/.", "https://discord.com/api/webhooks/123/..", "https://example.com/api/webhooks/123/token", "https://discord.com:443/api/webhooks/123/token", "https://discord.com/api/v9/webhooks/123/token", "https://discord.com/api/webhooks/0/token", "https://discord.com/api/webhooks/0123/token", "https://discord.com/api/webhooks/18446744073709551616/token", "https://discord.com/api/webhooks/123/%74oken", "https://discord.com/api/webhooks/123/token?thread_id=1", "https://discord.com/api/webhooks/123/token?", "https://discord.com/api/webhooks/123/token#x", "https://secret@discord.com/api/webhooks/123/token", "http://discord.com/api/webhooks/123/token"} {
		if _, _, ok := discordDestination(raw); ok {
			t.Errorf("accepted forbidden destination: %s", raw)
		}
	}
}

func TestDiscordPayloadPrivacyAndMentions(t *testing.T) {
	b, err := discordBody(Event{Type: "private event", Loop: strings.Repeat("*", 200), Text: "private text", Data: map[string]any{"private": "data"}}, "@everyone <@123>\n\u202eexample")
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil || len(obj) != 4 {
		t.Fatalf("unexpected payload: %s", b)
	}
	var content string
	json.Unmarshal(obj["content"], &content)
	if len([]rune(content)) > 2000 || strings.Contains(content, "private") || !strings.HasSuffix(content, "Event: unknown") || strings.Contains(content, "\u202e") {
		t.Fatalf("unsafe content: %q", content)
	}
	if string(obj["allowed_mentions"]) != "{\"parse\":[]}" || string(obj["flags"]) != "4" || string(obj["tts"]) != "false" {
		t.Fatal("missing mention/embed/tts controls")
	}
}

func TestDiscordAcknowledgementBounds(t *testing.T) {
	if !validDiscordReply([]byte(`{"id":"123456789012345679","webhook_id":"123456789012345678","content":"discarded"}`), "123456789012345678") {
		t.Fatal("valid message refused")
	}
	for _, b := range []string{`{}`, `null`, `{"id":"1","webhook_id":"2"}`, `{"id":1,"webhook_id":"123456789012345678"}`, `{"id":"01","webhook_id":"123456789012345678"}`, `{"id":"1","webhook_id":"123456789012345678"} {}`, `{"id":"1"`, strings.Repeat(" ", maxDiscordReply+1)} {
		if validDiscordReply([]byte(b), "123456789012345678") {
			t.Fatal("invalid response accepted")
		}
	}
}

func TestDiscordHistoryFailureDoesNotReplayAcceptedPOST(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"id":"123456789012345679","webhook_id":"123456789012345678"}`)
	}))
	defer srv.Close()
	t.Setenv("KG_TEST_URL", srv.URL+"/api/webhooks/123456789012345678/fictional")
	c := cfg("")
	c.Format = "discord"
	c.Scheme = "discord-webhook"
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

func TestDiscordLocalTransport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"accepted", 200, `{"id":"123456789012345679","webhook_id":"123456789012345678"}`, true},
		{"wrong identity", 200, `{"id":"123456789012345679","webhook_id":"9"}`, false},
		{"missing acknowledgement", 204, "", false}, {"malformed", 200, "private raw reply", false},
		{"redirect", 302, "", false}, {"rate limit", 429, "private raw reply", false}, {"failure", 500, "private raw reply", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/v10/webhooks/123456789012345678/fictional" || r.URL.RawQuery != "wait=true" || r.Method != "POST" {
					t.Error("wrong endpoint")
				}
				if r.Header.Get("Authorization") != "" || !strings.HasPrefix(r.Header.Get("User-Agent"), "DiscordBot (") {
					t.Error("wrong credential/client headers")
				}
				var payload map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload) != 4 || string(payload["allowed_mentions"]) != "{\"parse\":[]}" || string(payload["flags"]) != "4" {
					t.Error("unsafe payload")
				}
				raw, _ := json.Marshal(payload)
				if strings.Contains(string(raw), "private task") {
					t.Error("private task sent")
				}
				w.Header().Set("Location", "/api/webhooks/1/other")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			t.Setenv("KG_TEST_URL", srv.URL+"/api/webhooks/123456789012345678/fictional")
			c := cfg("")
			c.Format = "discord"
			c.Scheme = "discord-webhook"
			c.SecretEnv = ""
			sender, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			var d Delivery
			err = sender.report(context.Background(), Event{Type: "await_user", Loop: "example", Text: "private task"}, func(v Delivery) { d = v })
			if (err == nil) != tc.ok || (!tc.ok && !errors.Is(err, ErrNoRetry)) || calls.Load() != 1 || !validDelivery(d) || d.Receipt != "unverified" {
				t.Fatalf("wrong outcome: %v evidence=%+v calls=%d", err, d, calls.Load())
			}
			raw, _ := json.Marshal(d)
			if strings.Contains(string(raw), "fictional") || err != nil && (strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), srv.URL)) {
				t.Fatal("private response or URL exposed")
			}
		})
	}
}
