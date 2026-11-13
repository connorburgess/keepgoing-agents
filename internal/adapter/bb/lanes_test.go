package bb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

const (
	laneID     = "thr_lane_example1"
	laneParent = "thr_example123"
)

// laneWorld is a stand-in bb for one lane of the supervised thread. Each
// field is what a command returns; an empty string makes it fail.
type laneWorld struct {
	show, interactions, log, explain, pending, retry string
	calls                                            []string
}

func laneShow(status string, extra string) string {
	return fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":%q,"status":%q%s}}`, laneID, laneParent, status, extra)
}

// newLane is a lane in error after a lost connection: its turn started ten
// minutes ago, failed with a network error, and nothing is queued or pending.
func newLane(t *testing.T) (*Adapter, *laneWorld) {
	t.Helper()
	start := now.Add(-10 * time.Minute)
	w := &laneWorld{
		show:         laneShow("error", ""),
		interactions: `[]`,
		log:          `[` + at(turnStarted, start) + `,` + at(providerError("network", nil, "econnreset", false), start.Add(time.Minute)) + `,` + at(turnFailed, start.Add(2*time.Minute)) + `]`,
		explain:      fmt.Sprintf(`{"threadId":%q,"diagnostic":null}`, laneID),
		pending:      `{"retries":[]}`,
		retry:        fmt.Sprintf(`{"ok":true,"threadId":%q,"delivery":"sent","turnRequestId":"req_example1","attempt":2}`, laneID),
	}
	a := New(config.BB{Thread: laneParent})
	a.now = func() time.Time { return now }
	a.WithStateDir(t.TempDir())
	a.run = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		w.calls = append(w.calls, strings.Join(args, " "))
		if stdin != "" {
			t.Errorf("%v was given stdin", args)
		}
		out := ""
		switch {
		case args[0] == "thread" && args[1] == "show":
			out = w.show
		case args[0] == "thread" && args[1] == "interactions":
			out = w.interactions
		case args[0] == "thread" && args[1] == "log":
			return pageLog(w.log, args), nil
		case args[0] == "provider-retry" && args[1] == "explain":
			out = w.explain
		case args[0] == "provider-retry" && args[1] == "status":
			out = w.pending
		case args[0] == "thread" && args[1] == "retry":
			out = w.retry
		default:
			t.Errorf("unexpected bb command %v", args)
		}
		if out == "" {
			return nil, errors.New("exit status 1")
		}
		return []byte(out), nil
	}
	return a, w
}

func TestInspectLaneEstablishesEachFact(t *testing.T) {
	a, w := newLane(t)
	f := a.InspectLane(context.Background(), laneID)
	want := decide.LaneFacts{Verified: true, Errored: true, InputKnown: true, ClassKnown: true, Class: decide.ErrNetwork}
	if f != want || decide.LaneVerdictFor(f) != decide.LaneRetry {
		t.Fatalf("%+v", f)
	}
	// Status, pending interactions and the log, in that order, and nothing else.
	if got := strings.Join(w.calls, "\n"); got != "thread show "+laneID+" --json\nthread interactions list "+laneID+" --json\nthread log "+laneID+" --json --after-seq 0 --limit 500" {
		t.Fatalf("calls:\n%s", got)
	}

	start := now.Add(-10 * time.Minute)
	notice := `[` + at(turnStarted, start) + `,` + at(turnFailed, start.Add(2*time.Minute)) + `,` + at(retryNotice, now.Add(-2*time.Minute)) + `]`
	for name, tc := range map[string]struct {
		change  func(w *laneWorld)
		verdict decide.LaneVerdict
	}{
		"another thread's child": {func(w *laneWorld) { w.show = strings.Replace(w.show, laneParent, "thr_example999", 1) }, decide.LaneUnverified},
		"no parent": {func(w *laneWorld) {
			w.show = fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":null,"status":"error"}}`, laneID)
		}, decide.LaneUnverified},
		"a different thread's status":     {func(w *laneWorld) { w.show = strings.Replace(w.show, laneID, "thr_lane_example2", 1) }, decide.LaneUnverified},
		"archived":                        {func(w *laneWorld) { w.show = laneShow("error", `,"archivedAt":1700000000000`) }, decide.LaneUnverified},
		"deleted":                         {func(w *laneWorld) { w.show = laneShow("error", `,"deletedAt":1700000000000`) }, decide.LaneUnverified},
		"status unreadable":               {func(w *laneWorld) { w.show = "" }, decide.LaneUnverified},
		"status is an error envelope":     {func(w *laneWorld) { w.show = `{"ok":false,"error":{"code":"thread_not_found"}}` }, decide.LaneUnverified},
		"status is not JSON":              {func(w *laneWorld) { w.show = "PRIVATE_LANE_TEXT" }, decide.LaneUnverified},
		"working again":                   {func(w *laneWorld) { w.show = laneShow("active", "") }, decide.LaneNotInError},
		"idle again":                      {func(w *laneWorld) { w.show = laneShow("idle", "") }, decide.LaneNotInError},
		"a retry is queued":               {func(w *laneWorld) { w.show = laneShow("error", `,"queuedMessageCount":1`) }, decide.LaneQueued},
		"waiting on a person":             {func(w *laneWorld) { w.interactions = fmt.Sprintf(`[{"threadId":%q,"status":"pending"}]`, laneID) }, decide.LaneWaitingPerson},
		"interactions unreadable":         {func(w *laneWorld) { w.interactions = "" }, decide.LaneWaitingPerson},
		"interactions for another thread": {func(w *laneWorld) { w.interactions = `[{"threadId":"thr_lane_example2","status":"pending"}]` }, decide.LaneWaitingPerson},
		"log unreadable":                  {func(w *laneWorld) { w.log = "{" }, decide.LaneUnknown},
		"rate limited": {func(w *laneWorld) {
			w.log = `[` + at(turnStarted, start) + `,` + at(providerError("rate-limit", 429, "rate_limit_error", false), start.Add(time.Minute)) + `]`
		}, decide.LaneRateLimited},
		"needs a login": {func(w *laneWorld) {
			w.log = `[` + at(turnStarted, start) + `,` + at(providerError("unauthorized", 401, "authentication_error", false), start.Add(time.Minute)) + `]`
		}, decide.LaneAuth},
		"overloaded": {func(w *laneWorld) {
			w.log = `[` + at(turnStarted, start) + `,` + at(providerError("overloaded", 529, "overloaded_error", false), start.Add(time.Minute)) + `]`
		}, decide.LaneRetry},
		"unknown failure": {func(w *laneWorld) {
			w.log = `[` + at(turnStarted, start) + `,` + at(providerError("unknown", nil, "unknown", false), start.Add(time.Minute)) + `]`
		}, decide.LaneRetry},
		"no error event at all":                {func(w *laneWorld) { w.log = `[` + at(turnStarted, start) + `]` }, decide.LaneRetry},
		"the provider still announces a retry": {func(w *laneWorld) { w.log = notice }, decide.LaneRetrying},
		"a retry notice BB declined": {func(w *laneWorld) {
			w.log = notice
			w.explain = fmt.Sprintf(`{"threadId":%q,"diagnostic":{"requestId":"creq_example1","observedAt":%d,"availability":{"kind":"not-routed"},"decision":{"kind":"decline","reason":"not-retryable"}}}`, laneID, start.Add(2*time.Minute).UnixMilli())
		}, decide.LaneRetry},
		"a retry notice that aged out": {func(w *laneWorld) {
			w.log = `[` + at(turnStarted, start.Add(-time.Hour)) + `,` + at(turnFailed, start.Add(-58*time.Minute)) + `,` + at(retryNotice, now.Add(-20*time.Minute)) + `]`
		}, decide.LaneRetry},
	} {
		a, w := newLane(t)
		tc.change(w)
		f := a.InspectLane(context.Background(), laneID)
		if got := decide.LaneVerdictFor(f); got != tc.verdict {
			t.Errorf("%s: %s, want %s (%+v)", name, got, tc.verdict, f)
		}
		for _, c := range w.calls {
			if strings.HasPrefix(c, "thread tell") || strings.HasPrefix(c, "thread stop") || strings.HasPrefix(c, "thread retry") || strings.HasPrefix(c, "thread spawn") || strings.HasPrefix(c, "thread update") {
				t.Errorf("%s: inspection ran %q", name, c)
			}
		}
	}
	// IDs that are not a lane's: malformed, or the supervised thread itself.
	for _, id := range []string{"", "not-a-thread", laneParent, "thr_x --json; rm"} {
		a, w := newLane(t)
		if f := a.InspectLane(context.Background(), id); f != (decide.LaneFacts{}) || len(w.calls) != 0 {
			t.Errorf("%q: %+v %v", id, f, w.calls)
		}
		if _, err := a.RetryLane(context.Background(), id); err == nil || len(w.calls) != 0 {
			t.Errorf("%q was retried: %v %v", id, err, w.calls)
		}
	}
}

func TestRetryLaneRetriesThatLaneByReference(t *testing.T) {
	a, w := newLane(t)
	res, err := a.RetryLane(context.Background(), laneID)
	if err != nil || res != (adapter.TurnRetry{Outcome: adapter.TurnRetried, Attempt: 2}) {
		t.Fatalf("%+v %v", res, err)
	}
	if len(w.calls) != 1 || w.calls[0] != "thread retry "+laneID+" --reason "+retryReason+" --json" {
		t.Fatalf("%v", w.calls)
	}
	var _ adapter.LaneRecoverer = a
}

// laneText is planted in every field of a lane's log that holds what was
// said or done in the lane. None of it may be decoded into what the adapter
// returns, or reach what it keeps between checks.
const laneText = "PRIVATE_LANE_TEXT"

func laneLogWithText(start time.Time) string {
	events := []string{
		at(`{"type":"client/turn/requested","data":{"requestId":"creq_private1","initiator":"user","input":[{"type":"text","text":"`+laneText+`"}],"title":"`+laneText+`"}}`, start),
		at(`{"type":"turn/started","data":{"providerThreadId":"provider_private1","title":"`+laneText+`"}}`, start),
		at(`{"type":"item/started","data":{"providerThreadId":"provider_private1","item":{"id":"item_private1","type":"agentMessage","text":"`+laneText+`"}}}`, start.Add(time.Minute)),
		at(`{"type":"item/agentMessage/delta","data":{"providerThreadId":"provider_private1","itemId":"item_private1","delta":"`+laneText+`"}}`, start.Add(time.Minute)),
		at(`{"type":"item/toolCall/completed","data":{"providerThreadId":"provider_private1","output":"`+laneText+`","command":"`+laneText+`"}}`, start.Add(2*time.Minute)),
		at(`{"type":"provider/error","data":{"providerThreadId":"provider_private1","message":"`+laneText+`","detail":"`+laneText+`","willRetry":false,"errorInfo":{"category":"network","httpStatusCode":null,"providerCode":"econnreset","message":"`+laneText+`"}}}`, start.Add(3*time.Minute)),
		at(`{"type":"system/error","data":{"code":"thread_command_failed","message":"`+laneText+`","detail":"`+laneText+`"}}`, start.Add(4*time.Minute)),
		at(`{"type":"turn/completed","data":{"providerThreadId":"provider_private1","status":"failed","summary":"`+laneText+`"}}`, start.Add(5*time.Minute)),
	}
	return `[` + strings.Join(events, ",") + `]`
}

func TestLaneMessageTextIsNeverDecodedOrStored(t *testing.T) {
	a, w := newLane(t)
	w.log = laneLogWithText(now.Add(-10 * time.Minute))
	w.show = fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":%q,"status":"error","title":%q,"titleFallback":%q}}`, laneID, laneParent, laneText, laneText)
	w.interactions = fmt.Sprintf(`[{"threadId":%q,"status":"resolved","prompt":%q,"response":%q}]`, laneID, laneText, laneText)
	f := a.InspectLane(context.Background(), laneID)
	if decide.LaneVerdictFor(f) != decide.LaneRetry {
		t.Fatalf("%+v", f)
	}
	// What the adapter returns and what it holds in memory.
	if strings.Contains(fmt.Sprintf("%+v", f), laneText) {
		t.Fatalf("facts hold lane text: %+v", f)
	}
	inMemory, _ := json.Marshal(a.lanes)
	// What it keeps between checks.
	entries, err := os.ReadDir(a.stateDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("%v %v", entries, err)
	}
	stored := string(inMemory)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(a.stateDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		stored += string(b)
	}
	for _, private := range []string{laneText, laneID, "provider_private1", "item_private1", "creq_private1"} {
		if strings.Contains(stored, private) {
			t.Errorf("%q is kept between checks:\n%s", private, stored)
		}
	}
	// The summary still did its job: the lane is filed under a hash.
	if _, ok := a.lanes[a.laneHash(laneID)]; !ok || !a.lanes[a.laneHash(laneID)].Complete {
		t.Fatalf("%+v", a.lanes)
	}
}

// Every field the log reader, the lane inspection and the retry plugin decode
// from BB is listed here. A new field fails this test, so decoding anything
// that could hold what was said in a conversation cannot happen unnoticed.
func TestDecodedFieldsAreAnAllowlist(t *testing.T) {
	allowed := strings.Fields(`
		archivedAt attempt category code complete count createdAt cursor data decision deletedAt delivery
		diagnostic error errorInfo hashRefs httpStatusCode id initiator interaction item itemId kind lastEventAt lastSeq
		observedAt ok outcomes parentThreadId pending providerCode providerThread providerThreadId queuedMessageCount
		rateLimits resetsAt resetsAtMs retries retryOfRequestId retrySchema scannedAt seq status systemMessageKind
		systemMessageSubject thread threadId turnSeq turnStartedMs type willRetry windows childOutcomeSeq blocked key lanes
		evidence at class retryKnown activeBackgroundAgentCount`)
	want := map[string]bool{}
	for _, tag := range allowed {
		want[tag] = true
	}
	got := map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range []string{"cursor.go", "lanes.go", "retry_plugin.go", "children.go", "wake.go", "bb.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok || field.Tag == nil {
				return true
			}
			tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Get("json")
			if name := strings.Split(tag, ",")[0]; name != "" && name != "-" {
				got[name] = true
			}
			return true
		})
	}
	var unexpected, unused []string
	for tag := range got {
		if !want[tag] {
			unexpected = append(unexpected, tag)
		}
	}
	for tag := range want {
		if !got[tag] {
			unused = append(unused, tag)
		}
	}
	sort.Strings(unexpected)
	sort.Strings(unused)
	if len(unexpected) > 0 || len(unused) > 0 {
		t.Fatalf("the BB adapter decodes fields %v that are not in the allowlist, and the allowlist names %v that it does not decode. A field that can hold message text, a title or tool output must never be added.", unexpected, unused)
	}
	for _, text := range []string{"text", "message", "content", "delta", "input", "output", "title", "detail", "prompt", "body", "summary", "reason", "command", "response"} {
		if got[text] {
			t.Errorf("the BB adapter decodes %q", text)
		}
	}
}

// A long log is read over several checks. Until its end has been read the
// lane's failure is unknown and it is not retried.
func TestLongLaneLogIsReadAcrossChecks(t *testing.T) {
	a, w := newLane(t)
	start := now.Add(-time.Hour)
	events := []string{at(turnStarted, start)}
	for i := 0; i < lanePages*pageSize+20; i++ {
		events = append(events, at(`{"type":"item/toolCall/completed","data":{}}`, start.Add(time.Second)))
	}
	events = append(events, at(providerError("network", nil, "econnreset", false), now.Add(-30*time.Minute)), at(turnFailed, now.Add(-29*time.Minute)))
	w.log = `[` + strings.Join(events, ",") + `]`
	f := a.InspectLane(context.Background(), laneID)
	if decide.LaneVerdictFor(f) != decide.LaneUnknown || f.ClassKnown {
		t.Fatalf("first check: %+v", f)
	}
	logCalls := 0
	for _, c := range w.calls {
		if strings.HasPrefix(c, "thread log") {
			logCalls++
		}
	}
	if logCalls != lanePages {
		t.Fatalf("%d log pages read", logCalls)
	}
	// A new adapter, as after a restart, continues from the saved progress.
	b := New(config.BB{Thread: laneParent})
	b.now, b.run = a.now, a.run
	b.WithStateDir(a.stateDir)
	w.calls = nil
	f = b.InspectLane(context.Background(), laneID)
	if decide.LaneVerdictFor(f) != decide.LaneRetry || f.Class != decide.ErrNetwork {
		t.Fatalf("second check: %+v", f)
	}
	if !strings.Contains(strings.Join(w.calls, "\n"), fmt.Sprintf("--after-seq %d ", lanePages*pageSize)) {
		t.Fatalf("did not continue from the saved progress:\n%s", strings.Join(w.calls, "\n"))
	}
}

// A lane is filed under a hash keyed with a secret of this loop's own state:
// the same lane has a different name in another loop, the unkeyed hash of its
// ID appears nowhere, and switching lane recovery off removes the file.
func TestLaneHashesAreKeyedPerLoopAndForgotten(t *testing.T) {
	a, _ := newLane(t)
	b, _ := newLane(t)
	if f := a.InspectLane(context.Background(), laneID); decide.LaneVerdictFor(f) != decide.LaneRetry {
		t.Fatalf("%+v", f)
	}
	b.InspectLane(context.Background(), laneID)
	ha, hb := a.laneHash(laneID), b.laneHash(laneID)
	if ha == hb || len(ha) != 24 {
		t.Fatalf("the same lane is filed as %q in two loops (%q)", ha, hb)
	}
	path := filepath.Join(a.stateDir, "bb-lanes.json")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unkeyed := decide.ChildHashes([]string{laneID})[0]
	if !strings.Contains(string(stored), ha) || strings.Contains(string(stored), unkeyed) || strings.Contains(string(stored), hb) {
		t.Fatalf("%s", stored)
	}
	// A restart reads the key back and finds the same summary.
	again := New(config.BB{Thread: laneParent})
	again.WithStateDir(a.stateDir)
	if again.laneHash(laneID) != ha || !again.loadLanes()[ha].Complete {
		t.Fatal("the key or the summary did not survive a restart")
	}
	// A file that is not this format is not trusted: a new key, nothing kept.
	if err := os.WriteFile(path, []byte(`{"`+ha+`":{"cursor":{"lastSeq":3},"complete":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := New(config.BB{Thread: laneParent})
	old.WithStateDir(a.stateDir)
	if old.laneHash(laneID) == ha || len(old.loadLanes()) != 0 {
		t.Fatal("an unkeyed file was read as lane summaries")
	}
	a.ForgetLanes()
	if _, err := os.Stat(path); !os.IsNotExist(err) || a.lanes != nil || a.laneKey != nil {
		t.Fatalf("lane summaries were kept: %v", err)
	}
}
