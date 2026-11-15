package bb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
)

// The shape is the one `bb thread context --json` was seen to print, with
// fictional figures; nothing here runs bb.
func TestReadContextAcceptsOnlyTheKnownShape(t *testing.T) {
	ok := func(used, window int64, estimated bool) adapter.ContextReading {
		return adapter.ContextReading{UsedTokens: used, WindowTokens: window, Estimated: estimated}
	}
	cases := []struct {
		name string
		out  string
		err  error
		want *adapter.ContextReading
	}{
		{"estimated", `{"usage":{"estimated":true,"modelContextWindow":200000,"usedTokens":48000}}`, nil, ptr(ok(48000, 200000, true))},
		{"not estimated", `{"usage":{"estimated":false,"modelContextWindow":200000,"usedTokens":0}}`, nil, ptr(ok(0, 200000, false))},
		{"extra fields ignored", `{"ok":true,"usage":{"estimated":true,"modelContextWindow":1000,"usedTokens":10,"breakdown":[]}}`, nil, ptr(ok(10, 1000, true))},
		{"past the window, within bounds", `{"usage":{"estimated":true,"modelContextWindow":1000,"usedTokens":1200}}`, nil, ptr(ok(1200, 1000, true))},
		{"command failed (older bb)", `{"ok":false,"error":{"code":"unknown_command"}}`, errors.New("exit 1"), nil},
		{"error envelope on exit 0", `{"ok":false,"error":{"code":"thread_not_found"}}`, nil, nil},
		{"stopped at its time limit", `{"usage":{"estimated":true,"modelContextWindow":1000,"usedTokens":10}}`, errStopped, nil},
		{"empty", ``, nil, nil},
		{"not JSON", `usage: 10 of 1000`, nil, nil},
		{"no usage", `{"thread":{}}`, nil, nil},
		{"usage null", `{"usage":null}`, nil, nil},
		{"no estimated flag", `{"usage":{"modelContextWindow":1000,"usedTokens":10}}`, nil, nil},
		{"estimated not a bool", `{"usage":{"estimated":"yes","modelContextWindow":1000,"usedTokens":10}}`, nil, nil},
		{"window missing", `{"usage":{"estimated":true,"usedTokens":10}}`, nil, nil},
		{"used missing", `{"usage":{"estimated":true,"modelContextWindow":1000}}`, nil, nil},
		{"quoted number", `{"usage":{"estimated":true,"modelContextWindow":"1000","usedTokens":10}}`, nil, nil},
		{"fraction", `{"usage":{"estimated":true,"modelContextWindow":1000,"usedTokens":10.5}}`, nil, nil},
		{"exponent", `{"usage":{"estimated":true,"modelContextWindow":1e6,"usedTokens":10}}`, nil, nil},
		{"null figure", `{"usage":{"estimated":true,"modelContextWindow":1000,"usedTokens":null}}`, nil, nil},
		{"zero window", `{"usage":{"estimated":true,"modelContextWindow":0,"usedTokens":0}}`, nil, nil},
		{"negative used", `{"usage":{"estimated":true,"modelContextWindow":1000,"usedTokens":-1}}`, nil, nil},
		{"window out of range", `{"usage":{"estimated":true,"modelContextWindow":100000001,"usedTokens":10}}`, nil, nil},
		{"used out of range", `{"usage":{"estimated":true,"modelContextWindow":1000,"usedTokens":400000001}}`, nil, nil},
		{"overflowing integer", `{"usage":{"estimated":true,"modelContextWindow":1000,"usedTokens":99999999999999999999}}`, nil, nil},
		{"over the output bound", `{"usage":{"estimated":true,"modelContextWindow":1000,"usedTokens":10},"pad":"` + strings.Repeat("x", contextMaxOutput) + `"}`, nil, nil},
	}
	for _, c := range cases {
		a := New(config.BB{Thread: "thr_example123"})
		var args [][]string
		a.run = func(_ context.Context, _ string, arg ...string) ([]byte, error) {
			args = append(args, arg)
			return []byte(c.out), c.err
		}
		got, err := a.ReadContext(t.Context())
		if len(args) != 1 || strings.Join(args[0], " ") != "thread context thr_example123 --json" {
			t.Errorf("%s: ran %q", c.name, args)
		}
		switch {
		case c.want == nil && err == nil:
			t.Errorf("%s: accepted %+v", c.name, got)
		case c.want == nil && err != errContextUnavailable:
			t.Errorf("%s: error %v carries more than the fixed text", c.name, err)
		case c.want != nil && (err != nil || got != *c.want):
			t.Errorf("%s: got %+v, %v; want %+v", c.name, got, err, *c.want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// The adapter declares the reading through the optional interface only.
var _ adapter.ContextReader = (*Adapter)(nil)
