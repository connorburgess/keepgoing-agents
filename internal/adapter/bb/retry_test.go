package bb

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func retryLog(tail string) string {
	s := `[` + turnStarted + `,` + providerError("stream-disconnected", nil, "responseStreamDisconnected", true)
	if tail != "" {
		s += `,` + tail
	}
	return s + `]`
}

func TestActiveRetryAcrossIntervalsAndRestart(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 7; i++ {
		a, _ := newFake(`{"thread":{"status":"active","queuedMessageCount":2}}`, retryLog(`{"type":"client/turn/requested","data":{}}`), false)
		a.WithStateDir(dir)
		o, err := a.Observe(context.Background())
		in := decide.Input{Control: decide.Control{Enabled: true}, InWindow: true, Obs: o, UnchangedFor: time.Duration(i+1) * time.Hour, SinceWake: time.Hour, StallAfter: time.Minute, MinWakeInterval: time.Minute, MaxNudges: 3, Nudges: 3}
		d := decide.Decide(in)
		if err != nil || !o.Retrying || o.ErrorClass != decide.ErrNetwork || d.Action != decide.None || d.Reason != "provider is retrying the turn" {
			t.Fatalf("interval %d: %+v %+v %v", i, o, d, err)
		}
	}
}

func TestActiveRetryRecoveryEvidence(t *testing.T) {
	cases := []struct {
		name, tail string
		retry      bool
	}{
		{"message_delta", `{"type":"item/agentMessage/delta","data":{"providerThreadId":"p1","itemId":"item_example","delta":"private text"}}`, false},
		{"agent_item", `{"type":"item/started","data":{"providerThreadId":"p1","item":{"id":"item_example","type":"agentMessage","text":"private text"}}}`, false},
		{"completed", `{"type":"turn/completed","data":{"providerThreadId":"p1","status":"completed"}}`, false},
		{"failed", `{"type":"turn/completed","data":{"providerThreadId":"p1","status":"failed"}}`, false},
		{"interrupted", `{"type":"turn/completed","data":{"providerThreadId":"p1","status":"interrupted"}}`, false},
		{"new_turn", turnStarted, false},
		// A provider that recovers straight into a tool call starts a tool
		// item and may write no message for a long time.
		{"tool_item", `{"type":"item/started","data":{"providerThreadId":"p1","item":{"id":"item_example","type":"commandExecution"}}}`, false},
		{"reasoning_item", `{"type":"item/started","data":{"providerThreadId":"p1","item":{"id":"item_example","type":"reasoning"}}}`, false},
		{"item_without_identity", `{"type":"item/started","data":{"providerThreadId":"p1","item":{}}}`, true},
		{"item_of_another_provider_thread", `{"type":"item/started","data":{"providerThreadId":"other","item":{"id":"item_example","type":"commandExecution"}}}`, true},
		{"tool_item_finished", `{"type":"item/completed","data":{"providerThreadId":"p1","item":{"id":"item_example","type":"commandExecution"}}}`, true},
		{"queued_input", `{"type":"client/turn/requested","data":{}}`, true},
		{"acknowledged_input", `{"type":"turn/input/accepted","data":{"providerThreadId":"p1","clientRequestId":"req_example"}}`, true},
		{"tool_output", `{"type":"item/commandExecution/outputDelta","data":{"providerThreadId":"p1","itemId":"item_example"}}`, true},
		{"wrong_provider", `{"type":"item/agentMessage/delta","data":{"providerThreadId":"other","itemId":"item_example"}}`, true},
		{"missing_identity", `{"type":"item/agentMessage/delta","data":{}}`, true},
		{"unknown_end", `{"type":"turn/completed","data":{"providerThreadId":"p1","status":"mystery"}}`, true},
		{"prose", `{"type":"message","data":{"text":"the network recovered"}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a, _ := newFake(`{"thread":{"status":"active"}}`, retryLog(""), false)
			a.WithStateDir(dir)
			if o, err := a.Observe(context.Background()); err != nil || !o.Retrying {
				t.Fatalf("initial retry: %+v %v", o, err)
			}
			b, _ := newFake(`{"thread":{"status":"active"}}`, retryLog(tc.tail), false)
			b.WithStateDir(dir)
			o, err := b.Observe(context.Background())
			if err != nil || o.Retrying != tc.retry {
				t.Fatalf("%+v %v", o, err)
			}
			saved, _ := os.ReadFile(filepath.Join(dir, "bb-cursor.json"))
			if strings.Contains(string(saved), "private text") {
				t.Fatal("cursor persisted message content")
			}
		})
	}
}

func TestRetryScanFailureWithholdsStallAndRecovery(t *testing.T) {
	a, _ := newFake(`{"thread":{"status":"active"}}`, retryLog(""), false)
	a.Observe(context.Background())
	original := a.run
	a.run = func(ctx context.Context, in string, args ...string) ([]byte, error) {
		if args[1] == "log" {
			return nil, errors.New("unavailable")
		}
		return original(ctx, in, args...)
	}
	o, err := a.Observe(context.Background())
	d := decide.Decide(decide.Input{Control: decide.Control{Enabled: true}, InWindow: true, Obs: o, UnchangedFor: time.Hour, SinceWake: time.Hour, StallAfter: time.Minute})
	if err != nil || o.Evidence.Source != "unavailable" || d.Action != decide.None || d.Reason != "provider retry state unverified" {
		t.Fatalf("%+v %+v %v", o, d, err)
	}
}

func TestRetryLegacyCursorRebuildDoesNotReplayChildNotice(t *testing.T) {
	dir := t.TempDir()
	// Old cursors could clear a retry when input was only queued.
	legacy := Cursor{Thread: "thr_example123", LastSeq: 4, TurnSeq: 4}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "bb-cursor.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	log := retryLog(`{"type":"client/turn/requested","data":{}},{"type":"client/turn/requested","data":` + outcomeData("completed") + `}`)
	a, _ := newFake(`{"thread":{"status":"active"}}`, log, false)
	a.WithStateDir(dir)
	if a.PollWake(context.Background()) {
		t.Fatal("legacy replay woke parent")
	}
	o, err := a.Observe(context.Background())
	if err != nil || !o.Retrying {
		t.Fatalf("legacy retry not rebuilt: %+v %v", o, err)
	}
}

func TestActiveRetryMissingIntentIsUnknownUntilProviderActivity(t *testing.T) {
	for _, flag := range []any{nil, false, true} {
		log := `[` + turnStarted + `,` + providerError("unknown", nil, "unknown", flag) + `]`
		a, _ := newFake(`{"thread":{"status":"active"}}`, log, false)
		o, err := a.Observe(context.Background())
		if err != nil || o.RetryUnverified != (flag == nil) || o.Retrying != (flag == true) {
			t.Fatalf("flag=%v: %+v %v", flag, o, err)
		}
		log = strings.TrimSuffix(log, "]") + `,{"type":"item/agentMessage/delta","data":{"providerThreadId":"p1","itemId":"item_example"}}]`
		b, _ := newFake(`{"thread":{"status":"active"}}`, log, false)
		o, err = b.Observe(context.Background())
		if err != nil || o.RetryUnverified || o.Retrying {
			t.Fatalf("recovery: %+v %v", o, err)
		}
	}
}

func TestRetryIncompleteScanDoesNotPersistRecovery(t *testing.T) {
	for _, recovery := range []event{
		{Seq: 3, Type: "item/agentMessage/delta", Data: json.RawMessage(`{"providerThreadId":"p1","itemId":"item_example"}`)},
		{Seq: 3, Type: "turn/completed", Data: json.RawMessage(`{"providerThreadId":"p1","status":"failed"}`)},
	} {
		t.Run(recovery.Type, func(t *testing.T) {
			dir := t.TempDir()
			a, _ := newFake(`{"thread":{"status":"active"}}`, retryLog(""), false)
			a.WithStateDir(dir)
			a.Observe(context.Background())
			saved, _ := os.ReadFile(filepath.Join(dir, "bb-cursor.json"))
			original := a.run
			calls := 0
			a.run = func(ctx context.Context, in string, args ...string) ([]byte, error) {
				if args[1] != "log" {
					return original(ctx, in, args...)
				}
				calls++
				if calls > 1 {
					return nil, errors.New("unavailable next page")
				}
				events := []event{recovery}
				for i := 1; i < pageSize; i++ {
					events = append(events, event{Seq: int64(i + 3), Type: "ignored", Data: json.RawMessage(`{}`)})
				}
				b, _ := json.Marshal(events)
				return b, nil
			}
			o, err := a.Observe(context.Background())
			if err != nil || !o.RetryUnverified {
				t.Fatalf("partial scan recovered: %+v %v", o, err)
			}
			after, _ := os.ReadFile(filepath.Join(dir, "bb-cursor.json"))
			if string(saved) != string(after) {
				t.Fatal("partial scan persisted recovery")
			}

			if c := a.loadCursor(); c.Error == nil || !c.Error.WillRetry || c.LastSeq != 2 {
				t.Fatalf("partial scan mutated committed in-memory cursor: %+v", c)
			}
		})
	}
}

func TestErroredRetryIntentAvailability(t *testing.T) {
	for _, flag := range []any{nil, false, true} {
		a, _ := newFake(`{"thread":{"status":"error"}}`, `[`+turnStarted+`,`+providerError("network", nil, "unknown", flag)+`]`, false)
		o, err := a.Observe(context.Background())
		if err != nil || o.RetryKnown != (flag != nil) {
			t.Fatalf("flag=%v: %+v %v", flag, o, err)
		}
	}
}
