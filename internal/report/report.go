// Package report posts signed webhook reports. The signature follows the
// Standard Webhooks scheme: HMAC-SHA256 over "id.timestamp.body" with the
// base64 secret (optionally prefixed whsec_), sent as "v1,<base64>".
package report

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// Event is one report.
type Event struct {
	Type string         `json:"type"`
	Loop string         `json:"loop"`
	Text string         `json:"text"`
	At   time.Time      `json:"at"`
	Data map[string]any `json:"data,omitempty"`
}

// Reporter sends events.
type Reporter interface {
	Report(ctx context.Context, e Event) error
}

// Nop discards events.
type Nop struct{}

// Report implements Reporter.
func (Nop) Report(context.Context, Event) error { return nil }

// Webhook posts to one endpoint.
type Webhook struct {
	cfg    config.Report
	url    string
	secret string
	client *http.Client
	now    func() time.Time
}

// MaxBody caps a report's size.
const MaxBody = 1 << 20

// New resolves credentials from the environment, then the secret file.
// Secrets are never logged or included in errors.
func New(cfg config.Report) (*Webhook, error) {
	vals := map[string]string{}
	if cfg.SecretFile != "" {
		v, err := readEnvFile(cfg.SecretFile)
		if err != nil {
			return nil, fmt.Errorf("report secret file: %w", err)
		}
		vals = v
	}
	get := func(name string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return vals[name]
	}
	w := &Webhook{cfg: cfg, url: get(cfg.URLEnv), secret: get(cfg.SecretEnv), client: &http.Client{Timeout: cfg.Timeout.Duration}, now: time.Now}
	if w.url == "" || w.secret == "" {
		return nil, fmt.Errorf("report: %s and %s must be set in the environment or the secret file", cfg.URLEnv, cfg.SecretEnv)
	}
	u, err := url.Parse(w.url)
	if err != nil || u.Host == "" {
		return nil, errors.New("report: the webhook URL is malformed")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, errors.New("report: the webhook URL must use https (http is allowed only for localhost)")
	}
	if strings.ContainsAny(w.secret, " \t\r\n") {
		return nil, errors.New("report: the webhook secret is malformed")
	}
	if cfg.Scheme == "standard-webhooks" {
		if _, err := key(w.secret); err != nil {
			return nil, errors.New("report: the webhook secret must be base64, optionally prefixed whsec_")
		}
	}
	return w, nil
}

func isLoopback(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

func key(secret string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
}

// Body renders the payload in the configured format.
func (w *Webhook) Body(e Event) ([]byte, error) {
	if e.At.IsZero() {
		e.At = w.now()
	}
	var v any
	if w.cfg.Format == "text" {
		v = map[string]any{"source": w.cfg.Source, "sent_at": e.At.UTC().Format(time.RFC3339), "text": e.Text}
	} else {
		data := map[string]any{"source": w.cfg.Source, "loop": e.Loop, "text": e.Text}
		for k, val := range e.Data {
			data[k] = val
		}
		v = map[string]any{"type": "keepgoing." + e.Type, "timestamp": e.At.UTC().Format(time.RFC3339), "data": data}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBody {
		return nil, errors.New("report: body is over 1 MiB")
	}
	return b, nil
}

// Sign returns the Standard Webhooks headers for a body.
func Sign(secret, id string, ts time.Time, body []byte) (map[string]string, error) {
	k, err := key(secret)
	if err != nil {
		return nil, err
	}
	t := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, k)
	fmt.Fprintf(mac, "%s.%s.", id, t)
	mac.Write(body)
	return map[string]string{
		"webhook-id":        id,
		"webhook-timestamp": t,
		"webhook-signature": "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	}, nil
}

// Report implements Reporter. Most events retry once on a network error or 5xx.
// Completion declarations make one attempt without redirects: an uncertain
// delivery must not cause a duplicate notification at a non-idempotent receiver.
func (w *Webhook) Report(ctx context.Context, e Event) error {
	return w.report(ctx, e, nil)
}

// observe receives only typed transport evidence, never the payload or errors.
// It must not change sending, retry, redirect or completion semantics.
func (w *Webhook) report(ctx context.Context, e Event, observe func(Delivery)) error {
	if !w.cfg.ReportsEvent(e.Type) {
		return nil
	}
	d := Delivery{Event: eventName(e.Type), StartedAt: w.now().UTC(), Receipt: "unverified", Attempts: []Attempt{}}
	snapshot := func(outcome string) {
		d.Outcome, d.UpdatedAt = outcome, w.now().UTC()
		if observe != nil {
			observe(d)
		}
	}
	snapshot("prepared")
	body, err := w.Body(e)
	if err != nil {
		snapshot("body_failed")
		return err
	}
	headers := map[string]string{"content-type": "application/json"}
	if w.cfg.Scheme == "bearer" {
		headers["authorization"] = "Bearer " + w.secret
	} else {
		id := make([]byte, 12)
		rand.Read(id)
		sig, err := Sign(w.secret, "msg_"+hex.EncodeToString(id), w.now(), body)
		if err != nil {
			snapshot("sign_failed")
			return err
		}
		for k, v := range sig {
			headers[k] = v
		}
	}
	attempts := 2
	client := w.client
	// A limit_wait, child_error or check_gap report is requested once and, like a
	// completion, makes one attempt: an uncertain delivery must not page a
	// person twice.
	if e.Type == "complete" || e.Type == "limit_wait" || e.Type == "child_error" || e.Type == "network_wait" || e.Type == "check_gap" {
		attempts = 1
		copy := *w.client
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &copy
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
		if err != nil {
			snapshot("request_failed")
			return err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		d.Attempts = append(d.Attempts, Attempt{StartedAt: w.now().UTC(), Outcome: "unconfirmed"})
		snapshot("attempt_started")
		resp, err := client.Do(req)
		a := &d.Attempts[len(d.Attempts)-1]
		a.FinishedAt = w.now().UTC()
		if err != nil {
			a.Outcome = "transport_failed"
			snapshot("transport_failed")
			last = errors.New("report: request failed") // the error may embed the URL; keep it out
			continue
		}
		resp.Body.Close()
		a.HTTPStatus = resp.StatusCode
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			a.Outcome = "http_accepted"
			snapshot("http_accepted")
			return nil
		}
		a.Outcome = "http_rejected"
		snapshot("http_rejected")
		last = fmt.Errorf("report: endpoint returned %d", resp.StatusCode)
		if resp.StatusCode < 500 {
			return last
		}
	}
	return last
}

func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	v := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(val), `"'`)
	}
	return v, sc.Err()
}
