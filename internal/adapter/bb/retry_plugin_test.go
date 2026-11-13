package bb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// pluginFake is an errored thread whose turn started at start, logged retry
// notices and failed, as in outageLog. explain and status are what BB's retry
// plugin answers; an empty string makes that command fail.
func pluginFake(t *testing.T, status string, start time.Time, last time.Duration, explain, pending string) (*Adapter, func() []string) {
	t.Helper()
	a, _ := newFake(fmt.Sprintf(`{"thread":{"status":%q}}`, status), outageLog(start, last), false)
	a.WithStateDir(t.TempDir())
	inner := a.run
	var plugin []string
	a.run = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		if args[0] != "provider-retry" {
			return inner(ctx, stdin, args...)
		}
		plugin = append(plugin, strings.Join(args, " "))
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > retryPluginTimeout {
			t.Errorf("%v ran without the plugin timeout", args)
		}
		if stdin != "" {
			t.Errorf("%v was given stdin", args)
		}
		out := explain
		if args[1] == "status" {
			out = pending
		}
		if out == "" {
			return nil, errors.New("exit status 1")
		}
		return []byte(out), nil
	}
	return a, func() []string { return plugin }
}

func decision(kind string, observed time.Time) string {
	extra := ""
	if kind == "retry" {
		extra = fmt.Sprintf(`,"sendAt":%d`, observed.Add(time.Minute).UnixMilli())
	}
	return fmt.Sprintf(`{"threadId":"thr_example123","diagnostic":{"requestId":"creq_example1","observedAt":%d,"availability":{"kind":"not-routed"},"decision":{"kind":%q,"reason":"PRIVATE_REASON"%s}}}`, observed.UnixMilli(), kind, extra)
}

const (
	nothingPending = `{"retries":[]}`
	onePending     = `{"retries":[{"id":"qm_example1","threadId":"thr_example123"}]}`
	explainCall    = "provider-retry explain thr_example123 --json"
	statusCall     = "provider-retry status thr_example123 --json"
)

// The observed case: the provider logged retry notices, the turn failed, BB's
// retry plugin declined to retry it, and notices kept arriving afterwards.
// Before, every check for the next fifteen minutes waited on the notice.
func TestDeclinedRetryNoticeIsDroppedAtOnce(t *testing.T) {
	start := now.Add(-10 * time.Minute)
	failed := start.Add(2 * time.Minute) // when the turn failed and the plugin decided
	// The newest notice is 4 minutes old at the check: well inside the interval.
	a, calls := pluginFake(t, "error", start, 6*time.Minute, decision("decline", failed), nothingPending)
	a.CheckAt(now)
	o, err := a.Observe(context.Background())
	if err != nil || o.State != decide.Error || o.Retrying || !o.RetryExpired || !o.RetryDeclined || o.RetryUnverified {
		t.Fatalf("%+v %v", o, err)
	}
	if got := calls(); len(got) != 2 || got[0] != explainCall || got[1] != statusCall {
		t.Fatalf("%v", got)
	}
	// The ordinary error rules now apply: a re-prompt, carried out as a turn retry.
	d := errorDecision(o, 0, 0)
	if d.Action != decide.Reprompt || decide.RecoverBy(d, o, true) != decide.RecoverByTurnRetry {
		t.Fatalf("%+v", d)
	}
	// Without the plugin's decision the same check waits, as before.
	a, _ = pluginFake(t, "error", start, 6*time.Minute, `{"threadId":"thr_example123","diagnostic":null}`, nothingPending)
	a.CheckAt(now)
	if o, _ = a.Observe(context.Background()); !o.Retrying || o.RetryExpired || o.RetryDeclined || errorDecision(o, 0, 0).Reason != "provider is retrying the turn" {
		t.Fatalf("%+v", o)
	}
}

// Anything short of a decline, for this failure, with nothing pending, keeps
// the notice. The plugin is optional and its answers are never guessed at.
func TestRetryNoticeIsKeptUnlessTheDeclineIsCertain(t *testing.T) {
	start := now.Add(-10 * time.Minute)
	failed := start.Add(2 * time.Minute)
	for name, tc := range map[string]struct {
		explain, pending string
		wantCalls        int
	}{
		"a retry is scheduled":             {decision("retry", failed), onePending, 1},
		"declined, but a retry is pending": {decision("decline", failed), onePending, 2},
		"declined before this turn began":  {decision("decline", start.Add(-time.Minute)), nothingPending, 1},
		"no decision recorded":             {`{"threadId":"thr_example123","diagnostic":null}`, nothingPending, 1},
		"another thread's answer":          {strings.Replace(decision("decline", failed), "thr_example123", "thr_example999", 1), nothingPending, 1},
		"an unknown decision kind":         {strings.Replace(decision("decline", failed), `"decline"`, `"PRIVATE_KIND"`, 1), nothingPending, 1},
		"no decision time":                 {`{"threadId":"thr_example123","diagnostic":{"decision":{"kind":"decline","reason":"x"}}}`, nothingPending, 1},
		"no decision object":               {`{"threadId":"thr_example123","diagnostic":{"observedAt":1}}`, nothingPending, 1},
		"plugin not installed":             {`{"ok":false,"error":{"code":"unknown_command","message":"unknown command 'provider-retry'"}}`, nothingPending, 1},
		"explain fails":                    {"", nothingPending, 1},
		"explain is not JSON":              {"No automatic retry decision recorded for thr_example123.", nothingPending, 1},
		"explain is an array":              {`[]`, nothingPending, 1},
		"status fails":                     {decision("decline", failed), "", 2},
		"status is not JSON":               {decision("decline", failed), "No provider retries are pending.", 2},
		"status has no list":               {decision("decline", failed), `{}`, 2},
		"status list is null":              {decision("decline", failed), `{"retries":null}`, 2},
		"status is an envelope error":      {decision("decline", failed), `{"ok":false,"error":{"code":"unknown_command"}}`, 2},
	} {
		a, calls := pluginFake(t, "error", start, 6*time.Minute, tc.explain, tc.pending)
		a.CheckAt(now)
		o, err := a.Observe(context.Background())
		if err != nil || !o.Retrying || o.RetryExpired || o.RetryDeclined {
			t.Errorf("%s: %+v %v", name, o, err)
		}
		if got := calls(); len(got) != tc.wantCalls || got[0] != explainCall {
			t.Errorf("%s: plugin calls %v", name, got)
		}
	}
	// A turn start with no usable time cannot be compared with the decision.
	a, _ := newFake(`{"thread":{"status":"error"}}`, `[`+turnStarted+`,`+at(turnFailed, now.Add(-5*time.Minute))+`,`+at(retryNotice, now.Add(-4*time.Minute))+`]`, false)
	a.WithStateDir(t.TempDir())
	inner := a.run
	a.run = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		switch {
		case args[0] != "provider-retry":
			return inner(ctx, stdin, args...)
		case args[1] == "explain":
			return []byte(decision("decline", now.Add(-3*time.Minute))), nil
		}
		return []byte(nothingPending), nil
	}
	a.CheckAt(now)
	if o, err := a.Observe(context.Background()); err != nil || !o.Retrying || o.RetryDeclined {
		t.Fatalf("%+v %v", o, err)
	}
}

// The plugin is asked only when its answer could change the check: a fresh
// notice on an errored thread.
func TestRetryPluginIsAskedOnlyForAFreshNoticeOnAnErroredThread(t *testing.T) {
	start := now.Add(-10 * time.Minute)
	decline := decision("decline", start.Add(2*time.Minute))
	// Active: the turn is running, so its notice is about live work.
	a, calls := pluginFake(t, "active", start, 6*time.Minute, decline, nothingPending)
	a.CheckAt(now)
	if o, err := a.Observe(context.Background()); err != nil || !o.Retrying || o.RetryDeclined || len(calls()) != 0 {
		t.Fatalf("active: %+v %v %v", o, err, calls())
	}
	// Idle: nothing to recover.
	a, calls = pluginFake(t, "idle", start, 6*time.Minute, decline, nothingPending)
	a.CheckAt(now)
	if o, err := a.Observe(context.Background()); err != nil || o.RetryDeclined || len(calls()) != 0 {
		t.Fatalf("idle: %+v %v %v", o, err, calls())
	}
	// Errored with no notice at all: ordinary handling already applies.
	a, _ = newFake(`{"thread":{"status":"error"}}`, `[`+at(turnStarted, start)+`,`+at(turnFailed, start.Add(time.Minute))+`]`, false)
	asked := 0
	inner := a.run
	a.run = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		if args[0] == "provider-retry" {
			asked++
		}
		return inner(ctx, stdin, args...)
	}
	a.CheckAt(now)
	if o, err := a.Observe(context.Background()); err != nil || o.Retrying || o.RetryExpired || asked != 0 {
		t.Fatalf("no notice: %+v %v asked %d", o, err, asked)
	}
}

// Outage length: the first check after the turn failed runs at each gap. A
// declined notice is dropped whenever it is still fresh; once it has aged out
// the plugin is not asked at all and the result is the same.
func TestDeclinedNoticeAfterAnOutageOfAnyLength(t *testing.T) {
	const lastNotice = 3 * time.Minute // after the turn started
	for _, gap := range []time.Duration{time.Minute, 10 * time.Minute, 20 * time.Minute, 4 * time.Hour, 72 * time.Hour, 21 * 24 * time.Hour} {
		start := now.Add(-gap - lastNotice)
		for _, declined := range []bool{true, false} {
			explain := `{"threadId":"thr_example123","diagnostic":null}`
			if declined {
				explain = decision("decline", start.Add(2*time.Minute))
			}
			a, calls := pluginFake(t, "error", start, lastNotice, explain, nothingPending)
			a.CheckAt(now)
			o, err := a.Observe(context.Background())
			aged := gap > DefaultRetrySignalTTL
			wantWaiting := !aged && !declined
			if err != nil || o.Retrying != wantWaiting || o.RetryExpired == wantWaiting || o.RetryDeclined != (declined && !aged) {
				t.Fatalf("gap %s declined %t: %+v %v", gap, declined, o, err)
			}
			if asked := len(calls()) > 0; asked == aged {
				t.Fatalf("gap %s declined %t: plugin calls %v", gap, declined, calls())
			}
			if d := errorDecision(o, 0, 0); (d.Action == decide.Reprompt) == wantWaiting {
				t.Fatalf("gap %s declined %t: %+v", gap, declined, d)
			}
		}
	}
}

// The plugin's reason text is never read into an observation or an error.
func TestRetryPluginReasonIsNotRead(t *testing.T) {
	start := now.Add(-10 * time.Minute)
	a, _ := pluginFake(t, "error", start, 6*time.Minute, decision("decline", start.Add(2*time.Minute)), nothingPending)
	a.CheckAt(now)
	o, err := a.Observe(context.Background())
	if err != nil || strings.Contains(fmt.Sprintf("%+v", o), "PRIVATE_REASON") {
		t.Fatalf("%+v %v", o, err)
	}
}

// A cursor saved before turn-start times were kept loads, and gives the
// plugin's decision nothing to be compared with.
func TestCursorWithoutTurnStartTimeLoads(t *testing.T) {
	var c Cursor
	if err := json.Unmarshal([]byte(`{"retrySchema":1,"thread":"thr_example123","lastSeq":9,"turnSeq":4}`), &c); err != nil || c.TurnStartedMs != 0 || c.TurnSeq != 4 {
		t.Fatalf("%+v %v", c, err)
	}
	c.apply(event{Seq: 10, Type: "turn/started", CreatedAt: json.RawMessage(fmt.Sprint(now.UnixMilli()))}, now)
	if c.TurnStartedMs != now.UnixMilli() {
		t.Fatalf("%d", c.TurnStartedMs)
	}
	// A turn start without a usable time clears an earlier one.
	c.apply(event{Seq: 11, Type: "turn/started"}, now)
	if c.TurnStartedMs != 0 {
		t.Fatalf("%d", c.TurnStartedMs)
	}
}
