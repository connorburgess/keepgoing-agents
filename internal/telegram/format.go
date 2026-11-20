package telegram

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// AlertSummary contains only redacted, structured supervision metadata suitable
// for alerts mode (no transcripts, prompts, diffs, credentials, or raw paths).
type AlertSummary struct {
	Device     string
	Loop       string
	Event      string
	Summary    string
	Route      string
	HoldReason string
	At         time.Time
}

// FormatAlert renders a concise, plain-text Telegram alert bounded to
// MaxMessageRunes. Control characters other than newline are stripped.
func FormatAlert(a AlertSummary) string {
	loop := sanitizeInline(a.Loop)
	if loop == "" {
		loop = "loop"
	}
	event := sanitizeInline(a.Event)
	if event == "" {
		event = "update"
	}
	header := fmt.Sprintf("[%s] %s", event, loop)
	if dev := sanitizeInline(a.Device); dev != "" {
		header += " (" + dev + ")"
	}
	var lines []string
	lines = append(lines, header)
	if summary := sanitizeMultiline(a.Summary); summary != "" {
		lines = append(lines, summary)
	}
	var meta []string
	if route := sanitizeInline(a.Route); route != "" {
		meta = append(meta, "route: "+route)
	}
	if hold := sanitizeInline(a.HoldReason); hold != "" {
		meta = append(meta, "hold: "+hold)
	}
	if !a.At.IsZero() {
		meta = append(meta, a.At.UTC().Format(time.RFC3339))
	}
	if len(meta) > 0 {
		lines = append(lines, strings.Join(meta, " | "))
	}
	return TruncateRunes(strings.Join(lines, "\n"), MaxMessageRunes)
}

// TruncateRunes limits s to at most maxRunes Unicode code points, appending an
// ellipsis when truncated.
func TruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(r[:maxRunes-1]) + "…"
}

func sanitizeInline(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func sanitizeMultiline(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r == '\r' {
			continue
		}
		if r == '\n' || r == '\t' {
			b.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
