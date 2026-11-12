//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A BB thread fails during an outage, keeps logging "will retry" notices after
// its failed completion, then goes silent. The adapter reads event times
// against the wall clock, so the synthetic events are dated in the recent past.
// Before the freshness bound every check here recorded action none, "provider
// is retrying the turn", and no hold was ever reached.
func TestStaleRetrySignalJourneyRecoversHoldsAndExplains(t *testing.T) {
	f := newFixture(t)
	journeyConfig(t, f, fmt.Sprintf("  adapter: bb\n  bb: {thread: thr_primary, cli: %q}", filepath.Join(f.dir, "bin", "bb")), 2)
	failed := time.Now().Add(-100 * time.Minute)
	notice := func(seq int, after time.Duration) string {
		return fmt.Sprintf(`{"seq":%d,"createdAt":%d,"type":"provider/error","data":{"message":"PRIVATE_JOURNEY_CONTENT","willRetry":true,"errorInfo":{"category":"unknown","providerCode":"unknown"}}}`, seq, failed.Add(after).UnixMilli())
	}
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_primary.log"), fmt.Sprintf(`[{"seq":1,"createdAt":%d,"type":"turn/started","data":{"providerThreadId":"provider_private"}},
 %s,
 {"seq":3,"createdAt":%d,"type":"turn/completed","data":{"providerThreadId":"provider_private","status":"failed"}},
 %s,
 %s]`, failed.Add(-10*time.Minute).UnixMilli(), notice(2, -time.Minute), failed.UnixMilli(), notice(4, time.Minute), notice(5, 20*time.Minute)), 0600)
	journeyCommand(t, f, "enable")
	journeyTick(t, f, 0) // First bounded re-prompt.
	_, status, _ := f.run("status")
	if !strings.Contains(status, "retry signal: expired;") || !strings.Contains(status, "decision:  reprompt;") {
		t.Fatalf("status does not explain the expired retry signal:\n%s", status)
	}
	_, statusJSON, _ := f.run("status", "--json")
	var view struct {
		State struct {
			LastCheck struct {
				RetrySignal string `json:"retrySignal"`
			} `json:"lastCheck"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(statusJSON), &view); err != nil || view.State.LastCheck.RetrySignal != "expired" {
		t.Fatalf("status --json: %v %+v", err, view)
	}
	journeyTick(t, f, 1) // Second and last re-prompt.
	journeyTick(t, f, 2) // Allowance spent: a person is asked.
	journeyTick(t, f, 3) // Held; the provider is not queried.
	if got := strings.Count(f.read("calls"), "thread tell thr_primary "); got != 2 {
		t.Fatalf("re-prompts sent=%d", got)
	}
	r := isolatedJourney(t, f, "enable,reprompt,reprompt,await_user,none")
	for _, check := range r[1:4] {
		o := check.Observation
		if o.State != "error" || o.ErrorClass != "unknown" || o.Retry != "expired" || check.Cause == "retrying" || check.TargetKeyAfter != check.TargetKey {
			t.Fatal(check)
		}
	}
	if !r[3].ControlAfter.AwaitingUser || r[4].Cause != "awaiting_user" {
		t.Fatal("hold not persisted", r[3], r[4])
	}
	code, text, errs := timelineCLI(t, filepath.Join(f.dir, "state"))
	if code != 0 || strings.Count(text, "retry expired (stale provider retry notice no longer honored; ordinary error handling applied)") < 3 || strings.Contains(text, journeyPrivate) {
		t.Fatalf("timeline does not explain the expired retry signal: %d %s\n%s", code, errs, text)
	}
	// Once held, status reports the hold; the expiry stays in the timeline.
	if _, status, _ = f.run("status"); strings.Contains(status, "retry signal:") {
		t.Fatalf("held check claimed an observation:\n%s", status)
	}
}
