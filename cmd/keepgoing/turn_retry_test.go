//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Through the CLI and the real BB adapter against the stand-in bb: an errored
// thread is recovered by `bb thread retry`, and the prompt is sent only when
// BB reports there is nothing to retry or has no such command.
func TestTickRetriesAFailedBBTurnByReference(t *testing.T) {
	for name, tc := range map[string]struct {
		answer    string
		showAfter string // what BB reports once the retry has been asked for
		prompts   int
		counted   int
		recovery  string
		marker    string
		status    string
	}{
		"dispatched again": {"", "", 0, 1, "turn_retry", "retried", "the failed turn was re-run by reference; no prompt was sent"},
		"queued":           {`{"ok":true,"delivery":"queued","turnRequestId":"req_example1","attempt":3,"queuedMessageId":"qm_example1","waitingOn":{"kind":"thread-busy"},"sendAt":null}`, "", 0, 1, "turn_retry", "queued", "the failed turn was re-run by reference; no prompt was sent"},
		"no failed turn":   {`{"ok":false,"error":{"code":"no_failed_turn","message":"PRIVATE_RETRY_MESSAGE"}}`, "", 1, 1, "prompt", "nothing_to_retry", "the provider reported no failed turn, so the prompt was sent instead"},
		// BB's own retry, or a person, restarted the thread after it was observed.
		"no longer in error": {`{"ok":false,"error":{"code":"no_failed_turn","message":"PRIVATE_RETRY_MESSAGE"}}`, `{"thread":{"status":"active"}}`, 0, 0, "turn_retry", "not_in_error", "nothing was sent and no attempt was counted"},
		"BB without retry":   {`{"ok":false,"error":{"code":"unknown_command","message":"unknown command 'retry'"}}`, "", 1, 1, "prompt", "unsupported", "this provider version cannot retry a turn, so the prompt was sent instead"},
		"already queued":     {`{"ok":false,"error":{"code":"retry_already_queued","message":"PRIVATE_RETRY_MESSAGE"}}`, "", 0, 0, "turn_retry", "already_queued", "a retry of that turn was already waiting; nothing was sent and no attempt was counted"},
	} {
		f := newFixture(t)
		write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0o600)
		write(t, filepath.Join(f.bbDir, "thr_primary.log"), `[{"seq":1,"type":"turn/started","data":{"providerThreadId":"provider_example"}},{"seq":2,"type":"turn/completed","data":{"providerThreadId":"provider_example","status":"failed"}},{"seq":3,"type":"system/error","data":{"code":"thread_command_failed"}}]`, 0o600)
		if tc.answer != "" {
			write(t, filepath.Join(f.bbDir, "thr_primary.retry"), tc.answer, 0o600)
		}
		if tc.showAfter != "" {
			write(t, filepath.Join(f.bbDir, "thr_primary.show-after-retry"), tc.showAfter, 0o600)
		}
		f.run("enable")
		v := f.tick()
		calls, told := f.read("calls"), f.read("thr_primary.told")
		if v["action"] != "reprompt" || v["recovery"] != tc.recovery || strings.Count(calls, "thread retry thr_primary --reason keepgoing: recover the failed turn --json") != 1 || strings.Count(calls, "thread tell thr_primary ") != tc.prompts {
			t.Fatalf("%s: %v\n%s", name, v, calls)
		}
		if (told != "") != (tc.prompts == 1) {
			t.Fatalf("%s: told %q", name, told)
		}
		dir := filepath.Join(f.dir, "state")
		st, err := state.Load(dir)
		if err != nil || st.LastTurnRetry == nil || st.LastTurnRetry.Outcome != tc.marker || st.Reprompts != tc.counted {
			t.Fatalf("%s: %+v %v", name, st.LastTurnRetry, err)
		}
		code, out, errs := f.run("status")
		if code != exitOK || !strings.Contains(out, "last recorded turn retry: "+tc.marker+" on primary") || !strings.Contains(out, tc.status) || strings.Contains(out+errs, "PRIVATE_RETRY_MESSAGE") {
			t.Fatalf("%s: status %d\n%s%s", name, code, out, errs)
		}
		code, out, errs = f.run("status", "--json")
		if code != exitOK || !strings.Contains(out, `"turnRetry": {`) || !strings.Contains(out, `"outcome": "`+tc.marker+`"`) {
			t.Fatalf("%s: status json %d\n%s%s", name, code, out, errs)
		}
		code, out, errs = f.run("timeline", "--kind", "turn_retry")
		if code != exitOK || errs != "" || !strings.Contains(out, "TURN RETRY:") || !strings.Contains(out, "last recorded turn retry (a failed turn re-run by reference; not proof it succeeded): incidents.json #") || strings.Contains(out, "PRIVATE_RETRY_MESSAGE") {
			t.Fatalf("%s: timeline %d %s\n%s", name, code, errs, out)
		}
		j, err := incident.Load(dir)
		if err != nil || j.Records[len(j.Records)-1].TurnRetry != tc.marker {
			t.Fatalf("%s: %v", name, err)
		}
		// A counted attempt starts the backoff: the next check asks and sends nothing more.
		if tc.counted == 1 {
			f.tick()
			if c := f.read("calls"); strings.Count(c, "thread retry ") != 1 || strings.Count(c, "thread tell ") != tc.prompts {
				t.Fatalf("%s: repeated inside the backoff\n%s", name, c)
			}
		}
	}
}

// A refusal BB does not explain is not permission to prompt.
func TestTickSendsNothingWhenBBRefusesTheRetry(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0o600)
	write(t, filepath.Join(f.bbDir, "thr_primary.retry"), `{"ok":false,"error":{"code":"thread_host_offline","message":"PRIVATE_RETRY_MESSAGE"}}`, 0o600)
	f.run("enable")
	code, out, errs := f.run("tick")
	// The check is recorded as failed; the scheduler exit code keeps its existing meaning.
	if !strings.Contains(out, `"outcome":"failed"`) || !strings.Contains(errs, "bb thread retry: refused (thread_host_offline)") || strings.Contains(out+errs, "PRIVATE_RETRY_MESSAGE") || f.read("thr_primary.told") != "" || !strings.Contains(out, `"recovery":"turn_retry"`) {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	st, err := state.Load(filepath.Join(f.dir, "state"))
	if err != nil || st.LastTurnRetry == nil || st.LastTurnRetry.Outcome != "refused" || st.Reprompts != 1 {
		t.Fatalf("%+v %v", st.LastTurnRetry, err)
	}
	_, out, _ = f.run("status")
	if !strings.Contains(out, "the provider refused the retry or its result could not be read; nothing was sent") {
		t.Fatal(out)
	}
}
