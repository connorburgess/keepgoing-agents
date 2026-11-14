package bb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
		case "list":
			return []byte(`[]`), nil
		case "show":
			if fail {
				return []byte(show), errors.New("exit 1")
			}
			return []byte(show), nil
		case "log":
			return pageLog(log, args), nil
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

// pageLog behaves like bb: events oldest first, numbered from 1 when the
// fixture leaves seq out, filtered by --after-seq, and capped by --limit.
func pageLog(log string, args []string) []byte {
	var events []map[string]any
	if json.Unmarshal([]byte(log), &events) != nil {
		return []byte(log)
	}
	after, limit := int64(0), 100
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--after-seq":
			after, _ = strconv.ParseInt(args[i+1], 10, 64)
		case "--limit":
			limit, _ = strconv.Atoi(args[i+1])
		}
	}
	var out []map[string]any
	for i, e := range events {
		if _, ok := e["seq"]; !ok {
			e["seq"] = i + 1
		}
		seq := int64(0)
		switch v := e["seq"].(type) {
		case float64:
			seq = int64(v)
		case int:
			seq = int64(v)
		}
		if seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	b, _ := json.Marshal(out)
	return b
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

// A forbidden provider error is the provider's decision about the account or
// the work. It is its own class, so it can never be treated as a login to
// fail over from.
func TestObserveForbiddenIsRestricted(t *testing.T) {
	for _, tc := range []struct {
		event string
		want  decide.ErrorClass
	}{
		// The shapes BB's bundled Claude Code provider (BB 0.48.1) produces:
		// it gives HTTP 401 and 403 the same category, "unauthorized", and
		// the code "unknown" when it classified from the status alone. Read
		// from BB's provider code, not captured from a live thread.
		{providerError("unauthorized", 403, "unknown", nil), decide.ErrRestricted},
		{providerError("unauthorized", 403, "authentication_failed", nil), decide.ErrRestricted},
		{providerError("unauthorized", nil, "oauth_org_not_allowed", nil), decide.ErrRestricted},
		{providerError("unauthorized", 401, "unknown", nil), decide.ErrAuth},
		{providerError("unauthorized", 401, "authentication_failed", nil), decide.ErrAuth},
		{providerError("unauthorized", nil, "authentication_failed", nil), decide.ErrAuth},
		{providerError("unknown", nil, "permission_denied", nil), decide.ErrRestricted},
		// A restriction outranks a limit reported beside it.
		{providerError("rate-limit", 403, "rate_limit_event", nil), decide.ErrRestricted},
	} {
		a, _ := newFake(`{"thread":{"status":"error"}}`, "["+turnStarted+","+tc.event+"]", false)
		obs, _ := a.Observe(context.Background())
		if obs.State != decide.Error || obs.ErrorClass != tc.want {
			t.Errorf("%s: %+v, want %s", tc.event, obs, tc.want)
		}
	}
}

// BB's typed category for a provider's policy block, which its Codex provider
// reports for cyberPolicy and misalignmentPolicyViolation, is a refusal.
// An errored turn is named by its thread and the sequence of its start, so
// the supervisor counts a refused turn once however many checks see it.
func TestObserveNamesTheErroredTurn(t *testing.T) {
	a, _ := newFake(`{"thread":{"status":"error"}}`, "["+turnStarted+","+providerError("policy", nil, "cyberPolicy", nil)+"]", false)
	obs, _ := a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrRefused || obs.ErrorTurn != "thr_example123/bb-turn-1" {
		t.Fatalf("%+v", obs)
	}
	a, _ = newFake(`{"thread":{"status":"idle"}}`, "["+turnStarted+"]", false)
	if obs, _ := a.Observe(context.Background()); obs.ErrorTurn != "" {
		t.Fatalf("an idle thread: %+v", obs)
	}
}

func TestObservePolicyBlockIsRefused(t *testing.T) {
	for _, event := range []string{
		providerError("policy", nil, "cyberPolicy", nil),
		providerError("policy", nil, "misalignmentPolicyViolation", nil),
		providerError("policy", nil, "", nil),
		providerError("policy", 429, "rate_limit_event", nil),
	} {
		a, _ := newFake(`{"thread":{"status":"error"}}`, "["+turnStarted+","+event+"]", false)
		obs, _ := a.Observe(context.Background())
		if obs.State != decide.Error || obs.ErrorClass != decide.ErrRefused {
			t.Errorf("%s: %+v", event, obs)
		}
	}
}

// BB records provider/modelFallback when the provider answered with another
// model. Only a fallback after a refusal is reported, by its position in the
// log, and it changes nothing else about the observation.
func TestObserveModelFallbackAfterARefusal(t *testing.T) {
	fallback := func(reason string) string {
		return `{"type":"provider/modelFallback","data":{"providerThreadId":"p1","originalModel":"model-a","fallbackModel":"model-b","reason":"` + reason + `","message":"PRIVATE_TEXT"}}`
	}
	for _, tc := range []struct {
		name string
		log  string
		want int64
	}{
		{"after a refusal", "[" + turnStarted + "," + fallback("refusal") + "]", 2},
		{"for another reason", "[" + turnStarted + "," + fallback("provider") + "]", 0},
		{"none", "[" + turnStarted + "]", 0},
	} {
		a, _ := newFake(`{"thread":{"status":"idle"}}`, tc.log, false)
		obs, _ := a.Observe(context.Background())
		if obs.ModelFallback != tc.want || obs.State != decide.Idle || obs.ErrorClass != decide.ErrNone {
			t.Errorf("%s: %+v", tc.name, obs)
		}
	}
}

// A refusal or restriction stays for the rest of its turn: a later error
// event does not replace it, so it cannot become a limit that hands off. A
// restriction outranks a refusal, a new turn clears both, and so does the
// model answering again.
func TestRefusalOrRestrictionIsStickyForTheTurn(t *testing.T) {
	refusal := providerError("policy", nil, "cyberPolicy", nil)
	forbidden := providerError("unauthorized", 403, "unknown", nil)
	limit := providerError("rate-limit", 429, "rate_limit_event", false)
	systemErr := `{"type":"system/error","data":{"code":"rate_limit_error"}}`
	failed := `{"type":"turn/failed","data":{"code":"overloaded_error"}}`
	answer := `{"type":"item/agentMessage/delta","data":{"providerThreadId":"p1","itemId":"item_example"}}`
	for _, tc := range []struct {
		name   string
		events []string
		want   decide.ErrorClass
	}{
		{"refusal, then a rate limit that will not be retried", []string{refusal, limit}, decide.ErrRefused},
		{"refusal, then a system error", []string{refusal, systemErr}, decide.ErrRefused},
		{"refusal, then a failed event", []string{refusal, failed}, decide.ErrRefused},
		{"restriction, then a rate limit", []string{forbidden, limit}, decide.ErrRestricted},
		{"restriction, then a system error", []string{forbidden, systemErr}, decide.ErrRestricted},
		{"restriction, then a refusal", []string{forbidden, refusal}, decide.ErrRestricted},
		{"refusal, then a restriction", []string{refusal, forbidden}, decide.ErrRestricted},
		{"a rate limit, then a refusal", []string{limit, refusal}, decide.ErrRefused},
		{"a rate limit, then a restriction", []string{limit, forbidden}, decide.ErrRestricted},
		{"refusal, then a new turn with a rate limit", []string{refusal, turnStarted, limit}, decide.ErrRateLimit},
		{"refusal, then the model answers", []string{refusal, answer}, decide.ErrNone},
		{"a rate limit, then a system error (unchanged: the later error decides)", []string{limit, systemErr}, decide.ErrRateLimit},
	} {
		log := "[" + turnStarted + "," + strings.Join(tc.events, ",") + "]"
		a, _ := newFake(`{"thread":{"status":"error"}}`, log, false)
		obs, _ := a.Observe(context.Background())
		got := obs.ErrorClass
		if tc.want == decide.ErrNone && got == decide.ErrUnknown {
			got = decide.ErrNone // no error left: the thread status alone says error
		}
		if got != tc.want {
			t.Errorf("%s: %s, want %s (%+v)", tc.name, obs.ErrorClass, tc.want, obs)
		}
	}
}

// A summary saved by an earlier build keeps the class it derived then. One
// that cached a forbidden error as auth is derived again from the log.
func TestSummaryFromAnEarlierBuildIsDerivedAgain(t *testing.T) {
	log := "[" + turnStarted + "," + providerError("unauthorized", 403, "unknown", nil) + "]"
	a, _ := newFake(`{"thread":{"status":"error"}}`, log, false)
	stale := Cursor{Thread: "thr_example123", RetrySchema: 1, LastSeq: 2, Error: &errorSignal{Class: decide.ErrAuth, Seq: 2}}
	a.mem = &stale
	obs, _ := a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrRestricted {
		t.Fatalf("a cached auth class survived the upgrade: %+v", obs)
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

func TestLongThreadReadsTheNewestEvents(t *testing.T) {
	// 1200 old events, then a turn and a structured rate limit. The old code
	// read the first 60 events and missed the error entirely.
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 1200; i++ {
		b.WriteString(`{"type":"message","data":{"text":"old"}},`)
	}
	b.WriteString(turnStarted + "," + providerError("rate-limit", 429, "rate_limit_event", nil) + "]")
	a, _ := newFake(`{"thread":{"status":"error"}}`, b.String(), false)
	obs, _ := a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrRateLimit {
		t.Fatalf("newest error not found: %+v", obs)
	}
}

func TestCursorPersistsAndReadsOnlyNewEvents(t *testing.T) {
	dir := t.TempDir()
	log := "[" + turnStarted + `,{"type":"message","data":{}}]`
	a, calls := newFake(`{"thread":{"status":"idle"}}`, log, false)
	a.WithStateDir(dir)
	a.Observe(context.Background())
	// A fresh adapter, as on the next tick, resumes after seq 2.
	log2 := "[" + turnStarted + `,{"type":"message","data":{}},` + providerError("rate-limit", 429, "", nil) + "]"
	b, calls2 := newFake(`{"thread":{"status":"error"}}`, log2, false)
	b.WithStateDir(dir)
	obs, _ := b.Observe(context.Background())
	if obs.ErrorClass != decide.ErrRateLimit {
		t.Errorf("new error not seen: %+v", obs)
	}
	var after []string
	for _, c := range append(*calls, *calls2...) {
		if len(c.args) > 1 && c.args[1] == "log" {
			after = append(after, c.args[5])
		}
	}
	if len(after) < 2 || after[0] != "0" || after[len(after)-1] != "2" {
		t.Errorf("--after-seq values = %v, want 0 then 2", after)
	}
}
