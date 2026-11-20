package telegram

import (
	"fmt"
	"strings"
	"time"
)

const (
	// ActionPause is the restrictive pause callback action.
	ActionPause = "pause"
	// ActionStop is the restrictive stop callback action.
	ActionStop = "stop"

	// MaxCallbackDataBytes is Telegram's hard byte limit for inline button callback_data.
	MaxCallbackDataBytes = 64

	// ExpectDigestPrefixLen is the fixed hex prefix length (64 bits) carried in
	// compact Telegram control callbacks to detect concurrent control changes.
	ExpectDigestPrefixLen = 16

	maxStatusButtonLoops = 16
)

// ControlCallback identifies a restrictive inline-button hold action bound to
// a registered loop and an optional control-digest prefix.
type ControlCallback struct {
	Action         string
	RegistrationID string
	ExpectPrefix   string
}

// EncodeControlCallback encodes cb into a compact callback_data payload within
// Telegram's 64-byte limit: "<p|s>:<loop_<32hex>>[:<16hex>]".
func EncodeControlCallback(cb ControlCallback) (string, error) {
	var code string
	switch cb.Action {
	case ActionPause:
		code = "p"
	case ActionStop:
		code = "s"
	default:
		return "", fmt.Errorf("%w: unsupported control action", ErrInvalidConfig)
	}
	if !validRegistrationID(cb.RegistrationID) {
		return "", fmt.Errorf("%w: invalid registration id", ErrInvalidConfig)
	}
	if cb.ExpectPrefix != "" && !validHexPrefix(cb.ExpectPrefix) {
		return "", fmt.Errorf("%w: invalid control digest prefix", ErrInvalidConfig)
	}
	out := code + ":" + cb.RegistrationID
	if cb.ExpectPrefix != "" {
		out += ":" + cb.ExpectPrefix
	}
	if len(out) > MaxCallbackDataBytes {
		return "", fmt.Errorf("%w: callback payload exceeds %d bytes", ErrInvalidConfig, MaxCallbackDataBytes)
	}
	return out, nil
}

// DecodeControlCallback parses and strictly validates callback_data produced by
// EncodeControlCallback. Pure function.
func DecodeControlCallback(data string) (ControlCallback, bool) {
	if len(data) == 0 || len(data) > MaxCallbackDataBytes {
		return ControlCallback{}, false
	}
	parts := strings.Split(data, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return ControlCallback{}, false
	}
	var action string
	switch parts[0] {
	case "p":
		action = ActionPause
	case "s":
		action = ActionStop
	default:
		return ControlCallback{}, false
	}
	if !validRegistrationID(parts[1]) {
		return ControlCallback{}, false
	}
	var prefix string
	if len(parts) == 3 {
		if !validHexPrefix(parts[2]) {
			return ControlCallback{}, false
		}
		prefix = parts[2]
	}
	return ControlCallback{
		Action:         action,
		RegistrationID: parts[1],
		ExpectPrefix:   prefix,
	}, true
}

func validRegistrationID(id string) bool {
	if !strings.HasPrefix(id, "loop_") || len(id) != 37 {
		return false
	}
	return isLowerHex(id[5:])
}

func validHexPrefix(p string) bool {
	return len(p) == ExpectDigestPrefixLen && isLowerHex(p)
}

func isLowerHex(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ParseCommand parses a Telegram slash command from message text, returning the
// lowercase command name without the leading slash and any trailing arguments.
// Commands addressed to another bot (@other_bot) are rejected.
func ParseCommand(text, botUsername string) (cmd, args string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	first := trimmed
	if idx := strings.IndexAny(trimmed, " \t\r\n"); idx >= 0 {
		first = trimmed[:idx]
		args = strings.TrimSpace(trimmed[idx:])
	}
	raw := strings.TrimPrefix(first, "/")
	if raw == "" {
		return "", "", false
	}
	name, target, hasTarget := strings.Cut(raw, "@")
	if hasTarget {
		want := strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
		if want == "" || !strings.EqualFold(target, want) {
			return "", "", false
		}
	}
	name = strings.ToLower(name)
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return "", "", false
		}
	}
	if name == "" {
		return "", "", false
	}
	return name, args, true
}

// CallbackStale reports whether a callback query's underlying message timestamp
// is missing or older than maxAge relative to now.
func CallbackStale(u Update, now time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	if u.CallbackQuery == nil || u.CallbackQuery.Message == nil || u.CallbackQuery.Message.Date <= 0 {
		return true
	}
	sentAt := time.Unix(u.CallbackQuery.Message.Date, 0).UTC()
	return now.UTC().Sub(sentAt) > maxAge
}

// AuthorizeCallbackAt reports whether an inline callback comes from the paired
// user/chat, is newer than rec.LastUpdateOffset, and is within maxAge.
func AuthorizeCallbackAt(rec PairingRecord, u Update, now time.Time, maxAge time.Duration) bool {
	return AuthorizeCallback(rec, u) && !CallbackStale(u, now, maxAge)
}

// LoopStatusItem holds strictly redacted supervision metadata for one
// registered loop in a Telegram /status reply (never paths, prompts, notes, or secrets).
type LoopStatusItem struct {
	RegistrationID string
	Loop           string
	Outcome        string
	ControlState   string
	Paused         bool
	Stop           bool
	AwaitingUser   bool
	Completed      bool
	Freshness      string
	LastAction     string
	LastOutcome    string
	ExpectPrefix   string
	CanPause       bool
	CanStop        bool
}

// FormatStatusMessage renders a bounded, redacted /status summary and matching
// restrictive Pause/Stop inline keyboard buttons for registered loops.
func FormatStatusMessage(device string, items []LoopStatusItem, now time.Time) (string, *InlineKeyboardMarkup) {
	dev := sanitizeInline(device)
	var header string
	if dev != "" {
		header = fmt.Sprintf("looptender status (%s) — %d registered loop(s)", dev, len(items))
	} else {
		header = fmt.Sprintf("looptender status — %d registered loop(s)", len(items))
	}
	if !now.IsZero() {
		header += " | " + now.UTC().Format(time.RFC3339)
	}
	if len(items) == 0 {
		return TruncateRunes(header+"\nNo registered loops on this device.", MaxMessageRunes), nil
	}

	var lines []string
	lines = append(lines, header)
	var rows [][]InlineKeyboardButton

	for i, item := range items {
		name := sanitizeInline(item.Loop)
		if name == "" {
			name = "loop"
		}
		state := sanitizeInline(item.ControlState)
		if state == "" {
			state = sanitizeInline(item.Outcome)
		}
		if state == "" {
			state = "unknown"
		}

		var details []string
		var holds []string
		if item.Paused {
			holds = append(holds, "paused")
		}
		if item.Stop {
			holds = append(holds, "stop")
		}
		if item.AwaitingUser {
			holds = append(holds, "awaiting-user")
		}
		if item.Completed {
			holds = append(holds, "completed")
		}
		if len(holds) > 0 {
			details = append(details, "holds: "+strings.Join(holds, ","))
		}
		if f := sanitizeInline(item.Freshness); f != "" {
			details = append(details, "check: "+f)
		}
		if act := sanitizeInline(item.LastAction); act != "" {
			last := act
			if out := sanitizeInline(item.LastOutcome); out != "" {
				last += "/" + out
			}
			details = append(details, "last: "+last)
		}
		line := fmt.Sprintf("- %s: %s", name, state)
		if len(details) > 0 {
			line += " (" + strings.Join(details, "; ") + ")"
		}
		lines = append(lines, line)

		if i >= maxStatusButtonLoops {
			continue
		}
		var row []InlineKeyboardButton
		label := TruncateRunes(name, 20)
		if item.CanPause {
			if data, err := EncodeControlCallback(ControlCallback{
				Action:         ActionPause,
				RegistrationID: item.RegistrationID,
				ExpectPrefix:   item.ExpectPrefix,
			}); err == nil {
				row = append(row, InlineKeyboardButton{
					Text:         "Pause " + label,
					CallbackData: data,
				})
			}
		}
		if item.CanStop {
			if data, err := EncodeControlCallback(ControlCallback{
				Action:         ActionStop,
				RegistrationID: item.RegistrationID,
				ExpectPrefix:   item.ExpectPrefix,
			}); err == nil {
				row = append(row, InlineKeyboardButton{
					Text:         "Stop " + label,
					CallbackData: data,
				})
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}

	var markup *InlineKeyboardMarkup
	if len(rows) > 0 {
		markup = &InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	return TruncateRunes(strings.Join(lines, "\n"), MaxMessageRunes), markup
}

// FormatHelpMessage renders concise, redacted command help for the paired user.
func FormatHelpMessage(mode string) string {
	m := sanitizeInline(mode)
	if m == "" {
		m = ModeAlerts
	}
	return strings.Join([]string{
		fmt.Sprintf("looptender remote controls (mode: %s)", m),
		"/status — Show redacted status and Pause/Stop buttons for registered loops",
		"/help — Show this message",
		"Note: Pause and Stop apply restrictive holds on this device. Resume requires a local or ticketed flow.",
	}, "\n")
}
