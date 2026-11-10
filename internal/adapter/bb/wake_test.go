package bb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func outcomeData(status string) string {
	return fmt.Sprintf(`{"initiator":"system","systemMessageKind":"child-%s","systemMessageSubject":{"kind":"thread","threadId":"thr_child","outcomes":[{"threadId":"thr_child","status":%q}]},"input":[{"text":"fictional private output"}]}`, status, status)
}

func outcomeLog(status string) string {
	return `[{"seq":1,"type":"client/turn/requested","data":` + outcomeData(status) + `}]`
}

func TestChildOutcomeMetadata(t *testing.T) {
	good := outcomeData("completed")
	for _, tc := range []struct {
		name, typ, data string
		want            bool
	}{
		{"completed", "client/turn/requested", good, true},
		{"failed", "client/turn/requested", outcomeData("failed"), true},
		{"interrupted", "client/turn/requested", outcomeData("interrupted"), true},
		{"batch", "client/turn/requested", `{"initiator":"system","systemMessageKind":"child-outcome-batch","systemMessageSubject":{"kind":"thread-batch","count":2,"outcomes":[{"threadId":"thr_first","status":"completed"},{"threadId":"thr_second","status":"failed"}]}}`, true},
		{"wrong_event", "provider/message", good, false},
		{"user_metadata", "client/turn/requested", strings.Replace(good, `"system"`, `"user"`, 1), false},
		{"retry", "client/turn/requested", strings.Replace(good, `"initiator":`, `"retryOfRequestId":"request_example","initiator":`, 1), false},
		{"needs_attention", "client/turn/requested", strings.Replace(good, "child-completed", "child-needs-attention", 1), false},
		{"wrong_status", "client/turn/requested", strings.Replace(good, `"status":"completed"`, `"status":"active"`, 1), false},
		{"mismatched_kind", "client/turn/requested", strings.Replace(good, "child-completed", "child-failed", 1), false},
		{"mismatched_child", "client/turn/requested", strings.Replace(good, "thr_child", "thr_other", 1), false},
		{"self", "client/turn/requested", strings.ReplaceAll(good, "thr_child", "thr_parent"), false},
		{"invalid_id", "client/turn/requested", strings.ReplaceAll(good, "thr_child", "--private"), false},
		{"null", "client/turn/requested", `null`, false},
		{"prose_only", "client/turn/requested", `{"input":[{"text":"child completed"}]}`, false},
		{"missing_outcomes", "client/turn/requested", `{"initiator":"system","systemMessageKind":"child-completed","systemMessageSubject":{"kind":"thread","threadId":"thr_child"}}`, false},
		{"wrong_count", "client/turn/requested", `{"initiator":"system","systemMessageKind":"child-outcome-batch","systemMessageSubject":{"kind":"thread-batch","count":2,"outcomes":[{"threadId":"thr_child","status":"failed"}]}}`, false},
		{"duplicate_child", "client/turn/requested", `{"initiator":"system","systemMessageKind":"child-outcome-batch","systemMessageSubject":{"kind":"thread-batch","count":2,"outcomes":[{"threadId":"thr_child","status":"failed"},{"threadId":"thr_child","status":"completed"}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := childOutcome(event{Type: tc.typ, Data: json.RawMessage(tc.data)}, "thr_parent"); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestPollWakeConsumesAcrossRestart(t *testing.T) {
	for _, status := range []string{"completed", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			a, _ := newFake(`{"thread":{"status":"idle"}}`, outcomeLog(status), false)
			a.WithStateDir(dir)
			if !a.PollWake(context.Background()) {
				t.Fatal("new outcome missed")
			}
			if a.PollWake(context.Background()) {
				t.Fatal("outcome replayed")
			}
			a, _ = newFake(`{"thread":{"status":"idle"}}`, outcomeLog(status), false)
			a.WithStateDir(dir)
			if a.PollWake(context.Background()) {
				t.Fatal("outcome replayed after restart")
			}
			b, err := os.ReadFile(a.cursorPath())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "fictional private output") || strings.Contains(string(b), "thr_child") {
				t.Fatal("cursor retained notification content")
			}
		})
	}
}

func TestPollWakeNormalObservationAndNewTurnConsumeNotice(t *testing.T) {
	a, _ := newFake(`{"thread":{"status":"idle"}}`, outcomeLog("completed"), false)
	a.Observe(context.Background())
	if a.PollWake(context.Background()) {
		t.Fatal("ordinary observation replayed")
	}
	log := strings.TrimSuffix(outcomeLog("completed"), "]") + `,{"seq":2,"type":"turn/started","data":{}},{"seq":3,"type":"turn/completed","data":{}}]`
	a, _ = newFake(`{"thread":{"status":"idle"}}`, log, false)
	if a.PollWake(context.Background()) {
		t.Fatal("parent already ran after notification")
	}
}

func TestPollWakeFailureDoesNotAuthorizeCheck(t *testing.T) {
	for _, which := range []string{"log", "persist", "cancel"} {
		t.Run(which, func(t *testing.T) {
			a, _ := newFake(`{"thread":{"status":"idle"}}`, outcomeLog("completed"), false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch which {
			case "log":
				a.run = func(context.Context, string, ...string) ([]byte, error) {
					return []byte(outcomeLog("completed")), errors.New("unavailable")
				}
			case "persist":
				dir := t.TempDir()
				p := filepath.Join(dir, "file")
				if err := os.WriteFile(p, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				a.WithStateDir(p)
			case "cancel":
				cancel()
			}
			if a.PollWake(ctx) || a.PollWake(ctx) {
				t.Fatal("failed scan authorized early check")
			}
		})
	}
}
