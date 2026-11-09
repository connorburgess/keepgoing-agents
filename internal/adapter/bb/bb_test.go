package bb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

type call struct {
	stdin string
	args  []string
}

func newFake(show, log string, fail bool) (*Adapter, *[]call) {
	return newFakeWithInteractions(show, log, `[]`, fail)
}

func newFakeWithInteractions(show, log, interactions string, fail bool) (*Adapter, *[]call) {
	a := New(config.BB{Thread: "thr_example123"})
	a.now = func() time.Time { return now }
	var calls []call
	a.run = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		calls = append(calls, call{stdin, args})
		switch args[1] {
		case "show":
			if fail {
				return []byte(show), errors.New("exit 1")
			}
			return []byte(show), nil
		case "log":
			return []byte(log), nil
		case "interactions":
			if interactions == "error" {
				return []byte(`{"ok":false,"error":{"code":"unknown_command"}}`), errors.New("exit 1")
			}
			return []byte(interactions), nil
		}
		return []byte(`{"ok":true}`), nil
	}
	return a, &calls
}

func TestObserveIdle(t *testing.T) {
	a, _ := newFake(`{"thread":{"status":"idle","queuedMessageCount":1,"archivedAt":null,"deletedAt":null}}`, `[]`, false)
	obs, err := a.Observe(context.Background())
	if err != nil || obs.State != decide.Idle || obs.Queued != 1 || obs.Gone {
		t.Errorf("%+v %v", obs, err)
	}
}

// Fixtures below are synthetic and follow the field shapes of real bb events.
const turnStarted = `{"type":"turn/started","data":{"providerThreadId":"p1"}}`

func providerError(category string, status any, code string, willRetry any) string {
	return fmt.Sprintf(`{"type":"provider/error","data":{"message":"Usage limit reached, see you later","detail":"x","willRetry":%v,"errorInfo":{"category":%q,"httpStatusCode":%v,"providerCode":%q}}}`,
		jsonv(willRetry), category, jsonv(status), code)
}

func jsonv(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprint(v)
}

func TestObserveStructuredRateLimit(t *testing.T) {
	log := "[" + turnStarted + "," + providerError("rate-limit", 429, "rate_limit_event", nil) + "]"
	a, _ := newFake(`{"thread":{"status":"error"}}`, log, false)
	obs, _ := a.Observe(context.Background())
	if obs.State != decide.Error || obs.ErrorClass != decide.ErrRateLimit || obs.Retrying {
		t.Errorf("%+v", obs)
	}
}

func TestObserveStreamDisconnectIsNetworkAndRetrying(t *testing.T) {
	log := "[" + turnStarted + "," + providerError("stream-disconnected", nil, "responseStreamDisconnected", true) + "]"
	a, _ := newFake(`{"thread":{"status":"error"}}`, log, false)
	obs, _ := a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrNetwork || !obs.Retrying {
		t.Errorf("%+v", obs)
	}
}

func TestObserveProseNeverClassifies(t *testing.T) {
	// The message says "usage limit" but every structured field is unknown.
	log := "[" + turnStarted + "," + providerError("unknown", nil, "unknown", true) + "]"
	a, _ := newFake(`{"thread":{"status":"error"}}`, log, false)
	obs, _ := a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrUnknown {
		t.Errorf("prose classified as %s", obs.ErrorClass)
	}
	log = `[` + turnStarted + `,{"type":"message","data":{"text":"we hit the usage limit, 429"}}]`
	a, _ = newFake(`{"thread":{"status":"error"}}`, log, false)
	obs, _ = a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrUnknown {
		t.Errorf("message text classified as %s", obs.ErrorClass)
	}
}

func TestObserveIgnoresErrorsFromEarlierTurns(t *testing.T) {
	log := "[" + providerError("rate-limit", 429, "rate_limit_event", nil) + "," + turnStarted + `,{"type":"system/error","data":{"code":"thread_command_failed","message":"x"}}]`
	a, _ := newFake(`{"thread":{"status":"error"}}`, log, false)
	obs, _ := a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrUnknown {
		t.Errorf("stale error leaked: %s", obs.ErrorClass)
	}
}

func rateLimitsEvent(status string, resets time.Time) string {
	return fmt.Sprintf(`{"type":"provider/rateLimits/updated","data":{"providerThreadId":"p1","rateLimits":{"kind":"subscription-window","providerId":"claude-code","status":%q,"windows":[{"label":"Weekly","providerKey":"seven_day","status":%q,"resetsAtMs":%d}]}}}`,
		status, status, resets.UnixMilli())
}

func TestObserveBlockedWindowOnIdleThread(t *testing.T) {
	reset := now.Add(3 * time.Hour)
	a, _ := newFake(`{"thread":{"status":"idle"}}`, "["+turnStarted+","+rateLimitsEvent("blocked", reset)+"]", false)
	obs, _ := a.Observe(context.Background())
	if obs.State != decide.Error || obs.ErrorClass != decide.ErrRateLimit || !obs.ResetsAt.Equal(reset) {
		t.Errorf("%+v", obs)
	}
	// A blocked window that has already reset means nothing.
	a, _ = newFake(`{"thread":{"status":"idle"}}`, "["+rateLimitsEvent("blocked", now.Add(-time.Hour))+"]", false)
	obs, _ = a.Observe(context.Background())
	if obs.State != decide.Idle {
		t.Errorf("expired block: %+v", obs)
	}
	// An allowed window is not a limit.
	a, _ = newFake(`{"thread":{"status":"idle"}}`, "["+rateLimitsEvent("allowed", reset)+"]", false)
	obs, _ = a.Observe(context.Background())
	if obs.State != decide.Idle {
		t.Errorf("allowed window: %+v", obs)
	}
}

func TestObserveGoneArchivedAndFailures(t *testing.T) {
	a, _ := newFake(`{"ok":false,"error":{"code":"thread_not_found","message":"no"}}`, "", true)
	if obs, err := a.Observe(context.Background()); err != nil || !obs.Gone {
		t.Errorf("not found: %+v %v", obs, err)
	}
	a, _ = newFake(`{"thread":{"status":"idle","archivedAt":1794000000000}}`, "", false)
	if obs, _ := a.Observe(context.Background()); !obs.Gone {
		t.Error("archived thread should be gone")
	}
	a, _ = newFake(``, "", true)
	if obs, err := a.Observe(context.Background()); err == nil || obs.State != decide.Unknown {
		t.Errorf("cli failure: %+v %v", obs, err)
	}
}

func TestPendingInteractionMeansWaitingOnAPerson(t *testing.T) {
	// Synthetic, following the field names bb reports.
	a, _ := newFakeWithInteractions(`{"thread":{"status":"idle"}}`, `[]`, `[{"id":"pint_example1","status":"pending"}]`, false)
	if obs, _ := a.Observe(context.Background()); !obs.AwaitingInput {
		t.Errorf("idle with a pending interaction: %+v", obs)
	}
	a, _ = newFakeWithInteractions(`{"thread":{"status":"active"}}`, `[]`, `[{"id":"pint_example1","status":"pending"}]`, false)
	if obs, _ := a.Observe(context.Background()); !obs.AwaitingInput || obs.State != decide.Active {
		t.Errorf("active with a pending interaction: %+v", obs)
	}
	a, _ = newFakeWithInteractions(`{"thread":{"status":"idle"}}`, `[]`, `[{"id":"pint_example1","status":"resolved"}]`, false)
	if obs, _ := a.Observe(context.Background()); obs.AwaitingInput {
		t.Errorf("resolved interaction: %+v", obs)
	}
}

func TestPendingInteractionFallsBackToLifecycleEvents(t *testing.T) {
	lifecycle := func(id, status string) string {
		return fmt.Sprintf(`{"type":"system/interaction/lifecycle","data":{"interaction":{"id":%q,"status":%q,"origin":{"kind":"plugin"}}}}`, id, status)
	}
	log := "[" + lifecycle("pint_a", "pending") + "," + lifecycle("pint_a", "resolved") + "," + lifecycle("pint_b", "pending") + "]"
	a, _ := newFakeWithInteractions(`{"thread":{"status":"idle"}}`, log, "error", false)
	if obs, _ := a.Observe(context.Background()); !obs.AwaitingInput {
		t.Errorf("pint_b is still pending: %+v", obs)
	}
	log = "[" + lifecycle("pint_a", "pending") + "," + lifecycle("pint_a", "resolved") + "]"
	a, _ = newFakeWithInteractions(`{"thread":{"status":"idle"}}`, log, "error", false)
	if obs, _ := a.Observe(context.Background()); obs.AwaitingInput {
		t.Errorf("everything resolved: %+v", obs)
	}
}

func TestSendStopActivate(t *testing.T) {
	a, calls := newFake(``, ``, false)
	a.cfg.Model, a.cfg.Reasoning = "claude-fable-5-1", "medium"
	ctx := context.Background()
	if err := a.Send(ctx, "keep going", adapter.ModeQueue); err != nil {
		t.Fatal(err)
	}
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"thread tell thr_example123 --message-file - --mode queue --json",
		"thread stop thr_example123 --json",
		"thread update thr_example123 --json --model claude-fable-5-1 --reasoning-level medium",
	}
	for i, w := range want {
		if got := strings.Join((*calls)[i].args, " "); got != w {
			t.Errorf("call %d = %q want %q", i, got, w)
		}
	}
	if (*calls)[0].stdin != "keep going" {
		t.Errorf("stdin = %q", (*calls)[0].stdin)
	}
	a.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"ok":false,"error":{"code":"thread_not_found"}}`), errors.New("exit 1")
	}
	if err := a.Send(ctx, "x", adapter.ModeAuto); err == nil || !strings.Contains(err.Error(), "thread_not_found") {
		t.Errorf("err = %v", err)
	}
}
