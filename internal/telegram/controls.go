package telegram

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	// ActionPause is the restrictive pause callback action.
	ActionPause = "pause"
	// ActionStop is the restrictive stop callback action.
	ActionStop = "stop"
	// ActionPrepareResume mints a short-lived single-use resume ticket and sends a review summary.
	ActionPrepareResume = "prepare_resume"
	// ActionResume consumes a single-use prepared resume ticket bound to the reviewed state.
	ActionResume = "resume"

	// MaxCallbackDataBytes is Telegram's hard byte limit for inline button callback_data.
	MaxCallbackDataBytes = 64

	// ExpectDigestPrefixLen is the fixed hex prefix length (64 bits) carried in
	// compact Telegram control callbacks to detect concurrent control changes.
	ExpectDigestPrefixLen = 16

	maxStatusButtonLoops = 16
)

// ControlCallback identifies a remote inline-button control action bound to
// a registered loop and either an optional control-digest prefix (pause/stop/
// prepare_resume) or a single-use prepared resume ticket (resume).
type ControlCallback struct {
	Action         string
	RegistrationID string
	ExpectPrefix   string
	TicketID       string
}

// EncodeControlCallback encodes cb into a compact callback_data payload within
// Telegram's 64-byte limit:
//   - pause/stop/prepare_resume: "<p|s|pr>:<loop_<32hex>>[:<16hex>]" (<=57 bytes)
//   - resume: "r:<b64url(16-byte loop)>:<b64url(16-byte ticket)>" (47 bytes, lossless)
func EncodeControlCallback(cb ControlCallback) (string, error) {
	if !validRegistrationID(cb.RegistrationID) {
		return "", fmt.Errorf("%w: invalid registration id", ErrInvalidConfig)
	}
	if cb.Action == ActionResume {
		if cb.ExpectPrefix != "" {
			return "", fmt.Errorf("%w: resume callback does not carry digest prefix", ErrInvalidConfig)
		}
		if !validTicketID(cb.TicketID) {
			return "", fmt.Errorf("%w: invalid resume ticket id", ErrInvalidConfig)
		}
		loopRaw, err := hex.DecodeString(strings.TrimPrefix(cb.RegistrationID, "loop_"))
		if err != nil || len(loopRaw) != 16 {
			return "", fmt.Errorf("%w: invalid registration id", ErrInvalidConfig)
		}
		ticketRaw, err := hex.DecodeString(strings.TrimPrefix(cb.TicketID, "resume_"))
		if err != nil || len(ticketRaw) != 16 {
			return "", fmt.Errorf("%w: invalid resume ticket id", ErrInvalidConfig)
		}
		out := "r:" + base64.RawURLEncoding.EncodeToString(loopRaw) + ":" + base64.RawURLEncoding.EncodeToString(ticketRaw)
		if len(out) > MaxCallbackDataBytes {
			return "", fmt.Errorf("%w: callback payload exceeds %d bytes", ErrInvalidConfig, MaxCallbackDataBytes)
		}
		return out, nil
	}
	if cb.TicketID != "" {
		return "", fmt.Errorf("%w: ticket id is valid only for resume callbacks", ErrInvalidConfig)
	}
	var code string
	switch cb.Action {
	case ActionPause:
		code = "p"
	case ActionStop:
		code = "s"
	case ActionPrepareResume:
		code = "pr"
	default:
		return "", fmt.Errorf("%w: unsupported control action", ErrInvalidConfig)
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
	if parts[0] == "r" {
		if len(parts) != 3 || len(parts[1]) != 22 || len(parts[2]) != 22 {
			return ControlCallback{}, false
		}
		loopRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || len(loopRaw) != 16 || base64.RawURLEncoding.EncodeToString(loopRaw) != parts[1] {
			return ControlCallback{}, false
		}
		ticketRaw, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || len(ticketRaw) != 16 || base64.RawURLEncoding.EncodeToString(ticketRaw) != parts[2] {
			return ControlCallback{}, false
		}
		return ControlCallback{
			Action:         ActionResume,
			RegistrationID: "loop_" + hex.EncodeToString(loopRaw),
			TicketID:       "resume_" + hex.EncodeToString(ticketRaw),
		}, true
	}
	if len(parts) != 2 && len(parts) != 3 {
		return ControlCallback{}, false
	}
	var action string
	switch parts[0] {
	case "p":
		action = ActionPause
	case "s":
		action = ActionStop
	case "pr":
		action = ActionPrepareResume
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

func validTicketID(id string) bool {
	if !strings.HasPrefix(id, "resume_") || len(id) != 39 {
		return false
	}
	return isLowerHex(id[7:])
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
	CanResume      bool
}

// FormatStatusMessage renders a bounded, redacted /status summary and matching
// Pause/Stop/Prepare-resume inline keyboard buttons for registered loops.
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
		if item.CanResume {
			if data, err := EncodeControlCallback(ControlCallback{
				Action:         ActionPrepareResume,
				RegistrationID: item.RegistrationID,
				ExpectPrefix:   item.ExpectPrefix,
			}); err == nil {
				rows = append(rows, []InlineKeyboardButton{{
					Text:         "Prepare resume " + label,
					CallbackData: data,
				}})
			}
		}
	}

	var markup *InlineKeyboardMarkup
	if len(rows) > 0 {
		markup = &InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	return TruncateRunes(strings.Join(lines, "\n"), MaxMessageRunes), markup
}

// ResumeReview holds strictly redacted metadata for a prepared single-use
// Telegram resume confirmation message. Never includes prompts, notes, paths,
// or secrets.
type ResumeReview struct {
	Device         string
	Loop           string
	RegistrationID string
	TicketID       string
	ExpiresAt      time.Time
	ControlState   string
	Paused         bool
	Stop           bool
	AwaitingUser   bool
	Completed      bool
	Freshness      string
	LastAction     string
	LastOutcome    string
}

// FormatResumeReviewMessage renders a redacted review summary for a prepared
// single-use resume ticket and its Confirm resume inline button.
func FormatResumeReviewMessage(r ResumeReview) (string, *InlineKeyboardMarkup, error) {
	data, err := EncodeControlCallback(ControlCallback{
		Action:         ActionResume,
		RegistrationID: r.RegistrationID,
		TicketID:       r.TicketID,
	})
	if err != nil {
		return "", nil, err
	}
	name := sanitizeInline(r.Loop)
	if name == "" {
		name = "loop"
	}
	header := fmt.Sprintf("[looptender] resume review — %s", name)
	if dev := sanitizeInline(r.Device); dev != "" {
		header += " (" + dev + ")"
	}
	var lines []string
	lines = append(lines, header)
	if st := sanitizeInline(r.ControlState); st != "" {
		lines = append(lines, "State: "+st)
	}
	var holds []string
	if r.Paused {
		holds = append(holds, "paused")
	}
	if r.Stop {
		holds = append(holds, "stop")
	}
	if r.AwaitingUser {
		holds = append(holds, "awaiting-user")
	}
	if r.Completed {
		holds = append(holds, "completed")
	}
	if len(holds) > 0 {
		lines = append(lines, "Holds to clear: "+strings.Join(holds, ", "))
	} else {
		lines = append(lines, "Holds to clear: none")
	}
	if f := sanitizeInline(r.Freshness); f != "" {
		lines = append(lines, "Check freshness: "+f)
	}
	if act := sanitizeInline(r.LastAction); act != "" {
		last := act
		if out := sanitizeInline(r.LastOutcome); out != "" {
			last += "/" + out
		}
		lines = append(lines, "Last check: "+last)
	}
	if !r.ExpiresAt.IsZero() {
		lines = append(lines, "Single-use ticket expires: "+r.ExpiresAt.UTC().Format(time.RFC3339))
	}
	lines = append(lines, "Confirming clears these holds on this device. Loop-wide limits are unchanged; supervision is not enabled or started.")

	label := TruncateRunes(name, 20)
	markup := &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{{{
			Text:         "Confirm resume " + label,
			CallbackData: data,
		}}},
	}
	return TruncateRunes(strings.Join(lines, "\n"), MaxMessageRunes), markup, nil
}

// FormatHelpMessage renders concise, redacted command help for the paired user.
func FormatHelpMessage(mode string) string {
	m := sanitizeInline(mode)
	if m == "" {
		m = ModeAlerts
	}
	return strings.Join([]string{
		fmt.Sprintf("looptender remote controls (mode: %s)", m),
		"/status — Show redacted status and Pause/Stop/Prepare resume buttons for registered loops",
		"/resume — Explain how to review and confirm a single-use resume ticket",
		"/help — Show this message",
		"Note: Pause and Stop apply restrictive holds on this device. Resume uses a short-lived single-use ticket bound to the reviewed state.",
	}, "\n")
}
