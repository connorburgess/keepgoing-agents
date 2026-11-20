package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const testToken = "123456789:EXAMPLE_SECRET_TOKEN_DO_NOT_LEAK"

func TestNewClientValidation(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ClientConfig
		wantErr bool
	}{
		{name: "valid default https", cfg: ClientConfig{Token: testToken}},
		{name: "valid loopback http", cfg: ClientConfig{Token: testToken, BaseURL: "http://127.0.0.1:8080"}},
		{name: "valid localhost http", cfg: ClientConfig{Token: testToken, BaseURL: "http://localhost:8080"}},
		{name: "valid ipv6 loopback http", cfg: ClientConfig{Token: testToken, BaseURL: "http://[::1]:8080"}},
		{name: "valid custom https", cfg: ClientConfig{Token: testToken, BaseURL: "https://example.com"}},
		{name: "missing token", cfg: ClientConfig{}, wantErr: true},
		{name: "token without colon", cfg: ClientConfig{Token: "123456789"}, wantErr: true},
		{name: "token non-numeric id", cfg: ClientConfig{Token: "abc:secret"}, wantErr: true},
		{name: "token whitespace", cfg: ClientConfig{Token: "123:sec ret"}, wantErr: true},
		{name: "token slash injection", cfg: ClientConfig{Token: "123:sec/ret"}, wantErr: true},
		{name: "remote plain http rejected", cfg: ClientConfig{Token: testToken, BaseURL: "http://example.com"}, wantErr: true},
		{name: "base url with path rejected", cfg: ClientConfig{Token: testToken, BaseURL: "https://example.com/prefix"}, wantErr: true},
		{name: "base url with query rejected", cfg: ClientConfig{Token: testToken, BaseURL: "https://example.com?a=1"}, wantErr: true},
		{name: "base url with userinfo rejected", cfg: ClientConfig{Token: testToken, BaseURL: "https://user:pass@example.com"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(tc.cfg)
			if tc.wantErr {
				if err == nil || !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("got (%v, %v), want ErrInvalidConfig", c, err)
				}
				if strings.Contains(err.Error(), testToken) {
					t.Fatalf("error leaked token: %v", err)
				}
				return
			}
			if err != nil || c == nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestClientMethodsAgainstStandIn(t *testing.T) {
	var seenMethod string
	var seenBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/bot" + testToken + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		seenMethod = strings.TrimPrefix(r.URL.Path, prefix)
		seenBody = nil
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&seenBody)
		}
		w.Header().Set("Content-Type", "application/json")
		switch seenMethod {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":100001,"is_bot":true,"first_name":"Example Bot","username":"example_looptender_bot","has_topics_enabled":true,"allows_users_to_create_topics":true}}`))
		case "sendMessage":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42,"message_thread_id":7,"date":1795000000}}`))
		case "getUpdates":
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":99,"message":{"message_id":10,"date":1795000001,"from":{"id":555,"is_bot":false,"username":"example_user"},"chat":{"id":555,"type":"private"},"text":"/status"}}]}`))
		case "answerCallbackQuery", "setMyCommands":
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case "createForumTopic":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_thread_id":77,"name":"example-loop"}}`))
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{Token: testToken, BaseURL: srv.URL, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	me, err := c.GetMe(ctx)
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.ID != 100001 || me.Username != "example_looptender_bot" || !me.HasTopicsEnabled {
		t.Fatalf("unexpected BotInfo: %+v", me)
	}

	longText := strings.Repeat("a", MaxMessageRunes+50)
	sent, err := c.SendMessage(ctx, SendMessageRequest{
		ChatID:          555,
		MessageThreadID: 7,
		Text:            longText,
		ReplyMarkup: &InlineKeyboardMarkup{
			InlineKeyboard: [][]InlineKeyboardButton{{{Text: "Pause", CallbackData: "pause:example-loop"}}},
		},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if sent.MessageID != 42 || sent.MessageThreadID != 7 {
		t.Fatalf("unexpected SentMessage: %+v", sent)
	}
	gotText, _ := seenBody["text"].(string)
	if utf8.RuneCountInString(gotText) != MaxMessageRunes {
		t.Fatalf("sent text rune len = %d, want %d", utf8.RuneCountInString(gotText), MaxMessageRunes)
	}
	lpo, _ := seenBody["link_preview_options"].(map[string]any)
	if disabled, _ := lpo["is_disabled"].(bool); !disabled {
		t.Fatalf("expected link previews disabled by default, got %+v", seenBody["link_preview_options"])
	}

	updates, err := c.GetUpdates(ctx, GetUpdatesRequest{Offset: 98, TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 1 || updates[0].UpdateID != 99 || updates[0].Message.Text != "/status" {
		t.Fatalf("unexpected updates: %+v", updates)
	}

	if err := c.AnswerCallbackQuery(ctx, AnswerCallbackQueryRequest{CallbackQueryID: "cb-1", Text: "Paused"}); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if err := c.SetMyCommands(ctx, []BotCommand{{Command: "status", Description: "Show loop status"}}); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
	topic, err := c.CreateForumTopic(ctx, 555, "example-loop")
	if err != nil {
		t.Fatalf("CreateForumTopic: %v", err)
	}
	if topic.MessageThreadID != 77 || topic.Name != "example-loop" {
		t.Fatalf("unexpected topic: %+v", topic)
	}
}

func TestClientNeverLeaksTokenOrFollowsRedirects(t *testing.T) {
	redirectHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	t.Run("redirect refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
		}))
		defer srv.Close()
		c, err := NewClient(ClientConfig{Token: testToken, BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.GetMe(context.Background())
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("got %v, want ErrProtocol", err)
		}
		if redirectHit {
			t.Fatal("client followed redirect and forwarded bot token path")
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("redirect error leaked token: %v", err)
		}
	})

	t.Run("transport error strips url and token", func(t *testing.T) {
		srv := httptest.NewServer(http.NewServeMux())
		addr := srv.URL
		srv.Close()
		c, err := NewClient(ClientConfig{Token: testToken, BaseURL: addr, Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.GetMe(context.Background())
		if !errors.Is(err, ErrTransport) {
			t.Fatalf("got %v, want ErrTransport", err)
		}
		if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), addr) {
			t.Fatalf("transport error leaked token or url: %v", err)
		}
	})

	t.Run("api error omits server prose and token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests echoing ` + testToken + `","parameters":{"retry_after":30}}`))
		}))
		defer srv.Close()
		c, err := NewClient(ClientConfig{Token: testToken, BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.GetMe(context.Background())
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("got %v, want *APIError", err)
		}
		if apiErr.HTTPStatus != 429 || apiErr.ErrorCode != 429 || apiErr.RetryAfter != 30 {
			t.Fatalf("unexpected APIError: %+v", apiErr)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("api error leaked token: %v", err)
		}
	})

	t.Run("oversized response rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", MaxResponseBytes+10)))
		}))
		defer srv.Close()
		c, err := NewClient(ClientConfig{Token: testToken, BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.GetMe(context.Background())
		if !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), testToken) {
			t.Fatalf("got %v, want clean ErrProtocol", err)
		}
	})

	t.Run("canceled context preserved without token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Second)
		}))
		defer srv.Close()
		c, err := NewClient(ClientConfig{Token: testToken, BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = c.GetMe(ctx)
		if !errors.Is(err, ErrTransport) || !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want ErrTransport wrapping context.Canceled", err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("canceled error leaked token: %v", err)
		}
	})
}
