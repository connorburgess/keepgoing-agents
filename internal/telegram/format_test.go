package telegram

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFormatAlert(t *testing.T) {
	at := time.Date(2026, 11, 20, 12, 34, 56, 0, time.UTC)
	got := FormatAlert(AlertSummary{
		Device:     "example-mac",
		Loop:       "example-loop\x00",
		Event:      "await_user",
		Summary:    "Waiting for person\r\nNeeds review\x07",
		Route:      "primary-claude",
		HoldReason: "no_progress_timeout",
		At:         at,
	})
	want := "[await_user] example-loop (example-mac)\nWaiting for person\nNeeds review\nroute: primary-claude | hold: no_progress_timeout | 2026-11-20T12:34:56Z"
	if got != want {
		t.Fatalf("FormatAlert:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestFormatAlertTruncatesToTelegramLimit(t *testing.T) {
	got := FormatAlert(AlertSummary{
		Loop:    "example-loop",
		Event:   "error",
		Summary: strings.Repeat("é", MaxMessageRunes+100),
	})
	if utf8.RuneCountInString(got) != MaxMessageRunes {
		t.Fatalf("rune count = %d, want %d", utf8.RuneCountInString(got), MaxMessageRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix")
	}
}
