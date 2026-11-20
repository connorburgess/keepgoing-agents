// Package telegram provides an outbound-only Telegram Bot API client,
// one-time deep-link pairing primitives, atomic private pairing state, and
// redacted alert formatting. It uses only the Go standard library and never
// embeds bot tokens in errors or logs.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the official Telegram Bot API origin.
	DefaultBaseURL = "https://api.telegram.org"

	// MaxResponseBytes caps any Telegram Bot API response body read into memory.
	MaxResponseBytes = 1 << 20

	// MaxMessageRunes is Telegram's per-message text limit.
	MaxMessageRunes = 4096

	// DefaultTimeout is used when a ClientConfig omits Timeout.
	DefaultTimeout = 15 * time.Second
)

var (
	// ErrInvalidConfig reports malformed client configuration without echoing tokens.
	ErrInvalidConfig = errors.New("telegram: invalid configuration")
	// ErrTransport reports an HTTP transport failure without echoing URLs or tokens.
	ErrTransport = errors.New("telegram: transport request failed")
	// ErrProtocol reports a redirect, oversized body, or malformed API envelope.
	ErrProtocol = errors.New("telegram: unexpected API response")
)

// APIError is a structured Telegram Bot API rejection. It deliberately omits
// request URLs, bot tokens, and arbitrary server prose from its error string.
type APIError struct {
	Method     string
	HTTPStatus int
	ErrorCode  int
	RetryAfter int
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram: %s rejected (http %d, code %d, retry after %ds)", e.Method, e.HTTPStatus, e.ErrorCode, e.RetryAfter)
	}
	return fmt.Sprintf("telegram: %s rejected (http %d, code %d)", e.Method, e.HTTPStatus, e.ErrorCode)
}

// ClientConfig configures a Telegram Bot API client.
type ClientConfig struct {
	Token      string
	BaseURL    string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Client calls the Telegram Bot API over outbound HTTPS without following redirects.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// NewClient validates token and baseURL and returns an outbound-only Client.
// Plain HTTP is allowed only for loopback addresses (for offline test stand-ins).
func NewClient(cfg ClientConfig) (*Client, error) {
	if err := validateToken(cfg.Token); err != nil {
		return nil, err
	}
	rawBase := strings.TrimSpace(cfg.BaseURL)
	if rawBase == "" {
		rawBase = DefaultBaseURL
	}
	u, err := url.Parse(rawBase)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: base URL must be an origin without credentials, query, or fragment", ErrInvalidConfig)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("%w: base URL must not include a path", ErrInvalidConfig)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("%w: base URL must use https (http allowed only for loopback)", ErrInvalidConfig)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	var hc http.Client
	if cfg.HTTPClient != nil {
		hc = *cfg.HTTPClient
	}
	if hc.Timeout <= 0 {
		hc.Timeout = timeout
	}
	// Never follow redirects: the bot token lives in the request URL path.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	cleanBase := u.Scheme + "://" + u.Host
	return &Client{
		token:   cfg.Token,
		baseURL: cleanBase,
		http:    &hc,
	}, nil
}

func validateToken(token string) error {
	if token == "" {
		return fmt.Errorf("%w: bot token is required", ErrInvalidConfig)
	}
	if strings.ContainsAny(token, " \t\r\n/?#\\") {
		return fmt.Errorf("%w: bot token contains disallowed characters", ErrInvalidConfig)
	}
	id, secret, ok := strings.Cut(token, ":")
	if !ok || id == "" || secret == "" {
		return fmt.Errorf("%w: bot token must have <id>:<secret> form", ErrInvalidConfig)
	}
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return fmt.Errorf("%w: bot token id prefix must be numeric", ErrInvalidConfig)
		}
	}
	return nil
}

func isLoopbackHost(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// BotInfo is the subset of Telegram's User object returned by getMe.
type BotInfo struct {
	ID                        int64  `json:"id"`
	IsBot                     bool   `json:"is_bot"`
	FirstName                 string `json:"first_name"`
	Username                  string `json:"username"`
	HasTopicsEnabled          bool   `json:"has_topics_enabled,omitempty"`
	AllowsUsersToCreateTopics bool   `json:"allows_users_to_create_topics,omitempty"`
	CanManageBots             bool   `json:"can_manage_bots,omitempty"`
}

// User is a Telegram sender.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Chat is a Telegram chat container.
type Chat struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	IsForum bool   `json:"is_forum,omitempty"`
}

// Message is the subset of a Telegram Message needed for pairing and controls.
type Message struct {
	MessageID       int64  `json:"message_id"`
	MessageThreadID int64  `json:"message_thread_id,omitempty"`
	Date            int64  `json:"date"`
	From            *User  `json:"from,omitempty"`
	Chat            Chat   `json:"chat"`
	Text            string `json:"text,omitempty"`
}

// CallbackQuery is an inline keyboard button press.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// Update is one item returned by getUpdates.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// InlineKeyboardButton is one button in an inline keyboard row.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

// InlineKeyboardMarkup renders interactive buttons beneath a message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// LinkPreviewOptions controls URL previews on outbound messages.
type LinkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

// SendMessageRequest sends a plain text message to a paired chat or private-chat topic.
type SendMessageRequest struct {
	ChatID             int64                 `json:"chat_id"`
	MessageThreadID    int64                 `json:"message_thread_id,omitempty"`
	Text               string                `json:"text"`
	LinkPreviewOptions *LinkPreviewOptions   `json:"link_preview_options,omitempty"`
	ReplyMarkup        *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// SentMessage is the subset of Message returned by sendMessage.
type SentMessage struct {
	MessageID       int64 `json:"message_id"`
	MessageThreadID int64 `json:"message_thread_id,omitempty"`
	Date            int64 `json:"date"`
}

// GetUpdatesRequest configures outbound long polling.
type GetUpdatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	TimeoutSeconds int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

// AnswerCallbackQueryRequest acknowledges an inline button tap.
type AnswerCallbackQueryRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
}

// BotCommand registers one slash command in Telegram's command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// ForumTopic is returned by createForumTopic (including Bot API 9.4 private-chat topics).
type ForumTopic struct {
	MessageThreadID int64  `json:"message_thread_id"`
	Name            string `json:"name"`
}

type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// GetMe validates the bot token and inspects bot capabilities.
func (c *Client) GetMe(ctx context.Context) (BotInfo, error) {
	var info BotInfo
	if err := c.call(ctx, "getMe", nil, &info); err != nil {
		return BotInfo{}, err
	}
	if info.ID <= 0 || !info.IsBot {
		return BotInfo{}, fmt.Errorf("%w: getMe returned invalid bot identity", ErrProtocol)
	}
	return info, nil
}

// SendMessage sends a bounded text message to chat_id (and optional topic thread).
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) (SentMessage, error) {
	if req.ChatID == 0 || strings.TrimSpace(req.Text) == "" {
		return SentMessage{}, fmt.Errorf("%w: chat_id and non-empty text are required", ErrInvalidConfig)
	}
	req.Text = TruncateRunes(req.Text, MaxMessageRunes)
	if req.LinkPreviewOptions == nil {
		req.LinkPreviewOptions = &LinkPreviewOptions{IsDisabled: true}
	}
	var out SentMessage
	if err := c.call(ctx, "sendMessage", req, &out); err != nil {
		return SentMessage{}, err
	}
	return out, nil
}

// GetUpdates pulls updates over outbound HTTPS long polling.
func (c *Client) GetUpdates(ctx context.Context, req GetUpdatesRequest) ([]Update, error) {
	if req.TimeoutSeconds < 0 {
		return nil, fmt.Errorf("%w: negative poll timeout", ErrInvalidConfig)
	}
	if len(req.AllowedUpdates) == 0 {
		req.AllowedUpdates = []string{"message", "callback_query"}
	}
	var updates []Update
	if err := c.call(ctx, "getUpdates", req, &updates); err != nil {
		return nil, err
	}
	if updates == nil {
		updates = []Update{}
	}
	return updates, nil
}

// AnswerCallbackQuery clears the loading indicator on an inline keyboard button.
func (c *Client) AnswerCallbackQuery(ctx context.Context, req AnswerCallbackQueryRequest) error {
	if strings.TrimSpace(req.CallbackQueryID) == "" {
		return fmt.Errorf("%w: callback_query_id is required", ErrInvalidConfig)
	}
	var ok bool
	return c.call(ctx, "answerCallbackQuery", req, &ok)
}

// SetMyCommands sets the bot's slash-command menu.
func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	payload := struct {
		Commands []BotCommand `json:"commands"`
	}{Commands: commands}
	var ok bool
	return c.call(ctx, "setMyCommands", payload, &ok)
}

// CreateForumTopic creates a topic in a forum-enabled chat or private bot chat.
func (c *Client) CreateForumTopic(ctx context.Context, chatID int64, name string) (ForumTopic, error) {
	name = strings.TrimSpace(TruncateRunes(name, 128))
	if chatID == 0 || name == "" {
		return ForumTopic{}, fmt.Errorf("%w: chat_id and topic name are required", ErrInvalidConfig)
	}
	payload := struct {
		ChatID int64  `json:"chat_id"`
		Name   string `json:"name"`
	}{ChatID: chatID, Name: name}
	var topic ForumTopic
	if err := c.call(ctx, "createForumTopic", payload, &topic); err != nil {
		return ForumTopic{}, err
	}
	if topic.MessageThreadID <= 0 {
		return ForumTopic{}, fmt.Errorf("%w: createForumTopic returned invalid thread id", ErrProtocol)
	}
	return topic, nil
}

func (c *Client) call(ctx context.Context, method string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("%w: encode %s request", ErrProtocol, method)
		}
		body = bytes.NewReader(b)
	}
	endpoint := c.baseURL + "/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		// Never return the raw error from NewRequestWithContext: it can contain the URL/token.
		return fmt.Errorf("%w: build %s request", ErrTransport, method)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Preserve context cancellation/deadline while stripping url.Error (which embeds the token path).
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("%w: %s: %w", ErrTransport, method, context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %s: %w", ErrTransport, method, context.DeadlineExceeded)
		}
		return fmt.Errorf("%w: %s", ErrTransport, method)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fmt.Errorf("%w: %s received redirect (%d)", ErrProtocol, method, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%w: read %s response", ErrTransport, method)
	}
	if len(raw) > MaxResponseBytes {
		return fmt.Errorf("%w: %s response exceeded %d bytes", ErrProtocol, method, MaxResponseBytes)
	}

	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &APIError{Method: method, HTTPStatus: resp.StatusCode}
		}
		return fmt.Errorf("%w: decode %s envelope", ErrProtocol, method)
	}
	if !env.OK || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{
			Method:     method,
			HTTPStatus: resp.StatusCode,
			ErrorCode:  env.ErrorCode,
		}
		if env.Parameters != nil {
			apiErr.RetryAfter = env.Parameters.RetryAfter
		}
		return apiErr
	}
	if out != nil {
		if len(env.Result) == 0 || bytes.Equal(bytes.TrimSpace(env.Result), []byte("null")) {
			return fmt.Errorf("%w: %s returned empty result", ErrProtocol, method)
		}
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("%w: decode %s result", ErrProtocol, method)
		}
	}
	return nil
}
