package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/looptender/looptender/internal/telegram"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestTelegramCLIWorkflowAgainstStandIn(t *testing.T) {
	const tok = "123456789:EXAMPLE_CLI_PAIRING_TOKEN"
	dir := t.TempDir()
	t.Setenv(telegram.TokenEnv, "")
	t.Setenv(telegram.APIURLEnv, "")

	var mu sync.Mutex
	var pairCode string
	var sentMessages []string
	var commandsSet bool
	var outBuf syncBuffer

	nonceRE := regexp.MustCompile(`https://t\.me/example_looptender_bot\?start=(pair_[A-Za-z0-9_-]+)`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/bot" + tok + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		method := strings.TrimPrefix(r.URL.Path, prefix)
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":100001,"is_bot":true,"first_name":"Example Bot","username":"example_looptender_bot","has_topics_enabled":true}}`))
		case "setMyCommands":
			mu.Lock()
			commandsSet = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case "getUpdates":
			// Extract the pairing code printed to stdout and return a matching private /start update.
			mu.Lock()
			if pairCode == "" {
				if m := nonceRE.FindStringSubmatch(outBuf.String()); len(m) == 2 {
					pairCode = m[1]
				}
			}
			code := pairCode
			mu.Unlock()
			if code == "" {
				_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
				return
			}
			payload, _ := json.Marshal(map[string]any{
				"ok": true,
				"result": []map[string]any{
					{
						"update_id": 10,
						"message": map[string]any{
							"message_id": 1,
							"date":       1795000000,
							"from":       map[string]any{"id": 7001, "is_bot": false, "username": "example_owner"},
							"chat":       map[string]any{"id": 7001, "type": "private"},
							"text":       "/start " + code,
						},
					},
				},
			})
			_, _ = w.Write(payload)
		case "sendMessage":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			text, _ := body["text"].(string)
			mu.Lock()
			sentMessages = append(sentMessages, text)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2,"date":1795000001}}`))
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// 1. chat mode without explicit --accept-non-e2e must refuse.
	var errBuf bytes.Buffer
	code := telegramCommandWithInput(env{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &errBuf}, []string{
		"setup", "--dir", dir, "--mode", "chat", "--token-stdin", "--api-url", srv.URL,
	}, strings.NewReader(tok+"\n"))
	if code != exitUsage || !strings.Contains(errBuf.String(), "--accept-non-e2e") {
		t.Fatalf("chat mode without flag: code=%d stderr=%q", code, errBuf.String())
	}

	// 2. Full zero-infra setup via stdin token + deep-link pairing.
	errBuf.Reset()
	code = telegramCommandWithInput(env{ctx: context.Background(), stdout: &outBuf, stderr: &errBuf}, []string{
		"setup", "--dir", dir, "--token-stdin", "--api-url", srv.URL, "--timeout", "5s", "--poll-wait", "1s",
	}, strings.NewReader(tok+"\n"))
	if code != exitOK {
		t.Fatalf("setup failed: code=%d stdout=%q stderr=%q", code, outBuf.String(), errBuf.String())
	}
	if strings.Contains(outBuf.String(), tok) || strings.Contains(errBuf.String(), tok) {
		t.Fatal("setup output leaked bot token")
	}
	if !commandsSet {
		t.Fatal("expected setMyCommands to be called during setup")
	}

	// 3. Read-only status --json never leaks token and shows paired details.
	var statusOut bytes.Buffer
	errBuf.Reset()
	if code := telegramCommand(env{ctx: context.Background(), stdout: &statusOut, stderr: &errBuf}, []string{
		"status", "--dir", dir, "--json",
	}); code != exitOK {
		t.Fatalf("status failed: code=%d stderr=%q", code, errBuf.String())
	}
	if strings.Contains(statusOut.String(), tok) {
		t.Fatal("status output leaked bot token")
	}
	var st telegramStatusView
	if err := json.Unmarshal(statusOut.Bytes(), &st); err != nil {
		t.Fatalf("decode status json: %v", err)
	}
	if !st.Paired || st.BotUsername != "example_looptender_bot" || st.Mode != telegram.ModeAlerts || !st.TopicsEnabled || !st.TokenPresent {
		t.Fatalf("unexpected status view: %+v", st)
	}

	// 4. Test send alert (dry-run and stand-in send).
	var testOut bytes.Buffer
	errBuf.Reset()
	if code := telegramCommand(env{ctx: context.Background(), stdout: &testOut, stderr: &errBuf}, []string{
		"test", "--dir", dir, "--dry-run",
	}); code != exitOK || !strings.Contains(testOut.String(), "[test]") {
		t.Fatalf("test --dry-run: code=%d out=%q err=%q", code, testOut.String(), errBuf.String())
	}

	testOut.Reset()
	errBuf.Reset()
	if code := telegramCommand(env{ctx: context.Background(), stdout: &testOut, stderr: &errBuf}, []string{
		"test", "--dir", dir, "--api-url", srv.URL,
	}); code != exitOK {
		t.Fatalf("test send: code=%d out=%q err=%q", code, testOut.String(), errBuf.String())
	}
	mu.Lock()
	gotSent := len(sentMessages)
	mu.Unlock()
	if gotSent < 2 {
		t.Fatalf("expected pairing confirmation + test alert messages, got %d", gotSent)
	}

	// 5. Disconnect cleans up both pairing.json and token.env.
	var discOut bytes.Buffer
	errBuf.Reset()
	if code := telegramCommand(env{ctx: context.Background(), stdout: &discOut, stderr: &errBuf}, []string{
		"disconnect", "--dir", dir,
	}); code != exitOK {
		t.Fatalf("disconnect failed: code=%d err=%q", code, errBuf.String())
	}
	statusOut.Reset()
	if code := telegramCommand(env{ctx: context.Background(), stdout: &statusOut, stderr: &errBuf}, []string{
		"status", "--dir", dir, "--json",
	}); code != exitOK {
		t.Fatalf("status after disconnect failed: code=%d", code)
	}
	st = telegramStatusView{}
	if err := json.Unmarshal(statusOut.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Paired || st.TokenPresent {
		t.Fatalf("expected unpaired and token removed after disconnect, got %+v", st)
	}
}
