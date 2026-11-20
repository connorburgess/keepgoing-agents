package telegram

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDeepLinkAndTopicLink(t *testing.T) {
	nonce, err := NewPairingNonce()
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != 24 {
		t.Fatalf("nonce len = %d, want 24", len(nonce))
	}
	link, err := DeepLink("@example_looptender_bot", nonce)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://t.me/example_looptender_bot?start=pair_" + nonce; link != want {
		t.Fatalf("DeepLink = %q, want %q", link, want)
	}
	if _, err := DeepLink("bad/name", nonce); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid username err = %v", err)
	}
	if _, err := DeepLink("example_looptender_bot", "short"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("short nonce err = %v", err)
	}

	topicURL, err := TopicLink("example_looptender_bot", 77)
	if err != nil || topicURL != "https://t.me/example_looptender_bot/77" {
		t.Fatalf("TopicLink = %q, %v", topicURL, err)
	}
	if _, err := TopicLink("example_looptender_bot", 0); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid thread err = %v", err)
	}
}

func TestMatchPairingUpdate(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Minute)
	nonce := "ABCDEFGHIJKLMNOPQRSTUVWX"

	mkUpdate := func(text, chatType string, isBot bool) Update {
		return Update{
			UpdateID: 12,
			Message: &Message{
				MessageID: 3,
				Date:      now.Unix(),
				From:      &User{ID: 7001, IsBot: isBot, Username: "example_owner"},
				Chat:      Chat{ID: 7001, Type: chatType},
				Text:      text,
			},
		}
	}

	cases := []struct {
		name      string
		u         Update
		now       time.Time
		wantMatch bool
	}{
		{name: "exact private start", u: mkUpdate("/start pair_"+nonce, "private", false), now: now, wantMatch: true},
		{name: "addressed start command", u: mkUpdate("/start@example_looptender_bot pair_"+nonce, "private", false), now: now, wantMatch: true},
		{name: "wrong nonce", u: mkUpdate("/start pair_WRONGWRONGWRONGWRONGWR", "private", false), now: now, wantMatch: false},
		{name: "expired", u: mkUpdate("/start pair_"+nonce, "private", false), now: expires.Add(time.Second), wantMatch: false},
		{name: "group chat rejected", u: mkUpdate("/start pair_"+nonce, "supergroup", false), now: now, wantMatch: false},
		{name: "bot sender rejected", u: mkUpdate("/start pair_"+nonce, "private", true), now: now, wantMatch: false},
		{name: "other command rejected", u: mkUpdate("/pair pair_"+nonce, "private", false), now: now, wantMatch: false},
		{name: "nil message rejected", u: Update{UpdateID: 12}, now: now, wantMatch: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := MatchPairingUpdate(tc.u, nonce, tc.now, expires)
			if ok != tc.wantMatch {
				t.Fatalf("ok = %t, want %t (match=%+v)", ok, tc.wantMatch, m)
			}
			if ok && (m.UserID != 7001 || m.ChatID != 7001 || m.UpdateID != 12) {
				t.Fatalf("unexpected match: %+v", m)
			}
		})
	}
}

func TestAuthorizeMessageAndCallback(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	rec := PairingRecord{
		SchemaVersion:    PairingSchemaVersion,
		Mode:             ModeAlerts,
		BotID:            100001,
		BotUsername:      "example_looptender_bot",
		AllowedUserID:    7001,
		AllowedChatID:    7001,
		LastUpdateOffset: 50,
		PairedAt:         now.Add(-time.Hour),
		UpdatedAt:        now.Add(-time.Minute),
	}

	goodMsg := Update{
		UpdateID: 51,
		Message: &Message{
			Date: now.Add(-30 * time.Second).Unix(),
			From: &User{ID: 7001},
			Chat: Chat{ID: 7001, Type: "private"},
			Text: "/status",
		},
	}
	if !AuthorizeMessage(rec, goodMsg, now, DefaultStalenessWindow) {
		t.Fatal("expected paired fresh message to authorize")
	}

	staleMsg := goodMsg
	staleMsg.Message = &Message{Date: now.Add(-20 * time.Minute).Unix(), From: &User{ID: 7001}, Chat: Chat{ID: 7001, Type: "private"}, Text: "/status"}
	if AuthorizeMessage(rec, staleMsg, now, DefaultStalenessWindow) {
		t.Fatal("expected stale message to be rejected")
	}

	replayMsg := goodMsg
	replayMsg.UpdateID = 50
	if AuthorizeMessage(rec, replayMsg, now, DefaultStalenessWindow) {
		t.Fatal("expected replayed update_id to be rejected")
	}

	strangerMsg := goodMsg
	strangerMsg.Message = &Message{Date: now.Unix(), From: &User{ID: 9999}, Chat: Chat{ID: 7001, Type: "private"}, Text: "/status"}
	if AuthorizeMessage(rec, strangerMsg, now, DefaultStalenessWindow) {
		t.Fatal("expected stranger user_id to be rejected")
	}

	goodCB := Update{
		UpdateID: 52,
		CallbackQuery: &CallbackQuery{
			ID:      "cb-1",
			From:    User{ID: 7001},
			Message: &Message{Chat: Chat{ID: 7001, Type: "private"}},
			Data:    "pause:example-loop",
		},
	}
	if !AuthorizeCallback(rec, goodCB) {
		t.Fatal("expected paired callback to authorize")
	}

	wrongChatCB := goodCB
	wrongChatCB.CallbackQuery = &CallbackQuery{ID: "cb-2", From: User{ID: 7001}, Message: &Message{Chat: Chat{ID: 8888, Type: "private"}}}
	if AuthorizeCallback(rec, wrongChatCB) {
		t.Fatal("expected mismatched chat callback to be rejected")
	}
}

func TestPairingPersistence(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := LoadPairing(dir); err != nil || ok {
		t.Fatalf("missing pairing: ok=%t err=%v", ok, err)
	}

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	rec := PairingRecord{
		SchemaVersion:    PairingSchemaVersion,
		Mode:             ModeAlerts,
		BotID:            100001,
		BotUsername:      "example_looptender_bot",
		AllowedUserID:    7001,
		AllowedChatID:    7001,
		TopicsEnabled:    true,
		LastUpdateOffset: 100,
		LoopTopics:       map[string]int64{"example-loop": 77},
		PairedAt:         now,
		UpdatedAt:        now,
	}
	if err := SavePairing(dir, rec); err != nil {
		t.Fatalf("SavePairing: %v", err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(dir, PairingFile))
		if err != nil {
			t.Fatal(err)
		}
		if mode := st.Mode().Perm(); mode != 0600 {
			t.Fatalf("pairing.json mode = %o, want 0600", mode)
		}
	}

	got, ok, err := LoadPairing(dir)
	if err != nil || !ok {
		t.Fatalf("LoadPairing: ok=%t err=%v", ok, err)
	}
	if got.AllowedUserID != 7001 || got.LoopTopics["example-loop"] != 77 || got.Mode != ModeAlerts {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Corrupt/unknown fields must fail closed.
	if err := os.WriteFile(filepath.Join(dir, PairingFile), []byte(`{"schemaVersion":1,"unknownField":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPairing(dir); !errors.Is(err, ErrPairingUnavailable) {
		t.Fatalf("corrupt load err = %v, want ErrPairingUnavailable", err)
	}

	if err := RemovePairing(dir); err != nil {
		t.Fatalf("RemovePairing: %v", err)
	}
	if _, ok, err := LoadPairing(dir); err != nil || ok {
		t.Fatalf("after remove: ok=%t err=%v", ok, err)
	}
}
