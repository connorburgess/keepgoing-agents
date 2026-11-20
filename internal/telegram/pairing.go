package telegram

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

const (
	// ModeAlerts sends redacted summaries and permits /status, pause, stop, and ticketed resume.
	ModeAlerts = "alerts"
	// ModeChat opts in to threaded steering replies and bounded turn summaries.
	ModeChat = "chat"

	// PairingFile stores non-secret Telegram pairing state inside the private state directory.
	// The bot token is never written to this file.
	PairingFile = "pairing.json"

	// PairingSchemaVersion is the current persisted schema version.
	PairingSchemaVersion = 1

	// MaxPairingBytes caps pairing.json reads.
	MaxPairingBytes = 64 * 1024

	// DefaultStalenessWindow rejects queued inbound commands older than this window.
	DefaultStalenessWindow = 10 * time.Minute

	pairPrefix = "pair_"
)

// ErrPairingUnavailable never embeds paths, tokens, or raw OS errors.
var ErrPairingUnavailable = errors.New("telegram: pairing record unavailable; check private state storage")

// NewPairingNonce returns a cryptographically random URL-safe pairing nonce.
func NewPairingNonce() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("telegram: failed to generate pairing nonce")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DeepLink builds the one-tap Telegram start link for local pairing.
func DeepLink(botUsername, nonce string) (string, error) {
	u := strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if !validUsername(u) {
		return "", fmt.Errorf("%w: invalid bot username", ErrInvalidConfig)
	}
	if !validNonce(nonce) {
		return "", fmt.Errorf("%w: invalid pairing nonce", ErrInvalidConfig)
	}
	return "https://t.me/" + u + "?start=" + pairPrefix + nonce, nil
}

// TopicLink builds a Bot API 10.4 private-chat topic link.
func TopicLink(botUsername string, threadID int64) (string, error) {
	u := strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if !validUsername(u) || threadID <= 0 {
		return "", fmt.Errorf("%w: invalid bot username or topic thread id", ErrInvalidConfig)
	}
	return fmt.Sprintf("https://t.me/%s/%d", u, threadID), nil
}

func validUsername(u string) bool {
	if len(u) < 5 || len(u) > 32 {
		return false
	}
	for i := 0; i < len(u); i++ {
		c := u[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

func validNonce(n string) bool {
	if len(n) < 16 || len(n) > 48 {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// PairingMatch holds the verified Telegram identity captured from a one-time /start deep link.
type PairingMatch struct {
	UserID   int64
	ChatID   int64
	Username string
	UpdateID int64
}

// MatchPairingUpdate checks whether u is a valid one-time pairing message in a
// private chat from a non-bot Telegram user before expiresAt. Pure function.
func MatchPairingUpdate(u Update, expectedNonce string, now, expiresAt time.Time) (PairingMatch, bool) {
	if !validNonce(expectedNonce) || now.After(expiresAt) {
		return PairingMatch{}, false
	}
	msg := u.Message
	if msg == nil || msg.From == nil || msg.From.IsBot {
		return PairingMatch{}, false
	}
	if msg.Chat.Type != "private" || msg.Chat.ID <= 0 || msg.From.ID <= 0 {
		return PairingMatch{}, false
	}
	fields := strings.Fields(strings.TrimSpace(msg.Text))
	if len(fields) != 2 {
		return PairingMatch{}, false
	}
	cmd := fields[0]
	if cmd != "/start" && !strings.HasPrefix(cmd, "/start@") {
		return PairingMatch{}, false
	}
	want := pairPrefix + expectedNonce
	if subtle.ConstantTimeCompare([]byte(fields[1]), []byte(want)) != 1 {
		return PairingMatch{}, false
	}
	return PairingMatch{
		UserID:   msg.From.ID,
		ChatID:   msg.Chat.ID,
		Username: msg.From.Username,
		UpdateID: u.UpdateID,
	}, true
}

// PairingRecord stores non-secret local pairing metadata and the persisted
// update offset. The bot token always lives in a separate 0600 secret file.
type PairingRecord struct {
	SchemaVersion    int              `json:"schemaVersion"`
	Mode             string           `json:"mode"`
	BotID            int64            `json:"botId"`
	BotUsername      string           `json:"botUsername"`
	AllowedUserID    int64            `json:"allowedUserId"`
	AllowedChatID    int64            `json:"allowedChatId"`
	TopicsEnabled    bool             `json:"topicsEnabled"`
	LastUpdateOffset int64            `json:"lastUpdateOffset"`
	LoopTopics       map[string]int64 `json:"loopTopics,omitempty"`
	PairedAt         time.Time        `json:"pairedAt"`
	UpdatedAt        time.Time        `json:"updatedAt"`
}

// Validate checks every invariant of a PairingRecord.
func (r PairingRecord) Validate() error {
	if r.SchemaVersion != PairingSchemaVersion {
		return ErrPairingUnavailable
	}
	if r.Mode != ModeAlerts && r.Mode != ModeChat {
		return ErrPairingUnavailable
	}
	if r.BotID <= 0 || !validUsername(r.BotUsername) {
		return ErrPairingUnavailable
	}
	if r.AllowedUserID <= 0 || r.AllowedChatID == 0 {
		return ErrPairingUnavailable
	}
	if r.LastUpdateOffset < 0 {
		return ErrPairingUnavailable
	}
	if r.PairedAt.IsZero() || r.UpdatedAt.IsZero() {
		return ErrPairingUnavailable
	}
	for loopKey, threadID := range r.LoopTopics {
		if strings.TrimSpace(loopKey) == "" || threadID <= 0 {
			return ErrPairingUnavailable
		}
	}
	return nil
}

// AuthorizeMessage reports whether an inbound message update comes from the
// paired user and chat, is newer than the persisted offset, and is within maxAge.
func AuthorizeMessage(rec PairingRecord, u Update, now time.Time, maxAge time.Duration) bool {
	if rec.Validate() != nil || u.Message == nil || u.Message.From == nil {
		return false
	}
	if u.UpdateID <= rec.LastUpdateOffset {
		return false
	}
	if u.Message.From.IsBot {
		return false
	}
	if u.Message.From.ID != rec.AllowedUserID || u.Message.Chat.ID != rec.AllowedChatID {
		return false
	}
	if maxAge > 0 {
		if u.Message.Date <= 0 {
			return false
		}
		sentAt := time.Unix(u.Message.Date, 0).UTC()
		if now.UTC().Sub(sentAt) > maxAge {
			return false
		}
	}
	return true
}

// AuthorizeCallback reports whether an inline keyboard callback comes from the
// paired user and chat and is newer than the persisted offset.
func AuthorizeCallback(rec PairingRecord, u Update) bool {
	if rec.Validate() != nil || u.CallbackQuery == nil {
		return false
	}
	if u.UpdateID <= rec.LastUpdateOffset {
		return false
	}
	cb := u.CallbackQuery
	if cb.From.IsBot || cb.From.ID != rec.AllowedUserID {
		return false
	}
	if cb.Message == nil || cb.Message.Chat.ID != rec.AllowedChatID {
		return false
	}
	return true
}

// LoadPairing reads pairing.json strictly and read-only. If no pairing exists,
// it returns (PairingRecord{}, false, nil). Corrupt files fail closed.
func LoadPairing(dir string) (PairingRecord, bool, error) {
	if !filepath.IsAbs(dir) {
		return PairingRecord{}, false, ErrPairingUnavailable
	}
	path := filepath.Join(dir, PairingFile)
	info, err := snapshot.LstatRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return PairingRecord{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxPairingBytes {
		return PairingRecord{}, false, ErrPairingUnavailable
	}
	f, err := snapshot.OpenRecord(path)
	if err != nil {
		return PairingRecord{}, false, ErrPairingUnavailable
	}
	defer f.Close()
	opened, statErr := f.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || opened.Size() > MaxPairingBytes || !os.SameFile(info, opened) {
		return PairingRecord{}, false, ErrPairingUnavailable
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxPairingBytes+1))
	if err != nil || len(b) > MaxPairingBytes {
		return PairingRecord{}, false, ErrPairingUnavailable
	}
	var rec PairingRecord
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&rec) != nil || dec.Decode(new(any)) != io.EOF {
		return PairingRecord{}, false, ErrPairingUnavailable
	}
	if rec.Validate() != nil {
		return PairingRecord{}, false, ErrPairingUnavailable
	}
	return rec, true, nil
}

// SavePairing atomically persists pairing.json with 0600 permissions under a
// named lock. Callers must never hold this lock across network calls.
func SavePairing(dir string, rec PairingRecord) error {
	if !filepath.IsAbs(dir) || rec.Validate() != nil {
		return ErrPairingUnavailable
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ErrPairingUnavailable
	}
	l, err := lock.AcquireNamed(dir, "telegram")
	if err != nil {
		return ErrPairingUnavailable
	}
	defer l.Release()

	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil || len(b)+1 > MaxPairingBytes {
		return ErrPairingUnavailable
	}
	f, err := os.CreateTemp(dir, ".pairing-*")
	if err != nil {
		return ErrPairingUnavailable
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return ErrPairingUnavailable
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return ErrPairingUnavailable
	}
	if err := snapshot.Sync(f); err != nil {
		f.Close()
		return ErrPairingUnavailable
	}
	if err := f.Close(); err != nil {
		return ErrPairingUnavailable
	}
	if err := snapshot.Replace(tmp, filepath.Join(dir, PairingFile)); err != nil {
		return ErrPairingUnavailable
	}
	return nil
}

// RemovePairing deletes pairing.json and its snapshot marker under the lock.
func RemovePairing(dir string) error {
	if !filepath.IsAbs(dir) {
		return ErrPairingUnavailable
	}
	l, err := lock.AcquireNamed(dir, "telegram")
	if err != nil {
		return ErrPairingUnavailable
	}
	defer l.Release()
	if err := snapshot.Remove(filepath.Join(dir, PairingFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrPairingUnavailable
	}
	return nil
}
