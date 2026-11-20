package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/incident"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/telegram"
)

type tgSentRecord struct {
	ChatID          int64                          `json:"chat_id"`
	MessageThreadID int64                          `json:"message_thread_id"`
	Text            string                         `json:"text"`
	ReplyMarkup     *telegram.InlineKeyboardMarkup `json:"reply_markup"`
}

type tgCallbackAnswerRecord struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text"`
}

func setupPairedTelegramDir(t *testing.T, tok string, now time.Time) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(telegram.TokenEnv, "")
	t.Setenv(telegram.APIURLEnv, "")
	if err := telegram.SaveToken(dir, tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	rec := telegram.PairingRecord{
		SchemaVersion:    telegram.PairingSchemaVersion,
		Mode:             telegram.ModeAlerts,
		BotID:            100001,
		BotUsername:      "example_looptender_bot",
		AllowedUserID:    7001,
		AllowedChatID:    7001,
		LastUpdateOffset: 10,
		PairedAt:         now.Add(-time.Hour),
		UpdatedAt:        now.Add(-time.Hour),
	}
	if err := telegram.SavePairing(dir, rec); err != nil {
		t.Fatalf("SavePairing: %v", err)
	}
	return dir
}

func TestTelegramPollStatusPauseStopAndGuards(t *testing.T) {
	const tok = "123456789:EXAMPLE_POLL_TOKEN"
	now := time.Now().UTC().Truncate(time.Second)
	tgDir := setupPairedTelegramDir(t, tok, now)
	regDir, entry := clientFixture(t)

	// Start with an enabled loop carrying a private note that must never leak.
	if err := control.Save(entry.StateDir, control.File{
		Enabled: true,
		Note:    "PRIVATE-NOTE-NEVER-LEAK",
	}); err != nil {
		t.Fatalf("control.Save: %v", err)
	}

	var mu sync.Mutex
	var queued []telegram.Update
	var sent []tgSentRecord
	var answers []tgCallbackAnswerRecord
	var requestedOffsets []int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/bot" + tok + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		method := strings.TrimPrefix(r.URL.Path, prefix)
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getUpdates":
			var req telegram.GetUpdatesRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			requestedOffsets = append(requestedOffsets, req.Offset)
			batch := queued
			queued = nil
			mu.Unlock()
			resp, _ := json.Marshal(map[string]any{"ok": true, "result": batch})
			_, _ = w.Write(resp)
		case "sendMessage":
			var msg tgSentRecord
			_ = json.NewDecoder(r.Body).Decode(&msg)
			mu.Lock()
			sent = append(sent, msg)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":100,"date":1795000100}}`))
		case "answerCallbackQuery":
			var ans tgCallbackAnswerRecord
			_ = json.NewDecoder(r.Body).Decode(&ans)
			mu.Lock()
			answers = append(answers, ans)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	runOnce := func(updates []telegram.Update) {
		t.Helper()
		mu.Lock()
		queued = updates
		mu.Unlock()
		var outBuf, errBuf bytes.Buffer
		code := telegramCommand(env{ctx: context.Background(), stdout: &outBuf, stderr: &errBuf}, []string{
			"poll", "--once", "--offline", "--dir", tgDir, "--registry", regDir, "--api-url", srv.URL, "--poll-wait", "1s",
		})
		if code != exitOK {
			t.Fatalf("telegram poll --once failed: code=%d stdout=%q stderr=%q", code, outBuf.String(), errBuf.String())
		}
		if strings.Contains(outBuf.String(), tok) || strings.Contains(errBuf.String(), tok) {
			t.Fatal("telegram poll leaked bot token")
		}
	}

	// 1. Stranger message, stranger callback, and stale owner message must all be silently ignored while advancing offset.
	runOnce([]telegram.Update{
		{
			UpdateID: 11,
			Message: &telegram.Message{
				MessageID: 1,
				Date:      now.Unix(),
				From:      &telegram.User{ID: 9999, IsBot: false},
				Chat:      telegram.Chat{ID: 9999, Type: "private"},
				Text:      "/status",
			},
		},
		{
			UpdateID: 12,
			CallbackQuery: &telegram.CallbackQuery{
				ID:      "cb_stranger",
				From:    telegram.User{ID: 9999, IsBot: false},
				Message: &telegram.Message{MessageID: 2, Date: now.Unix(), Chat: telegram.Chat{ID: 7001, Type: "private"}},
				Data:    "p:" + entry.ID,
			},
		},
		{
			UpdateID: 13,
			Message: &telegram.Message{
				MessageID: 3,
				Date:      now.Add(-30 * time.Minute).Unix(),
				From:      &telegram.User{ID: 7001, IsBot: false},
				Chat:      telegram.Chat{ID: 7001, Type: "private"},
				Text:      "/status",
			},
		},
	})
	mu.Lock()
	if len(sent) != 0 || len(answers) != 0 {
		t.Fatalf("unauthorized/stale updates produced output: sent=%+v answers=%+v", sent, answers)
	}
	mu.Unlock()
	rec, ok, err := telegram.LoadPairing(tgDir)
	if err != nil || !ok || rec.LastUpdateOffset != 13 {
		t.Fatalf("expected persisted offset 13, got rec=%+v ok=%t err=%v", rec, ok, err)
	}

	// 2. Owner sends /status -> redacted summary + Pause/Stop buttons bound to digest prefix.
	runOnce([]telegram.Update{
		{
			UpdateID: 14,
			Message: &telegram.Message{
				MessageID: 4,
				Date:      now.Unix(),
				From:      &telegram.User{ID: 7001, IsBot: false},
				Chat:      telegram.Chat{ID: 7001, Type: "private"},
				Text:      "/status",
			},
		},
	})
	mu.Lock()
	if len(sent) != 1 {
		t.Fatalf("expected 1 status message, got %d", len(sent))
	}
	statusMsg := sent[0]
	sent = nil
	mu.Unlock()
	for _, secret := range []string{"PRIVATE-NOTE", entry.ConfigPath, entry.StateDir, tok} {
		if strings.Contains(statusMsg.Text, secret) {
			t.Fatalf("status message leaked private data %q: %s", secret, statusMsg.Text)
		}
	}
	if !strings.Contains(statusMsg.Text, entry.Loop) {
		t.Fatalf("status message missing loop name %q: %s", entry.Loop, statusMsg.Text)
	}
	if statusMsg.ReplyMarkup == nil || len(statusMsg.ReplyMarkup.InlineKeyboard) != 1 || len(statusMsg.ReplyMarkup.InlineKeyboard[0]) != 2 {
		t.Fatalf("expected Pause and Stop inline buttons, got %+v", statusMsg.ReplyMarkup)
	}
	pauseData := statusMsg.ReplyMarkup.InlineKeyboard[0][0].CallbackData
	stopData := statusMsg.ReplyMarkup.InlineKeyboard[0][1].CallbackData

	// 3. Stale owner callback (> 10m old) answers with expired notice and does NOT mutate control.json.
	runOnce([]telegram.Update{
		{
			UpdateID: 15,
			CallbackQuery: &telegram.CallbackQuery{
				ID:      "cb_expired",
				From:    telegram.User{ID: 7001, IsBot: false},
				Message: &telegram.Message{MessageID: 4, Date: now.Add(-20 * time.Minute).Unix(), Chat: telegram.Chat{ID: 7001, Type: "private"}},
				Data:    pauseData,
			},
		},
	})
	ctl, err := control.Load(entry.StateDir)
	if err != nil || ctl.Paused || ctl.Stop {
		t.Fatalf("stale callback mutated controls: %+v err=%v", ctl, err)
	}
	mu.Lock()
	if len(answers) != 1 || !strings.Contains(answers[0].Text, "Expired") {
		t.Fatalf("expected expired callback answer, got %+v", answers)
	}
	answers = nil
	mu.Unlock()

	// 4. Fresh Pause callback saves Paused=true with telegram_pause attribution and panel wording.
	runOnce([]telegram.Update{
		{
			UpdateID: 16,
			CallbackQuery: &telegram.CallbackQuery{
				ID:      "cb_pause",
				From:    telegram.User{ID: 7001, IsBot: false},
				Message: &telegram.Message{MessageID: 4, Date: now.Unix(), Chat: telegram.Chat{ID: 7001, Type: "private"}},
				Data:    pauseData,
			},
		},
	})
	ctl, err = control.Load(entry.StateDir)
	if err != nil || !ctl.Paused || ctl.Stop {
		t.Fatalf("expected Paused=true Stop=false, got %+v err=%v", ctl, err)
	}
	j, err := incident.Load(entry.StateDir)
	if err != nil || len(j.Records) == 0 {
		t.Fatalf("incident.Load: %v (%d records)", err, len(j.Records))
	}
	lastRec := j.Records[len(j.Records)-1]
	if lastRec.ControlChange == nil || lastRec.ControlChange.Operation != "telegram_pause" {
		t.Fatalf("expected telegram_pause attribution, got %+v", lastRec.ControlChange)
	}
	if phrase := dockControlPhrase(lastRec); phrase != "Paused from Telegram" {
		t.Fatalf("unexpected timeline phrase: %q", phrase)
	}
	mu.Lock()
	if len(answers) != 1 || answers[0].Text != "Paused" || len(sent) != 1 || !strings.Contains(sent[0].Text, "[control_pause]") {
		t.Fatalf("unexpected pause reply: answers=%+v sent=%+v", answers, sent)
	}
	answers = nil
	sent = nil
	mu.Unlock()

	// 5. Reusing the old Stop button from before Pause must be refused as observation_changed.
	runOnce([]telegram.Update{
		{
			UpdateID: 17,
			CallbackQuery: &telegram.CallbackQuery{
				ID:      "cb_stop_stale_digest",
				From:    telegram.User{ID: 7001, IsBot: false},
				Message: &telegram.Message{MessageID: 4, Date: now.Unix(), Chat: telegram.Chat{ID: 7001, Type: "private"}},
				Data:    stopData,
			},
		},
	})
	ctl, err = control.Load(entry.StateDir)
	if err != nil || ctl.Stop {
		t.Fatalf("stale-digest stop button must not save Stop=true: %+v err=%v", ctl, err)
	}
	mu.Lock()
	if len(answers) != 1 || !strings.Contains(answers[0].Text, "State changed") {
		t.Fatalf("expected observation_changed answer, got %+v", answers)
	}
	answers = nil
	sent = nil
	mu.Unlock()

	// 6. Fresh /status after Pause offers Stop (not Pause), and tapping Stop saves Stop=true with telegram_stop.
	runOnce([]telegram.Update{
		{
			UpdateID: 18,
			Message: &telegram.Message{
				MessageID: 5,
				Date:      now.Unix(),
				From:      &telegram.User{ID: 7001, IsBot: false},
				Chat:      telegram.Chat{ID: 7001, Type: "private"},
				Text:      "/status",
			},
		},
	})
	mu.Lock()
	if len(sent) != 1 || sent[0].ReplyMarkup == nil || len(sent[0].ReplyMarkup.InlineKeyboard) != 2 || len(sent[0].ReplyMarkup.InlineKeyboard[0]) != 1 || len(sent[0].ReplyMarkup.InlineKeyboard[1]) != 1 {
		t.Fatalf("expected Stop row and Prepare resume row when already paused, got %+v", sent)
	}
	if !strings.HasPrefix(sent[0].ReplyMarkup.InlineKeyboard[0][0].Text, "Stop ") || !strings.HasPrefix(sent[0].ReplyMarkup.InlineKeyboard[1][0].Text, "Prepare resume ") {
		t.Fatalf("unexpected paused keyboard buttons: %+v", sent[0].ReplyMarkup.InlineKeyboard)
	}
	freshStopData := sent[0].ReplyMarkup.InlineKeyboard[0][0].CallbackData
	sent = nil
	mu.Unlock()

	runOnce([]telegram.Update{
		{
			UpdateID: 19,
			CallbackQuery: &telegram.CallbackQuery{
				ID:      "cb_stop_fresh",
				From:    telegram.User{ID: 7001, IsBot: false},
				Message: &telegram.Message{MessageID: 5, Date: now.Unix(), Chat: telegram.Chat{ID: 7001, Type: "private"}},
				Data:    freshStopData,
			},
		},
	})
	ctl, err = control.Load(entry.StateDir)
	if err != nil || !ctl.Stop {
		t.Fatalf("expected Stop=true after fresh Stop button, got %+v err=%v", ctl, err)
	}
	j, err = incident.Load(entry.StateDir)
	if err != nil || len(j.Records) == 0 || j.Records[len(j.Records)-1].ControlChange.Operation != "telegram_stop" {
		t.Fatalf("expected telegram_stop attribution, got %+v (err=%v)", j.Records, err)
	}

	mu.Lock()
	if len(requestedOffsets) < 2 || requestedOffsets[0] != 11 || requestedOffsets[1] != 14 {
		t.Fatalf("unexpected getUpdates offset progression: %v", requestedOffsets)
	}
	mu.Unlock()
}

func TestTelegramPollSingleInstanceLockAndRetryAfter(t *testing.T) {
	const tok = "123456789:EXAMPLE_LOCK_RETRY_TOKEN"
	now := time.Now().UTC().Truncate(time.Second)
	tgDir := setupPairedTelegramDir(t, tok, now)
	regDir, _ := clientFixture(t)

	// Holding telegram-poll lock must cause exitSkipped (75) with zero HTTP calls.
	held, err := lock.AcquireNamed(tgDir, telegramPollLock)
	if err != nil {
		t.Fatalf("AcquireNamed: %v", err)
	}
	var errBuf bytes.Buffer
	code := telegramCommand(env{ctx: context.Background(), stdout: &bytes.Buffer{}, stderr: &errBuf}, []string{
		"poll", "--once", "--dir", tgDir, "--registry", regDir,
	})
	held.Release()
	if code != exitSkipped {
		t.Fatalf("expected exitSkipped (75) when lock held, got %d stderr=%q", code, errBuf.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// RetryAfter backoff test via runTelegramPoller with deterministic Sleep.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`))
			return
		}
		cancel()
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()

	client, err := telegram.NewClient(telegram.ClientConfig{Token: tok, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var slept []time.Duration
	errBuf.Reset()
	code = runTelegramPoller(env{ctx: ctx, stdout: &bytes.Buffer{}, stderr: &errBuf}, telegramPollConfig{
		Dir:         tgDir,
		RegistryDir: regDir,
		Client:      client,
		PollWait:    time.Second,
		StaleAfter:  telegram.DefaultStalenessWindow,
		Offline:     true,
		Host:        "example-host",
		Now:         func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
		DaemonQuery: inspectDaemon,
	})
	if code != exitOK {
		t.Fatalf("expected exitOK after cancel, got %d stderr=%q", code, errBuf.String())
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Fatalf("expected single 7s RetryAfter sleep, got %v", slept)
	}
	if strings.Contains(errBuf.String(), tok) {
		t.Fatal("stderr leaked bot token on 429 error")
	}
}

func TestExpectDigestMatches(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		expect string
		want   bool
	}{
		{"", true},
		{full, true},
		{full[:16], false},
		{"sha256prefix:" + full[:16], true},
		{"sha256prefix:fedcba9876543210", false},
		{"sha256prefix:01234567", false},
		{"sha256prefix:0123456789abcdeg", false},
		{"sha256prefix:0123456789ABCDEF", false},
	}
	for _, tc := range cases {
		if got := expectDigestMatches(full, tc.expect); got != tc.want {
			t.Errorf("expectDigestMatches(%q) = %t, want %t", tc.expect, got, tc.want)
		}
	}
}
