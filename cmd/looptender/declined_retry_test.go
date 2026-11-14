//go:build unix

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Through the CLI and the real BB adapter against the stand-in bb: an errored
// thread with a fresh retry notice is left alone while nothing says the
// retry is dead, and recovered on the same check once BB's retry plugin has
// declined the failure and has nothing pending.
func TestTickDoesNotWaitOutARetryNoticeBBDeclined(t *testing.T) {
	now := time.Now().UTC()
	started, failed, notice := now.Add(-8*time.Minute), now.Add(-6*time.Minute), now.Add(-2*time.Minute)
	ms := func(at time.Time) int64 { return at.UnixMilli() }
	log := fmt.Sprintf(`[{"seq":1,"createdAt":%d,"type":"turn/started","data":{"providerThreadId":"provider_example"}},
 {"seq":2,"createdAt":%d,"type":"provider/error","data":{"willRetry":true,"errorInfo":{"category":"unknown","providerCode":"unknown"}}},
 {"seq":3,"createdAt":%d,"type":"turn/completed","data":{"providerThreadId":"provider_example","status":"failed"}},
 {"seq":4,"createdAt":%d,"type":"provider/error","data":{"willRetry":true,"errorInfo":{"category":"unknown","providerCode":"unknown"}}}]`,
		ms(started), ms(started.Add(time.Minute)), ms(failed), ms(notice))
	decline := func(observed time.Time) string {
		return fmt.Sprintf(`{"threadId":"thr_primary","diagnostic":{"requestId":"creq_example1","observedAt":%d,"availability":{"kind":"not-routed"},"decision":{"kind":"decline","reason":"PRIVATE_PLUGIN_REASON"}}}`, ms(observed))
	}
	setup := func(decision, retries string) *fixture {
		f := newFixture(t)
		write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0o600)
		write(t, filepath.Join(f.bbDir, "thr_primary.log"), log, 0o600)
		if decision != "" {
			write(t, filepath.Join(f.bbDir, "thr_primary.retry-decision"), decision, 0o600)
		}
		if retries != "" {
			write(t, filepath.Join(f.bbDir, "thr_primary.retries"), retries, 0o600)
		}
		f.run("enable")
		return f
	}
	count := func(f *fixture, call string) int { return strings.Count(f.read("calls"), call) }

	// No decision recorded: the notice is honoured, as before.
	f := setup("", "")
	if v := f.tick(); v["action"] != "none" || v["reason"] != "provider is retrying the turn" || v["retrySignal"] != nil || count(f, "thread retry ") != 0 {
		t.Fatalf("no decision: %v", v)
	}
	if count(f, "provider-retry explain thr_primary --json") != 1 || count(f, "provider-retry status") != 0 {
		t.Fatalf("plugin calls:\n%s", f.read("calls"))
	}

	// Declined for this failure, nothing pending: recovered at once, by retry.
	f = setup(decline(failed), "")
	v := f.tick()
	if v["action"] != "reprompt" || v["retrySignal"] != "declined" || v["recovery"] != "turn_retry" || count(f, "thread retry thr_primary ") != 1 || f.read("thr_primary.told") != "" {
		t.Fatalf("declined: %v\n%s", v, f.read("calls"))
	}
	if count(f, "provider-retry explain thr_primary --json") != 1 || count(f, "provider-retry status thr_primary --json") != 1 {
		t.Fatalf("plugin calls:\n%s", f.read("calls"))
	}
	code, out, errs := f.run("status")
	if code != exitOK || !strings.Contains(out, "retry signal: declined; BB's retry plugin recorded that it will not retry the failed turn") || strings.Contains(out+errs, "PRIVATE_PLUGIN_REASON") {
		t.Fatalf("status %d\n%s%s", code, out, errs)
	}
	code, out, errs = f.run("timeline", "--kind", "turn_retry")
	if code != exitOK || errs != "" || !strings.Contains(out, "retry declined") || strings.Contains(out, "PRIVATE_PLUGIN_REASON") {
		t.Fatalf("timeline %d %s\n%s", code, errs, out)
	}

	// Declined, but a retry is pending: wait for it.
	f = setup(decline(failed), `{"retries":[{"id":"qm_example1","threadId":"thr_primary"}]}`)
	if v := f.tick(); v["action"] != "none" || v["reason"] != "provider is retrying the turn" || count(f, "thread retry ") != 0 {
		t.Fatalf("pending: %v", v)
	}
	// A decline recorded before this turn started is about an earlier failure.
	f = setup(decline(started.Add(-time.Hour)), "")
	if v := f.tick(); v["action"] != "none" || v["reason"] != "provider is retrying the turn" || count(f, "provider-retry status") != 0 {
		t.Fatalf("old decision: %v", v)
	}
	// A held loop is not observed, so the plugin is not asked either.
	f = setup(decline(failed), "")
	f.run("pause")
	if v := f.tick(); v["reason"] != "paused" || count(f, "provider-retry") != 0 || count(f, "thread retry ") != 0 {
		t.Fatalf("paused: %v\n%s", v, f.read("calls"))
	}
}
