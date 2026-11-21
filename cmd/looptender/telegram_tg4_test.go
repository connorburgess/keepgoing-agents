package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/incident"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/telegram"
)

func TestTelegramPollTicketedResumeFlowAndGuards(t *testing.T) {
	const tok = "123456789:EXAMPLE_TG4_RESUME_TOKEN"
	now := time.Now().UTC().Truncate(time.Second)
	tgDir := setupPairedTelegramDir(t, tok, now)
	regDir, entry := clientFixture(t)

	// Start with a paused + awaiting-user loop carrying a private note that must never leak.
	if err := control.Save(entry.StateDir, control.File{
		Enabled:      true,
		Paused:       true,
		AwaitingUser: true,
		Note:         "PRIVATE-HOLD-NOTE-NEVER-LEAK",
	}); err != nil {
		t.Fatalf("control.Save: %v", err)
	}

	var mu sync.Mutex
	var queued []telegram.Update
	var sent []tgSentRecord
	var answers []tgCallbackAnswerRecord

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
			mu.Lock()
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
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":200,"date":1795000200}}`))
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
	}

	// 1. /status renders Prepare resume button for the held loop without leaking private notes.
	runOnce([]telegram.Update{{
		UpdateID: 11,
		Message: &telegram.Message{
			MessageID: 1,
			Date:      now.Unix(),
			From:      &telegram.User{ID: 7001, IsBot: false},
			Chat:      telegram.Chat{ID: 7001, Type: "private"},
			Text:      "/status",
		},
	}})

	mu.Lock()
	if len(sent) != 1 {
		mu.Unlock()
		t.Fatalf("expected 1 status message, got %d", len(sent))
	}
	statusMsg := sent[0]
	mu.Unlock()

	if strings.Contains(statusMsg.Text, "PRIVATE-HOLD-NOTE-NEVER-LEAK") {
		t.Fatalf("status message leaked private hold note: %q", statusMsg.Text)
	}
	var prepareData string
	if statusMsg.ReplyMarkup != nil {
		for _, row := range statusMsg.ReplyMarkup.InlineKeyboard {
			for _, btn := range row {
				if strings.HasPrefix(btn.Text, "Prepare resume ") {
					prepareData = btn.CallbackData
				}
			}
		}
	}
	if prepareData == "" {
		t.Fatalf("expected Prepare resume button in /status keyboard, got %+v", statusMsg.ReplyMarkup)
	}

	// 2. Tapping Prepare resume mints a single-use ticket, sends redacted review with Confirm resume button,
	//    and leaves holds intact until confirmed.
	runOnce([]telegram.Update{{
		UpdateID: 12,
		CallbackQuery: &telegram.CallbackQuery{
			ID:   "cb-prep-1",
			From: telegram.User{ID: 7001, IsBot: false},
			Message: &telegram.Message{
				MessageID: 200,
				Date:      now.Unix(),
				Chat:      telegram.Chat{ID: 7001, Type: "private"},
			},
			Data: prepareData,
		},
	}})

	ctlAfterPrep, err := control.Load(entry.StateDir)
	if err != nil {
		t.Fatalf("control.Load after prepare: %v", err)
	}
	if !ctlAfterPrep.Paused || !ctlAfterPrep.AwaitingUser {
		t.Fatalf("prepare-resume must not clear holds: %+v", ctlAfterPrep)
	}

	mu.Lock()
	if len(sent) != 2 || len(answers) != 1 {
		mu.Unlock()
		t.Fatalf("expected 2 sent messages and 1 answer after prepare, got sent=%d answers=%d", len(sent), len(answers))
	}
	reviewMsg := sent[1]
	prepAns := answers[0]
	mu.Unlock()

	if prepAns.Text != "Review resume" {
		t.Fatalf("unexpected prepare callback answer: %q", prepAns.Text)
	}
	if strings.Contains(reviewMsg.Text, "PRIVATE-HOLD-NOTE-NEVER-LEAK") {
		t.Fatalf("resume review leaked private hold note: %q", reviewMsg.Text)
	}
	if !strings.Contains(reviewMsg.Text, "Holds to clear: paused, awaiting-user") {
		t.Fatalf("expected holds list in review message:\n%s", reviewMsg.Text)
	}
	if reviewMsg.ReplyMarkup == nil || len(reviewMsg.ReplyMarkup.InlineKeyboard) != 1 || len(reviewMsg.ReplyMarkup.InlineKeyboard[0]) != 1 {
		t.Fatalf("expected Confirm resume button, got %+v", reviewMsg.ReplyMarkup)
	}
	confirmData := reviewMsg.ReplyMarkup.InlineKeyboard[0][0].CallbackData
	if len(confirmData) != 47 {
		t.Fatalf("expected 47-byte confirm callback payload, got %d (%q)", len(confirmData), confirmData)
	}

	// 3. Tapping Confirm resume consumes the ticket, clears holds, and records telegram_resume attribution.
	runOnce([]telegram.Update{{
		UpdateID: 13,
		CallbackQuery: &telegram.CallbackQuery{
			ID:   "cb-confirm-1",
			From: telegram.User{ID: 7001, IsBot: false},
			Message: &telegram.Message{
				MessageID: 201,
				Date:      now.Unix(),
				Chat:      telegram.Chat{ID: 7001, Type: "private"},
			},
			Data: confirmData,
		},
	}})

	ctlAfterConfirm, err := control.Load(entry.StateDir)
	if err != nil {
		t.Fatalf("control.Load after confirm: %v", err)
	}
	if ctlAfterConfirm.Paused || ctlAfterConfirm.AwaitingUser || ctlAfterConfirm.Stop || ctlAfterConfirm.Completed {
		t.Fatalf("expected holds cleared after confirm resume, got %+v", ctlAfterConfirm)
	}

	j, err := incident.Load(entry.StateDir)
	if err != nil {
		t.Fatalf("incident.Load: %v", err)
	}
	var sawTelegramResume bool
	for _, r := range j.Records {
		if r.ControlChange != nil && r.ControlChange.Operation == "telegram_resume" {
			sawTelegramResume = true
			phrase := dockControlPhrase(r)
			if !strings.Contains(phrase, " from Telegram") {
				t.Errorf("expected dockControlPhrase to attribute Telegram, got %q", phrase)
			}
		}
	}
	if !sawTelegramResume {
		t.Fatalf("expected telegram_resume record in incident journal, got %+v", j.Records)
	}

	// 4. Replaying the same Confirm resume callback with a newer update_id is refused (single-use ticket).
	if err := control.Save(entry.StateDir, control.File{Enabled: true, Paused: true}); err != nil {
		t.Fatalf("control.Save re-pause: %v", err)
	}
	runOnce([]telegram.Update{{
		UpdateID: 14,
		CallbackQuery: &telegram.CallbackQuery{
			ID:   "cb-replay-1",
			From: telegram.User{ID: 7001, IsBot: false},
			Message: &telegram.Message{
				MessageID: 201,
				Date:      now.Unix(),
				Chat:      telegram.Chat{ID: 7001, Type: "private"},
			},
			Data: confirmData,
		},
	}})

	ctlAfterReplay, err := control.Load(entry.StateDir)
	if err != nil || !ctlAfterReplay.Paused {
		t.Fatalf("replayed ticket must not clear new pause hold: err=%v ctl=%+v", err, ctlAfterReplay)
	}
	mu.Lock()
	replayAns := answers[len(answers)-1]
	mu.Unlock()
	if !strings.Contains(replayAns.Text, "Ticket expired") {
		t.Fatalf("expected ticket expired toast on replay, got %q", replayAns.Text)
	}

	// 5. Local control mutation between Prepare and Confirm refuses confirmation (observation_changed).
	//    First prepare a fresh ticket on the paused loop.
	var freshCtl control.File
	if err := readTransferRecord(entry.StateDir, control.FileName, &freshCtl); err != nil {
		t.Fatal(err)
	}
	freshPrepData, err := telegram.EncodeControlCallback(telegram.ControlCallback{
		Action:         telegram.ActionPrepareResume,
		RegistrationID: entry.ID,
		ExpectPrefix:   controlDigest(freshCtl)[:telegram.ExpectDigestPrefixLen],
	})
	if err != nil {
		t.Fatal(err)
	}
	runOnce([]telegram.Update{{
		UpdateID: 15,
		CallbackQuery: &telegram.CallbackQuery{
			ID:      "cb-prep-2",
			From:    telegram.User{ID: 7001, IsBot: false},
			Message: &telegram.Message{MessageID: 202, Date: now.Unix(), Chat: telegram.Chat{ID: 7001, Type: "private"}},
			Data:    freshPrepData,
		},
	}})
	mu.Lock()
	confirmData2 := sent[len(sent)-1].ReplyMarkup.InlineKeyboard[0][0].CallbackData
	mu.Unlock()

	// Mutate control state locally (operator adds stop) before tapping Confirm.
	if err := control.Save(entry.StateDir, control.File{Enabled: true, Paused: true, Stop: true}); err != nil {
		t.Fatal(err)
	}
	runOnce([]telegram.Update{{
		UpdateID: 16,
		CallbackQuery: &telegram.CallbackQuery{
			ID:      "cb-confirm-stale",
			From:    telegram.User{ID: 7001, IsBot: false},
			Message: &telegram.Message{MessageID: 203, Date: now.Unix(), Chat: telegram.Chat{ID: 7001, Type: "private"}},
			Data:    confirmData2,
		},
	}})
	ctlAfterStale, err := control.Load(entry.StateDir)
	if err != nil || !ctlAfterStale.Stop || !ctlAfterStale.Paused {
		t.Fatalf("stale confirm must leave local stop+pause intact: err=%v ctl=%+v", err, ctlAfterStale)
	}
	mu.Lock()
	staleAns := answers[len(answers)-1]
	mu.Unlock()
	if !strings.Contains(staleAns.Text, "State changed") {
		t.Fatalf("expected State changed toast after concurrent local edit, got %q", staleAns.Text)
	}
}

func TestTelegramTopicsSyncAndTopicScopedStatus(t *testing.T) {
	const tok = "123456789:EXAMPLE_TG4_TOPICS_TOKEN"
	now := time.Now().UTC().Truncate(time.Second)
	tgDir := setupPairedTelegramDir(t, tok, now)
	regDir, entryA := clientFixture(t)

	// Add a second registered loop so we can verify topic-scoped /status vs main DM /status.
	cfgB := filepath.Join(t.TempDir(), "keepgoing.yaml")
	bodyB := `version: 1
loop:
  name: beta-loop
  state_dir: state
  adapter: bb
  provider: example
  bb: {thread: thr_example_beta, cli: nonexistent-provider-fixture}
  prompt_file: private-missing-task.txt
`
	if err := os.WriteFile(cfgB, []byte(bodyB), 0600); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := registryRun(t, regDir, "add", "-c", cfgB); code != 0 {
		t.Fatalf("register beta-loop: code=%d out=%q err=%q", code, out, errs)
	}

	// Enable topics in pairing.json.
	rec, ok, err := telegram.LoadPairing(tgDir)
	if err != nil || !ok {
		t.Fatalf("LoadPairing: ok=%v err=%v", ok, err)
	}
	rec.TopicsEnabled = true
	if err := telegram.SavePairing(tgDir, rec); err != nil {
		t.Fatalf("SavePairing: %v", err)
	}

	var mu sync.Mutex
	var nextThreadID int64 = 700
	var createdTopics []string
	var queued []telegram.Update
	var sent []tgSentRecord

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/bot" + tok + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		method := strings.TrimPrefix(r.URL.Path, prefix)
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "createForumTopic":
			var req struct {
				ChatID int64  `json:"chat_id"`
				Name   string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			nextThreadID++
			tid := nextThreadID
			createdTopics = append(createdTopics, req.Name)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_thread_id": tid,
					"name":              req.Name,
				},
			})
		case "getUpdates":
			mu.Lock()
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
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":300,"date":1795000300}}`))
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// 1. `looptender telegram topics --sync --json` creates per-loop topics and persists them.
	var outBuf, errBuf bytes.Buffer
	code := telegramCommand(env{ctx: context.Background(), stdout: &outBuf, stderr: &errBuf}, []string{
		"topics", "--sync", "--json", "--dir", tgDir, "--registry", regDir, "--api-url", srv.URL,
	})
	if code != exitOK {
		t.Fatalf("telegram topics --sync failed: code=%d stdout=%q stderr=%q", code, outBuf.String(), errBuf.String())
	}
	var view telegramTopicsView
	if err := json.Unmarshal(outBuf.Bytes(), &view); err != nil {
		t.Fatalf("decode topics JSON: %v\n%s", err, outBuf.String())
	}
	if !view.TopicsEnabled || len(view.Topics) != 2 {
		t.Fatalf("unexpected topics view: %+v", view)
	}
	var threadA int64
	for _, topic := range view.Topics {
		if topic.Loop == entryA.Loop {
			threadA = topic.ThreadID
		}
		if !strings.HasPrefix(topic.Link, "https://t.me/example_looptender_bot/") {
			t.Errorf("unexpected topic link %q", topic.Link)
		}
	}
	if threadA <= 0 {
		t.Fatalf("missing topic for %s in %+v", entryA.Loop, view.Topics)
	}

	// 2. /status inside entryA's topic scopes output to entryA only; /status in main DM lists both loops.
	mu.Lock()
	queued = []telegram.Update{
		{
			UpdateID: 11,
			Message: &telegram.Message{
				MessageID:       1,
				MessageThreadID: threadA,
				Date:            now.Unix(),
				From:            &telegram.User{ID: 7001, IsBot: false},
				Chat:            telegram.Chat{ID: 7001, Type: "private"},
				Text:            "/status",
			},
		},
		{
			UpdateID: 12,
			Message: &telegram.Message{
				MessageID: 2,
				Date:      now.Unix(),
				From:      &telegram.User{ID: 7001, IsBot: false},
				Chat:      telegram.Chat{ID: 7001, Type: "private"},
				Text:      "/status",
			},
		},
	}
	mu.Unlock()

	outBuf.Reset()
	errBuf.Reset()
	code = telegramCommand(env{ctx: context.Background(), stdout: &outBuf, stderr: &errBuf}, []string{
		"poll", "--once", "--offline", "--dir", tgDir, "--registry", regDir, "--api-url", srv.URL, "--poll-wait", "1s",
	})
	if code != exitOK {
		t.Fatalf("telegram poll --once failed: code=%d stderr=%q", code, errBuf.String())
	}
	mu.Lock()
	if len(sent) != 2 {
		mu.Unlock()
		t.Fatalf("expected 2 status messages, got %d", len(sent))
	}
	scopedMsg := sent[0]
	allMsg := sent[1]
	mu.Unlock()

	if scopedMsg.MessageThreadID != threadA {
		t.Fatalf("scoped status should reply in thread %d, got %d", threadA, scopedMsg.MessageThreadID)
	}
	if !strings.Contains(scopedMsg.Text, entryA.Loop) || strings.Contains(scopedMsg.Text, "beta-loop") {
		t.Fatalf("scoped status should contain only %s:\n%s", entryA.Loop, scopedMsg.Text)
	}
	if allMsg.MessageThreadID != 0 || !strings.Contains(allMsg.Text, entryA.Loop) || !strings.Contains(allMsg.Text, "beta-loop") {
		t.Fatalf("main DM status should list both loops:\n%s", allMsg.Text)
	}

	// 3. When TopicsEnabled is false, `telegram topics` falls back cleanly to single DM with zero API calls.
	rec, _, _ = telegram.LoadPairing(tgDir)
	rec.TopicsEnabled = false
	rec.LoopTopics = nil
	if err := telegram.SavePairing(tgDir, rec); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	beforeCount := len(createdTopics)
	mu.Unlock()
	outBuf.Reset()
	errBuf.Reset()
	code = telegramCommand(env{ctx: context.Background(), stdout: &outBuf, stderr: &errBuf}, []string{
		"topics", "--sync", "--dir", tgDir, "--registry", regDir, "--api-url", srv.URL,
	})
	if code != exitOK || !strings.Contains(outBuf.String(), "single private DM") {
		t.Fatalf("expected clean single DM fallback, code=%d out=%q err=%q", code, outBuf.String(), errBuf.String())
	}
	mu.Lock()
	afterCount := len(createdTopics)
	mu.Unlock()
	if afterCount != beforeCount {
		t.Fatalf("expected zero createForumTopic calls when TopicsEnabled is false")
	}
}

func TestTelegramTopicSyncRefusesWhilePollerOwnsOffsets(t *testing.T) {
	dir := setupPairedTelegramDir(t, "123456789:EXAMPLE_TOPICS_LOCK_TOKEN", time.Now().UTC())
	rec, ok, err := telegram.LoadPairing(dir)
	if err != nil || !ok {
		t.Fatalf("pairing: %v", err)
	}
	rec.TopicsEnabled = true
	rec.LastUpdateOffset = 234
	if err := telegram.SavePairing(dir, rec); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, telegram.PairingFile))
	if err != nil {
		t.Fatal(err)
	}
	held, err := lock.AcquireNamed(dir, telegramPollLock)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	var out, errs bytes.Buffer
	e := env{ctx: context.Background(), stdout: &out, stderr: &errs}
	if code := telegramTopicsCommand(e, []string{"--dir", dir, "--sync"}); code != exitSkipped {
		t.Fatalf("sync while polling: code=%d stderr=%s", code, &errs)
	}
	after, err := os.ReadFile(filepath.Join(dir, telegram.PairingFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("sync changed pairing/offset: %v", err)
	}
	// Inspection remains available while polling and performs no writes.
	if code := telegramTopicsCommand(e, []string{"--dir", dir, "--json"}); code != exitOK {
		t.Fatalf("read-only topics blocked: code=%d stderr=%s", code, &errs)
	}
}
