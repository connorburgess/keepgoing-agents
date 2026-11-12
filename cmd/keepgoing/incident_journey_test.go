//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
)

const journeyPrivate = "PRIVATE_JOURNEY_CONTENT"

func journeyConfig(t *testing.T, f *fixture, agent string, reprompts int) {
	t.Helper()
	write(t, f.cfg, fmt.Sprintf(`version: 1
loop:
  name: incident-example
  state_dir: state
  prompt: %s
  errors: {max_reprompts: %d, backoff: 1s, failover_on: []}
%s
`, journeyPrivate, reprompts, agent), 0600)
}

func journeyCommand(t *testing.T, f *fixture, args ...string) {
	t.Helper()
	if code, _, errs := f.run(args...); code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errs)
	}
}

// Each command reloads configuration and persisted state. Artificial check time
// avoids sleeping through backoffs; it does not change any live clock or limit.
func journeyTick(t *testing.T, f *fixture, minute int) {
	t.Helper()
	now := time.Date(2030, 1, 1, 0, minute, 0, 0, time.UTC)
	journeyCommand(t, f, "tick", "--now", now.Format(time.RFC3339))
}

// Reconstruct from a copy containing ONLY incidents.json: no control/state,
// BB cursor/log, runner output, config, or transcript is available to the reader.
func isolatedJourney(t *testing.T, f *fixture, want string) []incident.Record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "state", incident.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{journeyPrivate, f.dir, "thr_primary", "provider_private", "session_private", os.Getenv("KEEPGOING_WEBHOOK_URL"), secret} {
		if private != "" && strings.Contains(string(b), private) {
			t.Fatal("private fixture data entered incident journal")
		}
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, incident.FileName), string(b), 0600)
	j, err := incident.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if j.SchemaVersion != 6 || j.Dropped != 0 || j.NextSequence != uint64(len(j.Records)+1) {
		t.Fatal(j)
	}
	var got []string
	for i, r := range j.Records {
		if r.Sequence != uint64(i+1) || r.CheckStartedAt.IsZero() || r.Build != version || r.Observation.SourceTime != "unavailable" {
			t.Fatal(r)
		}
		if r.Kind == "control" {
			got = append(got, r.ControlChange.Operation)
		} else {
			got = append(got, r.Action)
		}
	}
	if strings.Join(got, ",") != want {
		t.Fatalf("journey=%s want=%s", strings.Join(got, ","), want)
	}
	// The public offline command reconstructs the same retained evidence without
	// adapter logs or configuration. Current control is explicitly not recorded.
	code, output, errs := timelineCLI(t, dir, "--json", "--limit", "320")
	if code != 0 {
		t.Fatalf("timeline: %d %s", code, errs)
	}
	var view timelineReport
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		t.Fatal(err)
	}
	if view.Control.Availability != "not_recorded" || len(view.Events) != len(j.Records) || view.Summary.LatestCheck == nil || view.Summary.LastHoldEntry == nil || view.Summary.HeldChecks < 1 {
		t.Fatal("timeline lost journey evidence", view.Summary)
	}
	if strings.Contains(output, journeyPrivate) || strings.Contains(output, f.dir) {
		t.Fatal("timeline leaked private content")
	}
	after, _ := os.ReadFile(filepath.Join(dir, incident.FileName))
	if string(after) != string(b) {
		t.Fatal("reconstruction modified evidence")
	}
	return j.Records
}

func assertJourneyHold(t *testing.T, hold, held, resume incident.Record) {
	t.Helper()
	if hold.ControlBefore.AwaitingUser || !hold.ControlAfter.AwaitingUser || hold.Action != "await_user" {
		t.Fatal("missing persisted escalation", hold)
	}
	if len(hold.Decisions.Evaluations) != 1 {
		t.Fatal("missing hold decision", hold)
	}
	in := hold.Decisions.Evaluations[0].Input
	if in.State != "error" || in.Reprompts != 1 || in.MaxReprompts != 1 {
		t.Fatal("hold has no recorded exhausted allowance", hold)
	}
	if !held.ControlBefore.AwaitingUser || !held.ControlAfter.AwaitingUser || held.Cause != "awaiting_user" || held.Observation.Availability != "not_observed" || held.Decisions.Availability != "captured" || len(held.Decisions.Evaluations) != 0 {
		t.Fatal("recovery silently cleared hold or invented observation", held)
	}
	if resume.Kind != "control" || resume.ControlChange.Operation != "resume" || !resume.ControlBefore.AwaitingUser || resume.ControlAfter.AwaitingUser {
		t.Fatal("missing explicit resume", resume)
	}
}

func TestIncidentJourneyBBRetryHoldResumeRecovery(t *testing.T) {
	f := newFixture(t)
	journeyConfig(t, f, fmt.Sprintf("  adapter: bb\n  bb: {thread: thr_primary, cli: %q}", filepath.Join(f.dir, "bin", "bb")), 1)
	show := filepath.Join(f.bbDir, "thr_primary.show")
	log := filepath.Join(f.bbDir, "thr_primary.log")
	events := `[{"seq":1,"type":"turn/started","data":{"providerThreadId":"provider_private"}},
 {"seq":2,"type":"provider/error","data":{"message":"PRIVATE_JOURNEY_CONTENT","willRetry":true,"errorInfo":{"category":"stream-disconnected","httpStatusCode":503,"providerCode":"responseStreamDisconnected"}}}`
	write(t, show, `{"thread":{"status":"active"}}`, 0600)
	write(t, log, events+`]`, 0600)
	journeyCommand(t, f, "enable")
	journeyTick(t, f, 0)
	journeyTick(t, f, 10) // A fresh CLI/adapter reloads the persisted retry summary.
	events += `,{"seq":3,"type":"provider/error","data":{"willRetry":false,"errorInfo":{"category":"stream-disconnected","httpStatusCode":503,"providerCode":"responseStreamDisconnected"}}}`
	write(t, show, `{"thread":{"status":"error"}}`, 0600)
	write(t, log, events+`]`, 0600)
	journeyTick(t, f, 11) // One bounded re-prompt.
	journeyTick(t, f, 12) // Its allowance is spent: hold.
	events += `,{"seq":4,"type":"item/agentMessage/delta","data":{"providerThreadId":"provider_private","itemId":"item_example","delta":"PRIVATE_JOURNEY_CONTENT"}}`
	write(t, show, `{"thread":{"status":"active"}}`, 0600)
	write(t, log, events+`]`, 0600)
	calls := f.read("calls")
	journeyTick(t, f, 13)
	if f.read("calls") != calls {
		t.Fatal("held check queried provider")
	}
	journeyCommand(t, f, "resume", "--note", journeyPrivate)
	journeyTick(t, f, 14)
	r := isolatedJourney(t, f, "enable,none,none,reprompt,await_user,none,resume,none")
	for _, retry := range r[1:3] {
		o := retry.Observation
		if retry.Cause != "retrying" || o.State != "active" || o.ErrorClass != "network" || o.Retry != "retrying" || o.Source != "bb_provider_error" || o.ProviderCode != "responsestreamdisconnected" || o.HTTPStatus == nil || *o.HTTPStatus != 503 || retry.Nudges != 0 {
			t.Fatal(retry)
		}
	}
	if r[3].Observation.Retry != "not_retrying" || r[3].Reprompts != 1 || r[4].Decisions.Evaluations[0].Input.Reprompts != 1 {
		t.Fatal("lost retry exhaustion")
	}
	assertJourneyHold(t, r[4], r[5], r[6])
	o := r[7].Observation
	if o.State != "active" || o.ErrorClass != "none" || o.Retry != "not_retrying" || o.Source != "bb_status" || o.ProviderCode != "unavailable" || o.HTTPStatus != nil {
		t.Fatal("stale failure survived verified recovery", r[7])
	}
}

func TestIncidentJourneyBBInterruptedCommandRecovery(t *testing.T) {
	f := newFixture(t)
	journeyConfig(t, f, fmt.Sprintf("  adapter: bb\n  bb: {thread: thr_primary, cli: %q}", filepath.Join(f.dir, "bin", "bb")), 1)
	show := filepath.Join(f.bbDir, "thr_primary.show")
	log := filepath.Join(f.bbDir, "thr_primary.log")
	journeyCommand(t, f, "enable")
	// A CLI transport failure and an invalid reply both leave state unknown.
	cli := filepath.Join(f.dir, "bin", "bb")
	write(t, cli, "#!/bin/sh\nexit 1\n", 0700)
	if code, _, _ := f.run("tick", "--now", "2030-01-01T00:00:00Z"); code == 0 {
		t.Fatal("failed observation reported success")
	}
	write(t, cli, fakeBB, 0700)
	write(t, show, "invalid-json", 0600)
	if code, _, _ := f.run("tick", "--now", "2030-01-01T00:01:00Z"); code == 0 {
		t.Fatal("invalid observation reported success")
	}
	if f.read("thr_primary.told") != "" {
		t.Fatal("unreadable status caused a prompt")
	}
	// This generic code does not prove a host outage, even when prose claims one.
	events := `[{"seq":1,"type":"turn/started","data":{"providerThreadId":"provider_private"}},
 {"seq":2,"type":"turn/completed","data":{"providerThreadId":"provider_private","status":"interrupted"}},
 {"seq":3,"type":"system/error","data":{"code":"thread_command_failed","message":"PRIVATE_JOURNEY_CONTENT host disconnected; rate limit; authentication failed","detail":"PRIVATE_JOURNEY_CONTENT"}}`
	write(t, log, events+`]`, 0600)
	write(t, show, `{"thread":{"status":"error","queuedMessageCount":1}}`, 0600)
	journeyTick(t, f, 2)
	if f.read("thr_primary.told") != "" {
		t.Fatal("queued work caused a duplicate prompt")
	}
	write(t, show, `{"thread":{"status":"error"}}`, 0600)
	journeyTick(t, f, 3)
	sent := f.read("thr_primary.told")
	if sent != journeyPrivate+"\n" {
		t.Fatalf("expected one continuation prompt, got %q", sent)
	}
	// The same recorded failure on a later check exhausts the configured cap.
	journeyTick(t, f, 4)
	events += `,{"seq":4,"type":"turn/started","data":{"providerThreadId":"provider_private"}}`
	write(t, log, events+`]`, 0600)
	write(t, show, `{"thread":{"status":"active"}}`, 0600)
	calls := f.read("calls")
	journeyTick(t, f, 5)
	if f.read("calls") != calls || f.read("thr_primary.told") != sent {
		t.Fatal("recovery bypassed the persisted hold")
	}
	journeyCommand(t, f, "resume")
	journeyTick(t, f, 6)
	r := isolatedJourney(t, f, "enable,none,none,none,reprompt,await_user,none,resume,none")
	for _, failed := range r[1:3] {
		if failed.Observation.Availability != "unavailable" || failed.Outcome != "failed" || failed.Reprompts != 0 || len(failed.Decisions.Evaluations) != 0 {
			t.Fatal("unavailable observation invented a decision or spent a retry", failed)
		}
	}
	for _, failed := range r[3:6] {
		o := failed.Observation
		if o.Source != "bb_system_error" || o.ProviderCode != "thread_command_failed" || o.ErrorClass != "unknown" || o.Category != "unavailable" || o.HTTPStatus != nil || o.SourceTime != "unavailable" {
			t.Fatal("generic failure lost its code or acquired an invented cause", failed)
		}
	}
	if r[3].Observation.Queued != 1 || r[3].Reprompts != 0 || r[4].Reprompts != 1 {
		t.Fatal("queued work spent an attempt or the send was not counted")
	}
	assertJourneyHold(t, r[5], r[6], r[7])
	if o := r[8].Observation; o.State != "active" || o.ErrorClass != "none" || o.ProviderCode != "unavailable" {
		t.Fatal("new turn retained old failure", o)
	}
	if f.read("probes") != "" || strings.Contains(f.read("calls"), "thread retry") || f.read("thr_primary.told") != sent {
		t.Fatal("recovery probed, retried a turn by reference, or sent extra prompts")
	}
}

func TestIncidentJourneyProcessHoldResumeRecovery(t *testing.T) {
	f := newFixture(t)
	journeyConfig(t, f, `  adapter: process
  process:
    status_command: [sh, -c, 'cat "$FAKE_BB/status"']
    resume_command: [sh, -c, 'cat > "$FAKE_BB/sent"']`, 1)
	status := filepath.Join(f.bbDir, "status")
	write(t, status, `{"state":"error","errorClass":"network","message":"PRIVATE_JOURNEY_CONTENT"}`, 0600)
	journeyCommand(t, f, "enable")
	journeyTick(t, f, 0)
	journeyTick(t, f, 1)
	write(t, status, `{"state":"active","errorClass":"none"}`, 0600)
	journeyTick(t, f, 2)
	journeyCommand(t, f, "resume", "--note", journeyPrivate)
	journeyTick(t, f, 3)
	r := isolatedJourney(t, f, "enable,reprompt,await_user,none,resume,none")
	for _, failed := range r[1:3] {
		o := failed.Observation
		if o.ErrorClass != "network" || o.Retry != "unavailable" || o.Source != "process_json" || o.ExitCode == nil || *o.ExitCode != 0 || o.HTTPStatus != nil {
			t.Fatal(failed)
		}
	}
	assertJourneyHold(t, r[2], r[3], r[4])
	if o := r[5].Observation; o.State != "active" || o.ErrorClass != "none" || o.Retry != "unavailable" {
		t.Fatal(r[5])
	}
	if f.read("sent") != journeyPrivate {
		t.Fatal("local resume command was not exercised")
	}
}

func waitJourneyTurn(t *testing.T, dir runner.Dir, wantExit int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, err := dir.Status()
		if err == nil && s.Phase == runner.Finished {
			if !s.ExitCodeKnown || s.ExitCode != wantExit {
				t.Fatalf("fixture exit=%d known=%v", s.ExitCode, s.ExitCodeKnown)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("local fixture turn did not finish")
}

func TestIncidentJourneyHeadlessHoldRevisedAllowanceResumeRecovery(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			cli := filepath.Join(f.dir, "bin", "fixture-provider")
			write(t, cli, `#!/bin/sh
cat > /dev/null
printf '%s\n' "$*" >> "$FAKE_BB/invocations"
if [ -f "$FAKE_BB/recover" ]; then cat "$FAKE_BB/good"; exit 0; fi
cat "$FAKE_BB/bad"
exit 1
`, 0700)
			bad := `{"type":"result","is_error":true,"subtype":"connection_error","session_id":"session_private","result":"PRIVATE_JOURNEY_CONTENT"}`
			good := `{"type":"result","is_error":false,"subtype":"success","session_id":"session_private","result":"PRIVATE_JOURNEY_CONTENT"}`
			source := "claude_result"
			if kind == "codex" {
				bad = "{\"type\":\"thread.started\",\"thread_id\":\"session_private\"}\n" + `{"type":"turn.failed","error":{"code":"connection_error","message":"PRIVATE_JOURNEY_CONTENT"}}`
				good = "{\"type\":\"thread.started\",\"thread_id\":\"session_private\"}\n" + `{"type":"turn.completed","text":"PRIVATE_JOURNEY_CONTENT"}`
				source = "codex_events"
			}
			write(t, filepath.Join(f.bbDir, "bad"), bad, 0600)
			write(t, filepath.Join(f.bbDir, "good"), good, 0600)
			agent := fmt.Sprintf("  adapter: %s\n  %s: {cli: %q, workdir: %q}", kind, kind, cli, f.dir)
			journeyConfig(t, f, agent, 1)
			dir := runner.Dir(filepath.Join(f.dir, "state", "targets", "primary"))
			defer dir.Stop()
			journeyCommand(t, f, "enable")
			journeyTick(t, f, 0)
			waitJourneyTurn(t, dir, 1)
			journeyTick(t, f, 1)
			waitJourneyTurn(t, dir, 1)
			journeyTick(t, f, 2)
			// Repair the fictional provider and revise the explicit retry allowance.
			// Changing config alone cannot clear the already-persisted hold.
			write(t, filepath.Join(f.bbDir, "recover"), "", 0600)
			journeyConfig(t, f, agent, 2)
			before := f.read("invocations")
			journeyTick(t, f, 3)
			if f.read("invocations") != before {
				t.Fatal("held check started another turn")
			}
			journeyCommand(t, f, "resume", "--note", journeyPrivate)
			journeyTick(t, f, 4)
			waitJourneyTurn(t, dir, 0)
			journeyTick(t, f, 5)
			waitJourneyTurn(t, dir, 0)
			r := isolatedJourney(t, f, "enable,wake,reprompt,await_user,none,resume,reprompt,wake")
			assertJourneyHold(t, r[3], r[4], r[5])
			for _, failed := range []incident.Record{r[2], r[3], r[6]} {
				o := failed.Observation
				if o.Source != source || o.ErrorClass != "network" || o.ProviderCode != "connection_error" || o.ExitCode == nil || *o.ExitCode != 1 || o.Retry != "unavailable" || o.HTTPStatus != nil {
					t.Fatal(failed)
				}
			}
			in := r[6].Decisions.Evaluations[0].Input
			if in.Reprompts != 1 || in.MaxReprompts != 2 || r[6].Reprompts != 2 {
				t.Fatal("resume refunded attempts or lost revised allowance", r[6])
			}
			o := r[7].Observation
			if o.Source != source || o.State != "idle" || o.ErrorClass != "none" || o.ExitCode == nil || *o.ExitCode != 0 || o.Retry != "unavailable" || r[7].Reprompts != 0 {
				t.Fatal("missing recovered process evidence", r[7])
			}
			if calls := f.read("invocations"); strings.Count(calls, "\n") != 4 || !strings.Contains(calls, "resume") {
				t.Fatal("local multi-turn/session-resume path not exercised", calls)
			}
		})
	}
}
