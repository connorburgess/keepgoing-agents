package telegram

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestControlCallbackCodec(t *testing.T) {
	const validID = "loop_0123456789abcdef0123456789abcdef"
	const validPrefix = "0123456789abcdef"

	cases := []struct {
		name    string
		in      ControlCallback
		want    string
		wantErr bool
	}{
		{
			name: "pause_with_digest_prefix",
			in:   ControlCallback{Action: ActionPause, RegistrationID: validID, ExpectPrefix: validPrefix},
			want: "p:" + validID + ":" + validPrefix,
		},
		{
			name: "stop_without_digest_prefix",
			in:   ControlCallback{Action: ActionStop, RegistrationID: validID},
			want: "s:" + validID,
		},
		{
			name:    "unsupported_resume_rejected",
			in:      ControlCallback{Action: "resume", RegistrationID: validID, ExpectPrefix: validPrefix},
			wantErr: true,
		},
		{
			name:    "invalid_registration_id",
			in:      ControlCallback{Action: ActionPause, RegistrationID: "loop_short"},
			wantErr: true,
		},
		{
			name:    "uppercase_id_rejected",
			in:      ControlCallback{Action: ActionPause, RegistrationID: "loop_0123456789ABCDEF0123456789abcdef"},
			wantErr: true,
		},
		{
			name:    "bad_digest_prefix_length",
			in:      ControlCallback{Action: ActionPause, RegistrationID: validID, ExpectPrefix: "01234567"},
			wantErr: true,
		},
		{
			name:    "non_hex_digest_prefix",
			in:      ControlCallback{Action: ActionPause, RegistrationID: validID, ExpectPrefix: "0123456789abcdeg"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeControlCallback(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if len(got) > MaxCallbackDataBytes {
				t.Fatalf("callback payload exceeds %d bytes: %d", MaxCallbackDataBytes, len(got))
			}
			decoded, ok := DecodeControlCallback(got)
			if !ok || decoded != tc.in {
				t.Fatalf("round-trip decode got (%+v, %t), want (%+v, true)", decoded, ok, tc.in)
			}
		})
	}

	for _, bad := range []string{
		"",
		"x:" + validID,
		"p:" + validID + ":bad",
		"p:" + validID + ":" + validPrefix + ":extra",
		strings.Repeat("a", MaxCallbackDataBytes+1),
	} {
		if _, ok := DecodeControlCallback(bad); ok {
			t.Fatalf("expected DecodeControlCallback(%q) to fail", bad)
		}
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		text    string
		bot     string
		wantCmd string
		wantArg string
		wantOK  bool
	}{
		{"/status", "example_bot", "status", "", true},
		{"  /STATUS@Example_Bot  extra arg ", "example_bot", "status", "extra arg", true},
		{"/help", "example_bot", "help", "", true},
		{"/status@other_bot", "example_bot", "", "", false},
		{"/status@example_bot", "", "", "", false},
		{"status", "example_bot", "", "", false},
		{"/", "example_bot", "", "", false},
		{"/bad-cmd", "example_bot", "", "", false},
	}
	for _, tc := range cases {
		cmd, arg, ok := ParseCommand(tc.text, tc.bot)
		if ok != tc.wantOK || cmd != tc.wantCmd || arg != tc.wantArg {
			t.Fatalf("ParseCommand(%q, %q) = (%q, %q, %t), want (%q, %q, %t)",
				tc.text, tc.bot, cmd, arg, ok, tc.wantCmd, tc.wantArg, tc.wantOK)
		}
	}
}

func TestAuthorizeCallbackAtAndStaleness(t *testing.T) {
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
		UpdatedAt:        now.Add(-time.Hour),
	}
	fresh := Update{
		UpdateID: 51,
		CallbackQuery: &CallbackQuery{
			ID:      "cb_fresh",
			From:    User{ID: 7001, IsBot: false},
			Message: &Message{MessageID: 10, Date: now.Add(-2 * time.Minute).Unix(), Chat: Chat{ID: 7001, Type: "private"}},
			Data:    "p:loop_0123456789abcdef0123456789abcdef",
		},
	}
	if !AuthorizeCallbackAt(rec, fresh, now, DefaultStalenessWindow) {
		t.Fatal("expected fresh owner callback to be authorized")
	}
	if CallbackStale(fresh, now, DefaultStalenessWindow) {
		t.Fatal("expected fresh callback not to be stale")
	}

	stale := fresh
	stale.CallbackQuery = &CallbackQuery{
		ID:      "cb_stale",
		From:    User{ID: 7001, IsBot: false},
		Message: &Message{MessageID: 10, Date: now.Add(-15 * time.Minute).Unix(), Chat: Chat{ID: 7001, Type: "private"}},
		Data:    fresh.CallbackQuery.Data,
	}
	if !AuthorizeCallback(rec, stale) {
		t.Fatal("expected stale owner callback to still pass identity check")
	}
	if !CallbackStale(stale, now, DefaultStalenessWindow) {
		t.Fatal("expected 15m old callback to be marked stale")
	}
	if AuthorizeCallbackAt(rec, stale, now, DefaultStalenessWindow) {
		t.Fatal("expected stale callback to be rejected by AuthorizeCallbackAt")
	}

	stranger := fresh
	stranger.CallbackQuery = &CallbackQuery{
		ID:      "cb_stranger",
		From:    User{ID: 9999, IsBot: false},
		Message: fresh.CallbackQuery.Message,
		Data:    fresh.CallbackQuery.Data,
	}
	if AuthorizeCallbackAt(rec, stranger, now, DefaultStalenessWindow) {
		t.Fatal("stranger callback must be rejected")
	}
}

func TestFormatStatusMessage(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	emptyText, emptyMarkup := FormatStatusMessage("example-host", nil, now)
	if !strings.Contains(emptyText, "No registered loops") || emptyMarkup != nil {
		t.Fatalf("empty status: text=%q markup=%+v", emptyText, emptyMarkup)
	}

	const idA = "loop_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const idB = "loop_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	text, markup := FormatStatusMessage("example-host", []LoopStatusItem{
		{
			RegistrationID: idA,
			Loop:           "alpha-loop\x00",
			Outcome:        "available",
			ControlState:   "enabled",
			Freshness:      "fresh",
			LastAction:     "wake",
			LastOutcome:    "sent",
			ExpectPrefix:   "0123456789abcdef",
			CanPause:       true,
			CanStop:        true,
		},
		{
			RegistrationID: idB,
			Loop:           "stopped-loop",
			Outcome:        "available",
			ControlState:   "stopped",
			Stop:           true,
			ExpectPrefix:   "fedcba9876543210",
			CanPause:       false,
			CanStop:        false,
		},
	}, now)
	if strings.Contains(text, "\x00") {
		t.Fatalf("status leaked control byte: %q", text)
	}
	if utf8.RuneCountInString(text) > MaxMessageRunes {
		t.Fatalf("status exceeded Telegram message limit")
	}
	if !strings.Contains(text, "alpha-loop: enabled") || !strings.Contains(text, "holds: stop") {
		t.Fatalf("unexpected status text:\n%s", text)
	}
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("expected one keyboard row with Pause and Stop for alpha-loop only, got %+v", markup)
	}
	cb0, ok := DecodeControlCallback(markup.InlineKeyboard[0][0].CallbackData)
	if !ok || cb0.Action != ActionPause || cb0.RegistrationID != idA || cb0.ExpectPrefix != "0123456789abcdef" {
		t.Fatalf("unexpected first button callback: %+v (%t)", cb0, ok)
	}
	cb1, ok := DecodeControlCallback(markup.InlineKeyboard[0][1].CallbackData)
	if !ok || cb1.Action != ActionStop || cb1.RegistrationID != idA {
		t.Fatalf("unexpected second button callback: %+v (%t)", cb1, ok)
	}
}
