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

// A BB thread fails during an outage and keeps logging "will retry" notices
// after its failed completion. Each re-prompt starts a turn that logs one more
// notice and fails again. The fake bb is edited between checks to model what a
// re-prompt does; every check is a fresh command with its own check time.
// Before the freshness bound and the re-prompt reset rule, every check here
// recorded "provider is retrying the turn" or restarted the count, and no hold
// was ever reached.
func TestStaleRetrySignalJourneyRecoversHoldsAndExplains(t *testing.T) {
	f := newFixture(t)
	journeyConfig(t, f, fmt.Sprintf("  adapter: bb\n  bb: {thread: thr_primary, cli: %q}", filepath.Join(f.dir, "bin", "bb")), 2)
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) // journeyTick minute 0
	seq := 0
	var events []string
	add := func(minute int, typ, data string) {
		seq++
		events = append(events, fmt.Sprintf(`{"seq":%d,"createdAt":%d,"type":%q,"data":%s}`, seq, base.Add(time.Duration(minute)*time.Minute).UnixMilli(), typ, data))
		write(t, filepath.Join(f.bbDir, "thr_primary.log"), "["+strings.Join(events, ",")+"]", 0600)
	}
	status := func(s string) {
		write(t, filepath.Join(f.bbDir, "thr_primary.show"), fmt.Sprintf(`{"thread":{"status":%q}}`, s), 0600)
	}
	const turn = `{"providerThreadId":"provider_private"}`
	const notice = `{"message":"PRIVATE_JOURNEY_CONTENT","willRetry":true,"errorInfo":{"category":"unknown","providerCode":"unknown"}}`
	const failed = `{"providerThreadId":"provider_private","status":"failed"}`
	statusText := func() string {
		_, out, _ := f.run("status")
		return out
	}

	add(-30, "turn/started", turn)
	add(-20, "provider/error", notice)
	add(-19, "turn/completed", failed)
	add(-18, "provider/error", notice) // after the failed completion, no new turn
	add(-10, "provider/error", notice)
	status("error")
	journeyCommand(t, f, "enable")
	journeyTick(t, f, 0) // The newest notice is 10 minutes old: still honored.
	if s := statusText(); strings.Contains(s, "retry signal:") || !strings.Contains(s, "provider is retrying the turn") {
		t.Fatalf("fresh notice not honored:\n%s", s)
	}
	journeyTick(t, f, 6) // 16 minutes old: expired, first re-prompt.
	if s := statusText(); !strings.Contains(s, "retry signal: expired;") || !strings.Contains(s, "decision:  reprompt;") {
		t.Fatalf("status does not explain the expired retry signal:\n%s", s)
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

	// The re-prompt starts a turn, which logs a retry notice and then fails.
	add(6, "client/turn/requested", `{}`)
	add(6, "turn/started", turn)
	add(7, "provider/error", notice)
	status("active")
	journeyTick(t, f, 8) // Active with a fresh notice: left alone.
	add(9, "turn/completed", failed)
	add(10, "provider/error", notice)
	status("error")
	journeyTick(t, f, 12) // Errored again; its notice is 2 minutes old.
	journeyTick(t, f, 26) // Expired: second and last re-prompt, not a fresh count.

	// Same again, but this turn goes silent while BB still reports it active.
	add(26, "client/turn/requested", `{}`)
	add(26, "turn/started", turn)
	add(27, "provider/error", notice)
	status("active")
	journeyTick(t, f, 28)
	journeyTick(t, f, 43) // 16 minutes of silence: the notice no longer suppresses stall handling.
	if s := statusText(); !strings.Contains(s, "retry signal: expired;") || !strings.Contains(s, "decision reason: active") {
		t.Fatalf("silent active thread still reported as retrying:\n%s", s)
	}
	status("error")
	journeyTick(t, f, 44) // Allowance spent: a person is asked.
	journeyTick(t, f, 45) // Held; the provider is not queried.
	// Both re-prompts retried the failed turn by reference; no prompt was sent.
	if retries, tells := strings.Count(f.read("calls"), "thread retry thr_primary "), strings.Count(f.read("calls"), "thread tell thr_primary "); retries != 2 || tells != 0 {
		t.Fatalf("turn retries=%d prompts=%d", retries, tells)
	}

	r := isolatedJourney(t, f, "enable,none,reprompt,none,none,reprompt,none,none,await_user,none")
	var got []string
	for _, check := range r[1:9] {
		o := check.Observation
		if o.ErrorClass != "unknown" || check.TargetKeyAfter != check.TargetKey {
			t.Fatal(check)
		}
		got = append(got, o.State+"/"+o.Retry+"/"+check.Cause)
	}
	want := "error/retrying/retrying,error/expired/other,active/retrying/retrying,error/retrying/retrying,error/expired/other,active/retrying/retrying,active/expired/active,error/expired/other"
	if strings.Join(got, ",") != want {
		t.Fatalf("journey\n got %s\nwant %s", strings.Join(got, ","), want)
	}
	if in := r[8].Decisions.Evaluations[0].Input; in.Reprompts != 2 || in.MaxReprompts != 2 || !r[8].ControlAfter.AwaitingUser || r[9].Cause != "awaiting_user" {
		t.Fatal("hold not reached through the spent re-prompt allowance", r[8], r[9])
	}
	code, text, errs := timelineCLI(t, filepath.Join(f.dir, "state"))
	if code != 0 || strings.Count(text, "retry expired (stale provider retry notice no longer honored; ordinary handling applied)") < 4 || strings.Contains(text, journeyPrivate) {
		t.Fatalf("timeline does not explain the expired retry signal: %d %s\n%s", code, errs, text)
	}
	// Once held, status reports the hold; the expiry stays in the timeline.
	if s := statusText(); strings.Contains(s, "retry signal:") {
		t.Fatalf("held check claimed an observation:\n%s", s)
	}
}
