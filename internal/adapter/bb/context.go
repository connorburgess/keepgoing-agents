package bb

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/looptender/looptender/internal/adapter"
)

// Bounds on a context reading. Anything outside them is unavailable, not
// clamped: a figure that cannot be true is not shown as one.
const (
	contextMaxOutput = 64 << 10    // bytes of `bb thread context --json`
	contextMaxWindow = 100_000_000 // tokens; far above any model's window
	contextMaxUsed   = 400_000_000 // tokens; an estimate may pass the window
)

// errContextUnavailable is the only error ReadContext returns. It carries no
// command output, so nothing bb printed can reach a log or a record.
var errContextUnavailable = errors.New("bb context reading unavailable")

// ReadContext implements adapter.ContextReader with
// `bb thread context <thread> --json`, for the configured thread only. The
// one shape it accepts is
//
//	{"usage":{"estimated":<bool>,"modelContextWindow":<int>,"usedTokens":<int>}}
//
// Output over its bound, an error envelope, a missing or non-integer figure,
// a figure out of range, a non-positive window and a missing estimated flag
// are all unavailable. Older bb versions lack the command; that is
// unavailable too.
func (a *Adapter) ReadContext(ctx context.Context) (adapter.ContextReading, error) {
	out, err := a.run(ctx, "", "thread", "context", a.cfg.Thread, "--json")
	if err != nil || len(out) == 0 || len(out) > contextMaxOutput {
		return adapter.ContextReading{}, errContextUnavailable
	}
	if _, failed := envelopeError(out); failed {
		return adapter.ContextReading{}, errContextUnavailable
	}
	return parseContext(out)
}

func parseContext(out []byte) (adapter.ContextReading, error) {
	var body struct {
		Usage *struct {
			Estimated *bool           `json:"estimated"`
			Window    json.RawMessage `json:"modelContextWindow"`
			Used      json.RawMessage `json:"usedTokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(out, &body) != nil || body.Usage == nil || body.Usage.Estimated == nil {
		return adapter.ContextReading{}, errContextUnavailable
	}
	window, ok := saneInt(body.Usage.Window, 1, contextMaxWindow)
	if !ok {
		return adapter.ContextReading{}, errContextUnavailable
	}
	used, ok := saneInt(body.Usage.Used, 0, contextMaxUsed)
	if !ok {
		return adapter.ContextReading{}, errContextUnavailable
	}
	return adapter.ContextReading{UsedTokens: used, WindowTokens: window, Estimated: *body.Usage.Estimated}, nil
}

// saneInt accepts a JSON integer within [lo, hi]: not a fraction, an
// exponent, a quoted number, null or an absent field.
func saneInt(raw json.RawMessage, lo, hi int64) (int64, bool) {
	v, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || v < lo || v > hi {
		return 0, false
	}
	return v, true
}
