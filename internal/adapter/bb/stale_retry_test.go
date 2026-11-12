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
		a.now = func() time.Time { return start.Add(after) }
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
		{"later_notice_active_thread", ended + `,` + at(retryNotice, start.Add(3*time.Minute)) + `]`, 50 * time.Minute, true, false, "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newFake(`{"thread":{"status":"`+tc.status+`"}}`, tc.log, false)
			a.now = func() time.Time { return start.Add(tc.after) }
			o, err := a.Observe(context.Background())
			if err != nil || o.Retrying != tc.retry || o.RetryExpired != tc.expired || o.RetryUnverified {
				t.Fatalf("%+v %v", o, err)
			}
		})
	}
}

// An active thread is running now: its retry notice never expires by age and
// suppresses nudges exactly as before, however long the provider takes.
func TestActiveThreadRetryNoticeNeverExpiresByAge(t *testing.T) {
	start := now.Add(-48 * time.Hour)
	a, _ := newFake(`{"thread":{"status":"active"}}`, `[`+at(turnStarted, start)+`,`+at(retryNotice, start.Add(time.Minute))+`]`, false)
	a.WithStateDir(t.TempDir())
	for _, after := range []time.Duration{2 * time.Minute, time.Hour, 47 * time.Hour} {
		a.now = func() time.Time { return start.Add(after) }
		o, err := a.Observe(context.Background())
		d := decide.Decide(decide.Input{Control: decide.Control{Enabled: true}, InWindow: true, Obs: o, UnchangedFor: after, SinceWake: after, StallAfter: time.Minute, MinWakeInterval: time.Minute, MaxNudges: 3})
		if err != nil || !o.Retrying || o.RetryExpired || d.Action != decide.None || d.Reason != "provider is retrying the turn" {
			t.Fatalf("+%s: %+v %+v %v", after, o, d, err)
		}
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
		a.now = func() time.Time { return start.Add(tc.after) }
		if o, err := a.Observe(context.Background()); err != nil || o.Retrying != tc.retry || o.RetryExpired == tc.retry {
			t.Fatalf("ttl=%s +%s: %+v %v", tc.ttl, tc.after, o, err)
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
