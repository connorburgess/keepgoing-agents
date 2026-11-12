package probe

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
)

// Where a usage reading came from.
const (
	// UsageSourceClaude is the rate_limit_event message in Claude Code's
	// stream-json output. Its shape was observed, not documented.
	UsageSourceClaude = "claude_rate_limit_event"
	// UsageSourceCodex is the Codex app-server's account/rateLimits/read.
	UsageSourceCodex = "codex_rate_limits_read"
)

// Bounds on a stored reading.
const (
	maxUsageWindows = 8
	maxUsageToken   = 64
)

// Usage is one usage reading for a candidate's account: what a provider
// reported about its quota windows at At. It is a display value. Nothing in
// the decision engine reads it, and an absent reading means unavailable.
type Usage struct {
	At      time.Time     `json:"at"`
	Source  string        `json:"source"`
	Windows []UsageWindow `json:"windows"`
}

// UsageWindow is one quota window. Key is the provider's own name for it and
// is treated as opaque. Used is the fraction consumed, 0 to 1.
type UsageWindow struct {
	Key      string     `json:"key"`
	Used     float64    `json:"used"`
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
	Status   string     `json:"status,omitempty"`
}

// Available reports whether the reading holds at least one window.
func (u *Usage) Available() bool {
	return u != nil && !u.At.IsZero() && len(u.Windows) > 0
}

// UnmarshalJSON never fails: a stored reading that is malformed or out of
// bounds loads as unavailable instead of making the health registry unreadable.
func (u *Usage) UnmarshalJSON(b []byte) error {
	type plain Usage
	var p plain
	*u = Usage{}
	if json.Unmarshal(b, &p) != nil || p.At.IsZero() {
		return nil
	}
	source, ok := usageToken(p.Source)
	if !ok {
		return nil
	}
	windows := boundWindows(p.Windows)
	if len(windows) == 0 {
		return nil
	}
	*u = Usage{At: p.At.UTC(), Source: source, Windows: windows}
	return nil
}

func newUsage(at time.Time, source string, windows []UsageWindow) *Usage {
	windows = boundWindows(windows)
	if len(windows) == 0 {
		return nil
	}
	return &Usage{At: at.UTC(), Source: source, Windows: windows}
}

// boundWindows drops windows that are not a plain reading and caps the rest.
// Keys are unique and sorted so the stored record is deterministic.
func boundWindows(in []UsageWindow) []UsageWindow {
	byKey := map[string]UsageWindow{}
	for _, w := range in {
		key, ok := usageToken(w.Key)
		if !ok || math.IsNaN(w.Used) || w.Used < 0 || w.Used > 1 {
			continue
		}
		out := UsageWindow{Key: key, Used: w.Used}
		if w.ResetsAt != nil && !w.ResetsAt.IsZero() {
			t := w.ResetsAt.UTC()
			out.ResetsAt = &t
		}
		if status, ok := usageToken(w.Status); ok {
			out.Status = status
		}
		byKey[key] = out
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > maxUsageWindows {
		keys = keys[:maxUsageWindows]
	}
	out := make([]UsageWindow, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

// usageToken accepts a short identifier: letters, digits, '_', '-' and '.'.
// Anything else is refused, so no free text from a provider is stored.
func usageToken(s string) (string, bool) {
	if s == "" || len(s) > maxUsageToken {
		return "", false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return "", false
		}
	}
	return s, true
}

// ClaudeUsage reads the quota windows from rate_limit_event messages in
// Claude Code stream-json output. Later events override earlier ones per
// window. The event is not a documented contract, so anything unexpected
// (no event, another shape, a utilization outside 0..1) yields no window for
// that part and never an error.
func ClaudeUsage(stdout []byte) []UsageWindow {
	var windows []UsageWindow
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Info struct {
				Windows map[string]json.RawMessage `json:"unifiedWindows"`
			} `json:"rate_limit_info"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "rate_limit_event" {
			continue
		}
		for key, raw := range event.Info.Windows {
			var w struct {
				Utilization *float64 `json:"utilization"`
				ResetsAt    *float64 `json:"resetsAt"`
				Status      *string  `json:"status"`
			}
			if json.Unmarshal(raw, &w) != nil || w.Utilization == nil {
				continue
			}
			window := UsageWindow{Key: key, Used: *w.Utilization}
			// Epoch seconds; refuse values that are not a plausible time.
			if w.ResetsAt != nil && *w.ResetsAt > 0 && *w.ResetsAt < 1e11 {
				t := time.Unix(int64(*w.ResetsAt), 0).UTC()
				window.ResetsAt = &t
			}
			if w.Status != nil {
				window.Status = *w.Status
			}
			windows = append(windows, window)
		}
	}
	return boundWindows(windows)
}

// CodexUsage converts the app-server snapshot's windows. A percentage outside
// 0..100 is not a reading.
func CodexUsage(l runner.CodexLimits) []UsageWindow {
	var windows []UsageWindow
	for _, w := range l.Windows {
		if w.UsedPercent < 0 || w.UsedPercent > 100 {
			continue
		}
		window := UsageWindow{Key: w.Name, Used: float64(w.UsedPercent) / 100}
		if !w.ResetsAt.IsZero() {
			t := w.ResetsAt
			window.ResetsAt = &t
		}
		windows = append(windows, window)
	}
	return boundWindows(windows)
}
