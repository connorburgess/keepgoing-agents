package bb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// stamped adds BB's createdAt field, given here as raw JSON, to a synthetic event.
func stamped(event, createdAt string) string {
	return `{"createdAt":` + createdAt + `,` + event[1:]
}

// at stamps a synthetic event with a createdAt in Unix milliseconds.
func at(event string, t time.Time) string {
	return stamped(event, fmt.Sprint(t.UnixMilli()))
}

const turnFailed = `{"type":"turn/completed","data":{"providerThreadId":"p1","status":"failed"}}`

var retryNotice = providerError("unknown", nil, "unknown", true)

// outageLog replays a turn that fails during an outage and then keeps logging
// retry notices after its failed completion, with no new turn. The last notice
// is logged lastNotice after the turn started.
func outageLog(start time.Time, lastNotice time.Duration) string {
	return `[` + at(turnStarted, start) + `,` + at(retryNotice, start.Add(time.Minute)) + `,` +
		at(turnFailed, start.Add(2*time.Minute)) + `,` + at(retryNotice, start.Add(3*time.Minute)) + `,` +
		at(retryNotice, start.Add(lastNotice)) + `]`
}

func errorDecision(o decide.Observation, reprompts int, since time.Duration) decide.Decision {
	return decide.Decide(decide.Input{Control: decide.Control{Enabled: true}, InWindow: true, Obs: o,
		Failover:  decide.Failover{Eligible: true, Available: true},
		Reprompts: reprompts, MaxReprompts: 3, SinceLastReprompt: since, Backoff: 10 * time.Minute})
}

// The regression for a dead errored thread that kept a "will retry" notice:
// before the freshness bound every check below returned "provider is retrying
// the turn" and the loop waited until a person intervened.
func TestStaleRetryNoticeOnErroredThreadExpires(t *testing.T) {
	start := now.Add(-3 * time.Hour)
	const last = 22 * time.Minute
	a, _ := newFake(`{"thread":{"status":"error"}}`, outageLog(start, last), false)
	a.WithStateDir(t.TempDir())
	observe := func(after time.Duration) decide.Observation {
		t.Helper()
		a.CheckAt(start.Add(after))
		o, err := a.Observe(context.Background())
		if err != nil || o.State != decide.Error || o.ErrorClass != decide.ErrUnknown || o.RetryUnverified || !o.RetryKnown {
			t.Fatalf("+%s: %+v %v", after, o, err)
		}
		return o
	}
	// While the newest notice is fresh the loop still leaves the provider alone.
	for _, after := range []time.Duration{last, last + time.Minute, last + DefaultRetrySignalTTL} {
		o := observe(after)
		if d := errorDecision(o, 0, 0); !o.Retrying || o.RetryExpired || d.Action != decide.None || d.Reason != "provider is retrying the turn" {
			t.Fatalf("+%s: fresh notice not honored: %+v %+v", after, o, d)
		}
	}
	// Past the interval the notice stops counting and ordinary bounded error
	// handling applies: re-prompts with backoff, then a person. Never a handoff.
	for _, tc := range []struct {
		after     time.Duration
		reprompts int
		since     time.Duration
		action    decide.Action
	}{
		{last + DefaultRetrySignalTTL + time.Millisecond, 0, 0, decide.Reprompt},
		{last + 20*time.Minute, 1, 4 * time.Minute, decide.None},
		{last + 30*time.Minute, 1, 14 * time.Minute, decide.Reprompt},
		{last + 75*time.Minute, 3, 20 * time.Minute, decide.AwaitUser},
	} {
		o := observe(tc.after)
		d := errorDecision(o, tc.reprompts, tc.since)
		if o.Retrying || !o.RetryExpired || d.Action != tc.action || d.Reason == "provider is retrying the turn" {
			t.Fatalf("+%s: %+v %+v", tc.after, o, d)
		}
	}
}

// A retry notice logged after the turn's failed completion, with no new turn,
// is aged exactly like any other notice: honored while fresh, then expired.
// A failed completion with no later notice is simply not retrying.
func TestRetryNoticeAfterFailedCompletion(t *testing.T) {
	start := now.Add(-time.Hour)
	ended := `[` + at(turnStarted, start) + `,` + at(retryNotice, start.Add(time.Minute)) + `,` + at(turnFailed, start.Add(2*time.Minute))
	for _, tc := range []struct {
		name, log      string
		after          time.Duration
		retry, expired bool
		status         string
	}{
		{"no_later_notice", ended + `]`, 3 * time.Minute, false, false, "error"},
		{"later_notice_fresh", ended + `,` + at(retryNotice, start.Add(3*time.Minute)) + `]`, 10 * time.Minute, true, false, "error"},
		{"later_notice_expired", ended + `,` + at(retryNotice, start.Add(3*time.Minute)) + `]`, 19 * time.Minute, false, true, "error"},
		{"later_notice_declined", ended + `,` + at(providerError("unknown", nil, "unknown", false), start.Add(3*time.Minute)) + `]`, 19 * time.Minute, false, false, "error"},
		{"later_notice_active_thread_fresh", ended + `,` + at(retryNotice, start.Add(3*time.Minute)) + `]`, 18 * time.Minute, true, false, "active"},
		{"later_notice_active_thread_silent", ended + `,` + at(retryNotice, start.Add(3*time.Minute)) + `]`, 19 * time.Minute, false, true, "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newFake(`{"thread":{"status":"`+tc.status+`"}}`, tc.log, false)
			a.CheckAt(start.Add(tc.after))
			o, err := a.Observe(context.Background())
			if err != nil || o.Retrying != tc.retry || o.RetryExpired != tc.expired || o.RetryUnverified {
				t.Fatalf("%+v %v", o, err)
			}
		})
	}
}

const toolOutput = `{"type":"item/commandExecution/outputDelta","data":{"providerThreadId":"p1","itemId":"item_example"}}`
const queuedInput = `{"type":"client/turn/requested","data":{}}`

func stallDecision(o decide.Observation, nudges int) decide.Decision {
	return decide.Decide(decide.Input{Control: decide.Control{Enabled: true}, InWindow: true, Obs: o, UnchangedFor: 2 * time.Hour, SinceWake: 2 * time.Hour,
		StallAfter: 30 * time.Minute, MinWakeInterval: time.Hour, Nudges: nudges, MaxNudges: 3,
		Failover: decide.Failover{Eligible: true, Available: true}, MaxReprompts: 3})
}

// An active thread that keeps logging provider events is alive: its retry
// notice suppresses nudges exactly as before, for as long as that continues.
func TestActiveThreadRetryNoticeStandsWhileLogIsNotSilent(t *testing.T) {
	start := now.Add(-48 * time.Hour)
	events := `[` + at(turnStarted, start) + `,` + at(retryNotice, start.Add(time.Minute))
	dir := t.TempDir()
	kinds := []string{toolOutput, retryNotice, `{"type":"item/started","data":{"providerThreadId":"p1","item":{"id":"item_example","type":"commandExecution"}}}`}
	for i := 1; i <= 280; i++ { // about 47 hours, one event every ten minutes
		logged := start.Add(time.Duration(i) * 10 * time.Minute)
		events += `,` + at(kinds[i%len(kinds)], logged)
		a, _ := newFake(`{"thread":{"status":"active"}}`, events+`]`, false)
		a.WithStateDir(dir)
		a.CheckAt(logged.Add(9 * time.Minute))
		o, err := a.Observe(context.Background())
		d := stallDecision(o, 0)
		if err != nil || !o.Retrying || o.RetryExpired || d.Action != decide.None || d.Reason != "provider is retrying the turn" {
			t.Fatalf("event %d: %+v %+v %v", i, o, d, err)
		}
	}
}

// The second observed shape: a re-prompt starts a new turn, which clears the
// earlier error; the new turn logs a retry notice while the thread is active
// and then nothing at all. Before the silence rule this returned "provider is
// retrying the turn" on every later check. An expired notice on an active
// thread returns the thread to ordinary stall handling: nudges bounded by
// max_nudges, then a person. It never becomes a re-prompt or a handoff.
func TestSilentActiveThreadRetryNoticeExpiresIntoStallHandling(t *testing.T) {
	start := now.Add(-6 * time.Hour)
	reprompted := start.Add(40 * time.Minute)
	log := outageLog(start, 22*time.Minute)
	log = log[:len(log)-1] + `,` + at(queuedInput, reprompted) + `,` + at(turnStarted, reprompted.Add(time.Second)) + `,` + at(retryNotice, reprompted.Add(time.Minute)) + `]`
	a, _ := newFake(`{"thread":{"status":"active"}}`, log, false)
	a.WithStateDir(t.TempDir())
	last := reprompted.Add(time.Minute)
	for _, tc := range []struct {
		after  time.Duration
		nudges int
		retry  bool
		action decide.Action
		reason string
	}{
		{0, 0, true, decide.None, "provider is retrying the turn"},
		{DefaultRetrySignalTTL, 0, true, decide.None, "provider is retrying the turn"},
		{DefaultRetrySignalTTL + time.Second, 0, false, decide.Nudge, "no progress for 2h0m0s"},
		{time.Hour, 2, false, decide.Nudge, "no progress for 2h0m0s"},
		{4 * time.Hour, 3, false, decide.AwaitUser, "no progress after 3 nudges"},
	} {
		a.CheckAt(last.Add(tc.after))
		o, err := a.Observe(context.Background())
		d := stallDecision(o, tc.nudges)
		if err != nil || o.State != decide.Active || o.Retrying != tc.retry || o.RetryExpired == tc.retry || o.RetryUnverified || d.Action != tc.action || d.Reason != tc.reason {
			t.Fatalf("+%s: %+v %+v %v", tc.after, o, d, err)
		}
	}
	// Requested input is not thread activity. If BB logs this supervisor's own
	// nudge (or anyone's message) when it is queued, the notice stays expired.
	for i, minutes := range []int{17, 77, 137} {
		log = log[:len(log)-1] + `,` + at(queuedInput, last.Add(time.Duration(minutes)*time.Minute)) + `]`
		b, _ := newFake(`{"thread":{"status":"active","queuedMessageCount":`+fmt.Sprint(i+1)+`}}`, log, false)
		b.CheckAt(last.Add(time.Duration(minutes+1) * time.Minute))
		if o, err := b.Observe(context.Background()); err != nil || o.Retrying || !o.RetryExpired || o.Queued != i+1 {
			t.Fatalf("queued input %d re-armed the notice: %+v %v", i+1, o, err)
		}
	}
	// A thread that has not stalled by its own progress rules is simply active.
	o, _ := a.Observe(context.Background())
	if d := decide.Decide(decide.Input{Control: decide.Control{Enabled: true}, InWindow: true, Obs: o, StallAfter: 30 * time.Minute}); d.Action != decide.None || d.Reason != "active" {
		t.Fatalf("%+v", d)
	}
}

// BB schedules its own retry of a rate-limited turn for the structured reset.
// The notice must not expire before that reset, nor for one interval after it,
// so keepgoing neither hands off early nor prompts alongside BB's retry.
// Without a structured reset there is nothing scheduled to wait for.
func TestRateLimitRetryNoticeWaitsForStructuredReset(t *testing.T) {
	start := now.Add(-12 * time.Hour)
	notice := at(providerError("rate-limit", 429, "rate_limit_event", true), start.Add(time.Minute))
	resets := start.Add(5 * time.Hour)
	for _, status := range []string{"error", "active"} {
		for _, tc := range []struct {
			name    string
			window  string
			after   time.Duration
			retry   bool
			resetIn bool
		}{
			{"reset_ahead", at(rateLimitsEvent("blocked", resets), start.Add(time.Minute)), 16 * time.Minute, true, true},
			{"reset_ahead_hours_later", at(rateLimitsEvent("blocked", resets), start.Add(time.Minute)), 4*time.Hour + 59*time.Minute, true, true},
			{"just_after_reset", at(rateLimitsEvent("blocked", resets), start.Add(time.Minute)), 5*time.Hour + time.Minute, true, false},
			{"grace_after_reset", at(rateLimitsEvent("blocked", resets), start.Add(time.Minute)), 5*time.Hour + DefaultRetrySignalTTL, true, false},
			{"grace_spent", at(rateLimitsEvent("blocked", resets), start.Add(time.Minute)), 5*time.Hour + DefaultRetrySignalTTL + time.Second, false, false},
			{"no_structured_reset_fresh", "", 15 * time.Minute, true, false},
			{"no_structured_reset_expired", "", 17 * time.Minute, false, false},
		} {
			t.Run(status+"_"+tc.name, func(t *testing.T) {
				log := `[` + at(turnStarted, start) + `,` + notice
				if tc.window != "" {
					log += `,` + tc.window
				}
				a, _ := newFake(`{"thread":{"status":"`+status+`"}}`, log+`]`, false)
				a.CheckAt(start.Add(tc.after))
				o, err := a.Observe(context.Background())
				if err != nil || o.ErrorClass != decide.ErrRateLimit || o.Retrying != tc.retry || o.RetryExpired == tc.retry {
					t.Fatalf("%+v %v", o, err)
				}
				if status == "error" && o.ResetsAt.Equal(resets) != tc.resetIn {
					t.Fatalf("reset reported=%v: %+v", !tc.resetIn, o)
				}
				d := decide.Decide(decide.Input{Control: decide.Control{Enabled: true}, InWindow: true, Obs: o, MaxReprompts: 3,
					Failover: decide.Failover{Eligible: true, Available: true}})
				if tc.retry && (d.Action != decide.None || d.Reason != "provider is retrying the turn") {
					t.Fatalf("acted while BB's retry is pending: %+v", d)
				}
				if !tc.retry && status == "error" && d.Action != decide.Handoff {
					t.Fatalf("expired rate limit not handled by the ordinary rules: %+v", d)
				}
			})
		}
	}
}

// The newest blocked rate-limit window stays in the summary across turns. A
// later notice of another class must not borrow its reset: before the class
// check, this network notice was honored until the weekly reset plus the
// interval, days away, as "provider is retrying the turn".
func TestOtherErrorClassDoesNotInheritAStaleRateLimitWindow(t *testing.T) {
	start := now.Add(-6 * time.Hour)
	weekly := start.Add(5 * 24 * time.Hour)
	for _, status := range []string{"error", "active"} {
		for _, tc := range []struct {
			name, notice string
		}{
			{"network", providerError("stream-disconnected", nil, "responseStreamDisconnected", true)},
			{"unknown", retryNotice},
			{"overloaded", providerError("overloaded", 529, "overloaded_error", true)},
		} {
			t.Run(status+"_"+tc.name, func(t *testing.T) {
				log := `[` + at(turnStarted, start.Add(-time.Hour)) + `,` + at(rateLimitsEvent("blocked", weekly), start.Add(-50*time.Minute)) + `,` +
					at(turnStarted, start) + `,` + at(tc.notice, start.Add(time.Minute)) + `]`
				for _, c := range []struct {
					after time.Duration
					retry bool
				}{{10 * time.Minute, true}, {17 * time.Minute, false}, {26 * time.Hour, false}} {
					a, _ := newFake(`{"thread":{"status":"`+status+`"}}`, log, false)
					a.CheckAt(start.Add(c.after))
					if o, err := a.Observe(context.Background()); err != nil || o.Retrying != c.retry || o.RetryExpired == c.retry {
						t.Fatalf("+%s: %+v %v", c.after, o, err)
					}
				}
			})
		}
	}
}

// BB counts a retry its plugin has scheduled as a queued message. An errored
// thread with queued messages is left alone ahead of any handoff or re-prompt,
// so a scheduled retry survives the expiry of its notice, with or without a
// structured reset (a pooled route has none that keepgoing can see).
func TestQueuedRetryIsLeftAloneAfterItsNoticeExpires(t *testing.T) {
	start := now.Add(-3 * time.Hour)
	for _, tc := range []struct {
		name, notice string
		class        decide.ErrorClass
	}{
		{"rate_limit_without_reset", providerError("rate-limit", 429, "rate_limit_event", true), decide.ErrRateLimit},
		{"network", providerError("stream-disconnected", nil, "responseStreamDisconnected", true), decide.ErrNetwork},
		{"unknown", retryNotice, decide.ErrUnknown},
	} {
		for _, queued := range []int{1, 0} {
			t.Run(fmt.Sprintf("%s_queued_%d", tc.name, queued), func(t *testing.T) {
				a, _ := newFake(fmt.Sprintf(`{"thread":{"status":"error","queuedMessageCount":%d}}`, queued), `[`+at(turnStarted, start)+`,`+at(tc.notice, start.Add(time.Minute))+`,`+at(turnFailed, start.Add(2*time.Minute))+`,`+at(tc.notice, start.Add(3*time.Minute))+`]`, false)
				a.CheckAt(start.Add(2 * time.Hour))
				o, err := a.Observe(context.Background())
				if err != nil || o.Retrying || !o.RetryExpired || o.ErrorClass != tc.class || o.Queued != queued {
					t.Fatalf("%+v %v", o, err)
				}
				d := decide.Decide(decide.Input{Control: decide.Control{Enabled: true}, InWindow: true, Obs: o, MaxReprompts: 3,
					Failover: decide.Failover{Eligible: true, Available: true}})
				if queued > 0 && (d.Action != decide.None || d.Reason != "messages already queued after error") {
					t.Fatalf("acted over a queued retry: %+v", d)
				}
				if queued == 0 && d.Action == decide.None {
					t.Fatalf("nothing queued and nothing done: %+v", d)
				}
			})
		}
	}
}

// A BB host whose clock runs behind this machine stamps events in the local
// past. An event that was not in the previous complete scan cannot be older
// than that scan, so it is aged from there instead of expiring at once. On a
// first scan there is no earlier scan, and old events keep their own time.
func TestEventTimeBehindLocalClockIsBoundedByPreviousScan(t *testing.T) {
	const skew = 3 * time.Hour
	dir := t.TempDir()
	events := `[` + at(turnStarted, now.Add(-skew))
	a, _ := newFake(`{"thread":{"status":"error"}}`, events+`]`, false)
	a.WithStateDir(dir)
	a.CheckAt(now)
	if o, err := a.Observe(context.Background()); err != nil || o.Retrying || o.RetryExpired {
		t.Fatalf("%+v %v", o, err)
	}
	events += `,` + at(retryNotice, now.Add(5*time.Minute-skew)) // logged five minutes later, stamped three hours behind
	for _, tc := range []struct {
		after time.Duration
		retry bool
	}{{6 * time.Minute, true}, {15 * time.Minute, true}, {16 * time.Minute, false}} {
		b, _ := newFake(`{"thread":{"status":"error"}}`, events+`]`, false)
		b.WithStateDir(dir)
		b.CheckAt(now.Add(tc.after))
		if o, err := b.Observe(context.Background()); err != nil || o.Retrying != tc.retry || o.RetryExpired == tc.retry {
			t.Fatalf("+%s: %+v %v", tc.after, o, err)
		}
	}
	c, _ := newFake(`{"thread":{"status":"error"}}`, events+`]`, false)
	c.CheckAt(now.Add(6 * time.Minute))
	if o, err := c.Observe(context.Background()); err != nil || o.Retrying || !o.RetryExpired {
		t.Fatalf("first scan trusted an event stamped hours ago: %+v %v", o, err)
	}
}

// A provider that is retrying slowly but genuinely logs a newer notice on each
// failed attempt; every one restarts the interval.
func TestNewerRetryNoticeRestartsInterval(t *testing.T) {
	start := now.Add(-2 * time.Hour)
	events := `[` + at(turnStarted, start)
	dir := t.TempDir()
	for i := 1; i <= 6; i++ {
		// Notices 14 minutes apart: each is checked just before it would expire.
		noticed := start.Add(time.Duration(i) * 14 * time.Minute)
		events += `,` + at(retryNotice, noticed)
		a, _ := newFake(`{"thread":{"status":"error"}}`, events+`]`, false)
		a.WithStateDir(dir)
		a.now = func() time.Time { return noticed.Add(13 * time.Minute) }
		if o, err := a.Observe(context.Background()); err != nil || !o.Retrying || o.RetryExpired {
			t.Fatalf("notice %d: %+v %v", i, o, err)
		}
	}
	a, _ := newFake(`{"thread":{"status":"error"}}`, events+`]`, false)
	a.WithStateDir(dir)
	a.now = func() time.Time { return start.Add(6*14*time.Minute + 16*time.Minute) }
	if o, err := a.Observe(context.Background()); err != nil || o.Retrying || !o.RetryExpired {
		t.Fatalf("silence after the last notice did not expire: %+v %v", o, err)
	}
}

// Without a usable event time the notice is aged from the scan that first read
// it, which survives a restart. A source clock running ahead is clamped to that
// same scan time, and no createdAt shape can fail the scan itself.
func TestRetryNoticeWithoutUsableEventTime(t *testing.T) {
	first := now
	for _, createdAt := range []string{"", `null`, `"2026-11-09T12:00:00Z"`, `1794225600`, `-5`, `0`, `1794225600000.5`, `{}`, `1e30`,
		fmt.Sprint(first.Add(72 * time.Hour).UnixMilli())} {
		t.Run(createdAt, func(t *testing.T) {
			notice := retryNotice
			if createdAt != "" {
				notice = stamped(retryNotice, createdAt)
			}
			dir := t.TempDir()
			for _, tc := range []struct {
				after time.Duration
				retry bool
			}{{0, true}, {DefaultRetrySignalTTL, true}, {DefaultRetrySignalTTL + time.Second, false}, {5 * time.Hour, false}} {
				a, _ := newFake(`{"thread":{"status":"error"}}`, `[`+turnStarted+`,`+notice+`]`, false)
				a.WithStateDir(dir) // a new adapter per check, as after a restart
				a.now = func() time.Time { return first.Add(tc.after) }
				o, err := a.Observe(context.Background())
				if err != nil || o.RetryUnverified || o.Retrying != tc.retry || o.RetryExpired == tc.retry {
					t.Fatalf("+%s: %+v %v", tc.after, o, err)
				}
			}
		})
	}
}

// A summary saved before retry notices carried a time is aged from the first
// scan after the upgrade, without rereading the log.
func TestLegacyRetrySummaryIsAgedFromFirstScan(t *testing.T) {
	dir := t.TempDir()
	legacy := Cursor{Thread: "thr_example123", RetrySchema: 1, LastSeq: 2, Error: &errorSignal{Class: decide.ErrNetwork, WillRetry: true, RetryKnown: true}}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "bb-cursor.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		after time.Duration
		retry bool
	}{{0, true}, {10 * time.Minute, true}, {16 * time.Minute, false}} {
		a, calls := newFake(`{"thread":{"status":"error"}}`, `[]`, false)
		a.WithStateDir(dir)
		a.now = func() time.Time { return now.Add(tc.after) }
		o, err := a.Observe(context.Background())
		if err != nil || o.Retrying != tc.retry || o.RetryExpired == tc.retry || o.ErrorClass != decide.ErrNetwork {
			t.Fatalf("+%s: %+v %v", tc.after, o, err)
		}
		for _, c := range *calls {
			if c.args[1] == "log" && c.args[5] != "2" {
				t.Fatalf("legacy summary was rebuilt from seq %s", c.args[5])
			}
		}
	}
}

func TestRetrySignalTTLIsConfigurable(t *testing.T) {
	start := now.Add(-time.Hour)
	log := `[` + at(turnStarted, start) + `,` + at(retryNotice, start) + `]`
	for _, tc := range []struct {
		ttl, after time.Duration
		retry      bool
	}{
		{0, 15 * time.Minute, true}, {0, 15*time.Minute + time.Second, false},
		{2 * time.Minute, 2 * time.Minute, true}, {2 * time.Minute, 3 * time.Minute, false},
		{45 * time.Minute, 44 * time.Minute, true}, {45 * time.Minute, 46 * time.Minute, false},
	} {
		a, _ := newFake(`{"thread":{"status":"error"}}`, log, false)
		a.cfg.RetrySignalTTL = config.Duration{Duration: tc.ttl}
		a.CheckAt(start.Add(tc.after))
		if o, err := a.Observe(context.Background()); err != nil || o.Retrying != tc.retry || o.RetryExpired == tc.retry {
			t.Fatalf("ttl=%s +%s: %+v %v", tc.ttl, tc.after, o, err)
		}
	}
}

// Signals that carry no retry intent are stamped too, so only a summary saved
// by an older build is ever re-aged, and only once.
func TestEverySignalCarriesItsEventTime(t *testing.T) {
	logged := now.Add(-time.Hour)
	for _, typ := range []string{"system/error", "turn/failed"} {
		dir := t.TempDir()
		for _, checked := range []time.Duration{0, time.Hour} {
			a, _ := newFake(`{"thread":{"status":"error"}}`, `[`+at(`{"type":"`+typ+`","data":{"code":"connection_error"}}`, logged)+`]`, false)
			a.WithStateDir(dir)
			a.CheckAt(now.Add(checked))
			if _, err := a.Observe(context.Background()); err != nil {
				t.Fatal(err)
			}
			if c := a.loadCursor(); c.Error == nil || !c.Error.At.Equal(logged) || !c.LastEventAt.Equal(logged) || !c.ScannedAt.Equal(now.Add(checked)) {
				t.Fatalf("%s +%s: %+v %+v", typ, checked, c, c.Error)
			}
		}
	}
}

// A local clock that moved backwards cannot expire a notice early.
func TestRetryNoticeSurvivesLocalClockRollback(t *testing.T) {
	a, _ := newFake(`{"thread":{"status":"error"}}`, `[`+turnStarted+`,`+at(retryNotice, now)+`]`, false)
	a.now = func() time.Time { return now.Add(-6 * time.Hour) }
	if o, err := a.Observe(context.Background()); err != nil || !o.Retrying || o.RetryExpired {
		t.Fatalf("%+v %v", o, err)
	}
}
