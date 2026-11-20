package report

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/telegram"
)

func TestTelegramReportFromPairedStateAndDeliveryHistory(t *testing.T) {
	const tok = "123456789:EXAMPLE_REPORT_TOKEN_SECRET"
	tgDir := t.TempDir()
	loopDir := t.TempDir()
	origDir := telegramStateDir
	telegramStateDir = func() (string, error) { return tgDir, nil }
	t.Cleanup(func() { telegramStateDir = origDir })

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := telegram.SaveToken(tgDir, tok); err != nil {
		t.Fatal(err)
	}
	if err := telegram.SavePairing(tgDir, telegram.PairingRecord{
		SchemaVersion: telegram.PairingSchemaVersion,
		Mode:          telegram.ModeAlerts,
		BotID:         100001,
		BotUsername:   "example_looptender_bot",
		AllowedUserID: 7001,
		AllowedChatID: 7001,
		TopicsEnabled: true,
		LoopTopics:    map[string]int64{"example-loop": 88},
		PairedAt:      now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}

	var gotPayload map[string]any
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/bot"+tok+"/sendMessage" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":9,"message_thread_id":88,"date":1795000000}}`))
	}))
	defer srv.Close()

	t.Setenv(telegram.APIURLEnv, srv.URL)
	t.Setenv(telegram.TokenEnv, "")
	t.Setenv(telegram.ChatIDEnv, "")

	cfg := config.Report{
		Format:    "telegram",
		Scheme:    "telegram-bot",
		URLEnv:    telegram.APIURLEnv,
		SecretEnv: telegram.TokenEnv,
		Events:    []string{"await_user", "complete"},
		Timeout:   config.Duration{Duration: 2 * time.Second},
	}
	rep, err := NewRecorded(cfg, loopDir)
	if err != nil {
		t.Fatalf("NewRecorded: %v", err)
	}
	ev := Event{
		Type: "await_user",
		Loop: "example-loop",
		Text: "loop needs person",
		At:   now,
		Data: map[string]any{"target": "primary", "cause": "no_progress_timeout"},
	}
	if err := rep.Report(context.Background(), ev); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if gotPayload["chat_id"] != float64(7001) || gotPayload["message_thread_id"] != float64(88) {
		t.Fatalf("unexpected payload routing: %+v", gotPayload)
	}
	if text, _ := gotPayload["text"].(string); !strings.Contains(text, "[await_user] example-loop") || !strings.Contains(text, "hold: no_progress_timeout") {
		t.Fatalf("unexpected formatted text: %q", text)
	}

	hist, err := LoadDeliveries(loopDir)
	if err != nil {
		t.Fatalf("LoadDeliveries: %v", err)
	}
	if len(hist.Records) != 1 || hist.Records[0].Outcome != "http_accepted" || hist.Records[0].Receipt != "unverified" {
		t.Fatalf("unexpected delivery history: %+v", hist.Records)
	}
}

func TestTelegramReportFailureSingleAttemptNoTokenLeak(t *testing.T) {
	const tok = "123456789:EXAMPLE_FAIL_TOKEN_SECRET"
	loopDir := t.TempDir()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"upstream error ` + tok + `"}`))
	}))
	defer srv.Close()

	t.Setenv(telegram.APIURLEnv, srv.URL)
	t.Setenv(telegram.TokenEnv, tok)
	t.Setenv(telegram.ChatIDEnv, "7001")

	cfg := config.Report{
		Format:    "telegram",
		Scheme:    "telegram-bot",
		URLEnv:    telegram.APIURLEnv,
		SecretEnv: telegram.TokenEnv,
		Events:    []string{"all"},
		Timeout:   config.Duration{Duration: 2 * time.Second},
	}
	rep, err := NewRecorded(cfg, loopDir)
	if err != nil {
		t.Fatalf("NewRecorded: %v", err)
	}
	err = rep.Report(context.Background(), Event{Type: "complete", Loop: "example-loop", Text: "done"})
	if !errors.Is(err, ErrNoRetry) {
		t.Fatalf("err = %v, want ErrNoRetry", err)
	}
	if strings.Contains(err.Error(), tok) {
		t.Fatalf("error leaked bot token: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no duplicate retries)", calls)
	}
	hist, err := LoadDeliveries(loopDir)
	if err != nil || len(hist.Records) != 1 || hist.Records[0].Outcome != "http_rejected" {
		t.Fatalf("unexpected delivery history: %+v, err=%v", hist.Records, err)
	}
}
