package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"time"
)

// CodexLimits is the structured usage state Codex's app-server reports
// through account/rateLimits/read.
type CodexLimits struct {
	// Blocked: the backend reports ordinary usage not allowed, or a limit
	// reached type is set.
	Blocked     bool
	ReachedType string
	// ResetsAt is when the reached window lifts, when reported.
	ResetsAt time.Time
	// Windows are the usage windows the snapshot reported, blocked or not.
	// They are readings only and never decide Blocked.
	Windows []CodexWindow
}

// CodexWindow is one usage window from the snapshot. Name is the snapshot's
// own field name. ResetsAt is zero when the snapshot gave none.
type CodexWindow struct {
	Name        string
	UsedPercent int
	ResetsAt    time.Time
}

// ReadCodexLimits asks `codex app-server` for the account's rate-limit
// snapshot over its stdio JSON-RPC protocol. It reads usage status only and
// spends no tokens. A variable so tests can substitute a fake.
var ReadCodexLimits = readCodexLimits

type rpcMessage struct {
	ID     *int            `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type rateLimitWindow struct {
	UsedPercent *int   `json:"usedPercent"`
	ResetsAt    *int64 `json:"resetsAt"`
}

type rateLimitsResponse struct {
	OrdinaryUsageAllowed *bool `json:"ordinaryUsageAllowed"`
	RateLimits           *struct {
		RateLimitReachedType *string          `json:"rateLimitReachedType"`
		Primary              *rateLimitWindow `json:"primary"`
		Secondary            *rateLimitWindow `json:"secondary"`
	} `json:"rateLimits"`
}

func readCodexLimits(ctx context.Context, cli string, env []string) (CodexLimits, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "app-server")
	cmd.Env = append(os.Environ(), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return CodexLimits{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return CodexLimits{}, err
	}
	if err := cmd.Start(); err != nil {
		return CodexLimits{}, err
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	send := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(b, '\n'))
		return err
	}
	await := func(id int) (rpcMessage, error) {
		for lines.Scan() {
			var m rpcMessage
			if json.Unmarshal(lines.Bytes(), &m) == nil && m.ID != nil && *m.ID == id {
				if len(m.Error) > 0 && string(m.Error) != "null" {
					return m, errors.New("codex app-server returned an error")
				}
				return m, nil
			}
		}
		if ctx.Err() != nil {
			return rpcMessage{}, ctx.Err()
		}
		return rpcMessage{}, errors.New("codex app-server closed before replying")
	}
	if err := send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "keepgoing", "version": "1"}}}); err != nil {
		return CodexLimits{}, err
	}
	if _, err := await(1); err != nil {
		return CodexLimits{}, err
	}
	if err := send(map[string]any{"method": "initialized"}); err != nil {
		return CodexLimits{}, err
	}
	if err := send(map[string]any{"id": 2, "method": "account/rateLimits/read", "params": map[string]any{"excludeResetCreditDetails": true}}); err != nil {
		return CodexLimits{}, err
	}
	m, err := await(2)
	if err != nil {
		return CodexLimits{}, err
	}
	var r rateLimitsResponse
	if err := json.Unmarshal(m.Result, &r); err != nil {
		return CodexLimits{}, errors.New("codex app-server: unexpected rate-limit JSON")
	}
	return limitsFrom(r), nil
}

// limitsFrom interprets the snapshot. Blocked comes only from the backend's
// own fields; the reset time is the latest reset among windows at 100%, or
// among all windows when the backend says blocked without a full window.
func limitsFrom(r rateLimitsResponse) CodexLimits {
	var l CodexLimits
	if r.OrdinaryUsageAllowed != nil && !*r.OrdinaryUsageAllowed {
		l.Blocked = true
	}
	if r.RateLimits == nil {
		return l
	}
	if t := r.RateLimits.RateLimitReachedType; t != nil && *t != "" {
		l.Blocked, l.ReachedType = true, *t
	}
	for _, named := range []struct {
		name string
		w    *rateLimitWindow
	}{{"primary", r.RateLimits.Primary}, {"secondary", r.RateLimits.Secondary}} {
		// A window without a usage figure is not a reading.
		if named.w == nil || named.w.UsedPercent == nil {
			continue
		}
		cw := CodexWindow{Name: named.name, UsedPercent: *named.w.UsedPercent}
		if named.w.ResetsAt != nil && *named.w.ResetsAt > 0 {
			cw.ResetsAt = time.Unix(*named.w.ResetsAt, 0).UTC()
		}
		l.Windows = append(l.Windows, cw)
	}
	if !l.Blocked {
		return l
	}
	var full, any time.Time
	for _, w := range []*rateLimitWindow{r.RateLimits.Primary, r.RateLimits.Secondary} {
		if w == nil || w.ResetsAt == nil || *w.ResetsAt <= 0 {
			continue
		}
		t := time.Unix(*w.ResetsAt, 0).UTC()
		if t.After(any) {
			any = t
		}
		if w.UsedPercent != nil && *w.UsedPercent >= 100 && t.After(full) {
			full = t
		}
	}
	l.ResetsAt = full
	if l.ResetsAt.IsZero() {
		l.ResetsAt = any
	}
	return l
}
