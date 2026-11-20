package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/telegram"
	"github.com/looptender/looptender/internal/userdir"
)

var telegramStateDir = func() (string, error) {
	return userdir.State("telegram")
}

type telegramSender struct {
	client     *telegram.Client
	chatID     int64
	loopTopics map[string]int64
	device     string
}

func newTelegramWebhook(cfg config.Report, get func(string) string) (*Webhook, error) {
	if cfg.Scheme != "" && cfg.Scheme != "telegram-bot" {
		return nil, errors.New("report: telegram format requires telegram-bot scheme")
	}
	dir, _ := telegramStateDir()
	token := strings.TrimSpace(get(cfg.SecretEnv))
	if token == "" && dir != "" {
		if t, err := telegram.LoadTokenFile(dir); err == nil {
			token = t
		}
	}
	if token == "" {
		return nil, fmt.Errorf("report: %s must be set or Telegram must be paired with `looptender telegram setup`", cfg.SecretEnv)
	}

	var chatID int64
	var loopTopics map[string]int64
	if rawChat := strings.TrimSpace(get(telegram.ChatIDEnv)); rawChat != "" {
		id, err := strconv.ParseInt(rawChat, 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("report: %s must be a non-zero integer chat id", telegram.ChatIDEnv)
		}
		chatID = id
	} else if dir != "" {
		rec, ok, err := telegram.LoadPairing(dir)
		if err != nil {
			return nil, errors.New("report: paired Telegram state is unavailable")
		}
		if ok {
			chatID = rec.AllowedChatID
			if rec.TopicsEnabled && len(rec.LoopTopics) > 0 {
				loopTopics = rec.LoopTopics
			}
		}
	}
	if chatID == 0 {
		return nil, fmt.Errorf("report: %s must be set or Telegram must be paired with `looptender telegram setup`", telegram.ChatIDEnv)
	}

	baseURL := strings.TrimSpace(get(cfg.URLEnv))
	client, err := telegram.NewClient(telegram.ClientConfig{
		Token:   token,
		BaseURL: baseURL,
		Timeout: cfg.Timeout.Duration,
	})
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}
	host, _ := os.Hostname()
	return &Webhook{
		cfg: cfg,
		now: time.Now,
		tg: &telegramSender{
			client:     client,
			chatID:     chatID,
			loopTopics: loopTopics,
			device:     host,
		},
	}, nil
}

func (s *telegramSender) request(e Event, now time.Time) telegram.SendMessageRequest {
	at := e.At
	if at.IsZero() {
		at = now
	}
	route, _ := e.Data["target"].(string)
	holdReason, _ := e.Data["cause"].(string)
	text := telegram.FormatAlert(telegram.AlertSummary{
		Device:     s.device,
		Loop:       e.Loop,
		Event:      e.Type,
		Summary:    e.Text,
		Route:      route,
		HoldReason: holdReason,
		At:         at,
	})
	var threadID int64
	if s.loopTopics != nil {
		threadID = s.loopTopics[e.Loop]
	}
	return telegram.SendMessageRequest{
		ChatID:             s.chatID,
		MessageThreadID:    threadID,
		Text:               text,
		LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
	}
}

func (s *telegramSender) body(e Event, now time.Time) ([]byte, error) {
	b, err := json.Marshal(s.request(e, now))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBody {
		return nil, errors.New("report: body is over 1 MiB")
	}
	return b, nil
}

// report sends one redacted Telegram alert without redirects or retries so an
// uncertain transport result never double-pages the user.
func (s *telegramSender) report(ctx context.Context, e Event, now func() time.Time, observe func(Delivery)) error {
	d := Delivery{Event: eventName(e.Type), StartedAt: now().UTC(), Receipt: "unverified", Attempts: []Attempt{}}
	save := func(outcome string) {
		d.Outcome = outcome
		d.UpdatedAt = now().UTC()
		if observe != nil {
			observe(d)
		}
	}
	save("prepared")
	req := s.request(e, now())
	d.Attempts = append(d.Attempts, Attempt{StartedAt: now().UTC(), Outcome: "unconfirmed"})
	save("attempt_started")
	_, err := s.client.SendMessage(ctx, req)
	a := &d.Attempts[0]
	a.FinishedAt = now().UTC()
	if err == nil {
		a.HTTPStatus = 200
		a.Outcome = "http_accepted"
		save("http_accepted")
		return nil
	}
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) {
		a.HTTPStatus = apiErr.HTTPStatus
		if apiErr.HTTPStatus >= 200 && apiErr.HTTPStatus < 300 {
			a.Outcome = "protocol_failed"
			save("protocol_failed")
		} else {
			a.Outcome = "http_rejected"
			save("http_rejected")
		}
		return errors.Join(fmt.Errorf("report: %w", err), ErrNoRetry)
	}
	if errors.Is(err, telegram.ErrProtocol) {
		a.HTTPStatus = 502
		a.Outcome = "protocol_failed"
		save("protocol_failed")
		return errors.Join(fmt.Errorf("report: %w", err), ErrNoRetry)
	}
	a.Outcome = "transport_failed"
	save("transport_failed")
	return errors.Join(fmt.Errorf("report: %w", err), ErrNoRetry)
}
