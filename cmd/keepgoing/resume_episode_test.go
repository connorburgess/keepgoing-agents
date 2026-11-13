//go:build unix

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// An explicit resume is a person saying "try again", so it starts a new error
// episode: everything counted per episode or per stalled turn starts over.
// What is counted across the whole loop is never refunded.
func TestResumeStartsANewErrorEpisodeAndRefundsNoLoopWideLimit(t *testing.T) {
	f := newFixture(t)
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := cfg.Loop.StateDir
	earlier := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	spent := state.File{
		Reprompts: 3, LastRepromptAt: earlier, Nudges: 3, LastWakeAt: earlier,
		SilentRetry:              &state.SilentRetry{Target: state.Primary, Since: earlier},
		HandoffBriefs:            map[string]state.BriefRecord{"fallback-example": {Failed: 3, LastAt: earlier}},
		SwitchBackRejected:       2,
		HandoffDeferrals:         3,
		ExactCut:                 map[string]int{"fallback-example": 2},
		ChildErrorNoticeAttempts: 3,
		NewThreadsCreated:        2, LastNewThreadAt: earlier,
		TurnRetryNoOps: 2,
	}
	if err := state.Save(dir, spent); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := spendSends(dir, 10); err != nil {
			t.Fatal(err)
		}
	}
	ledger := filepath.Join(dir, state.ProgressName)
	write(t, ledger, `{"version":1,"fingerprint":"fictional","changedAt":"2030-01-01T00:00:00Z","observedAt":"2030-01-01T01:00:00Z"}`, 0600)
	progress, _ := os.ReadFile(ledger)
	// The hold a check raises when the re-prompt cap is spent.
	if _, err := control.Update(dir, func(c *control.File) {
		c.AwaitingUser, c.EpisodeHold, c.Note = true, true, "error persisted after re-prompts"
	}); err != nil {
		t.Fatal(err)
	}

	if code, _, errs := f.run("resume"); code != 0 {
		t.Fatal(errs)
	}
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Reprompts != 0 || !st.LastRepromptAt.IsZero() || st.Nudges != 0 || st.SilentRetry != nil || len(st.HandoffBriefs) != 0 ||
		st.SwitchBackRejected != 0 || st.HandoffDeferrals != 0 || len(st.ExactCut) != 0 || st.ChildErrorNoticeAttempts != 0 {
		t.Fatalf("per-episode counts kept after resume: %+v", st)
	}
	// A provider that keeps answering a turn retry with nothing to do is
	// counted whoever resumed.
	if st.TurnRetryNoOps != 2 {
		t.Fatalf("the turn-retry no-op count was cleared: %d", st.TurnRetryNoOps)
	}
	if st.ResumeEpisodes != 1 || st.LastResumeEpisodeAt.IsZero() {
		t.Fatalf("the episode started by resume was not counted: %d at %v", st.ResumeEpisodes, st.LastResumeEpisodeAt)
	}
	if st.NewThreadsCreated != 2 || !st.LastNewThreadAt.Equal(earlier) {
		t.Fatalf("the new-thread cap was refunded: %d at %v", st.NewThreadsCreated, st.LastNewThreadAt)
	}
	if !st.LastWakeAt.Equal(earlier) {
		t.Fatalf("the time of the last send was lost: %v", st.LastWakeAt)
	}
	if used, err := state.LoadSends(dir); err != nil || used != 4 {
		t.Fatalf("sends used %d after resume, want 4 (%v)", used, err)
	}
	if after, _ := os.ReadFile(ledger); !bytes.Equal(after, progress) {
		t.Fatalf("the no-progress ledger changed on resume: %s", after)
	}
	if ctl, _ := control.Load(dir); ctl.AwaitingUser || ctl.EpisodeHold || ctl.Note != "" {
		t.Fatalf("%+v", ctl)
	}
	// The reset is in the history with who ran it, and status counts it.
	j, err := incident.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	last := j.Records[len(j.Records)-1]
	if c := last.ControlChange; c == nil || c.Operation != "resume" || !c.NewEpisode || c.Caller != "terminal" || last.Tier != incident.TierKey {
		t.Fatalf("%+v", last.ControlChange)
	}
	_, out, _ := f.run("timeline")
	if !strings.Contains(out, "a new error episode started") || !strings.Contains(out, "run by a terminal with no BB thread identity") {
		t.Fatalf("timeline does not show the reset:\n%s", out)
	}
	if _, out, _ = f.run("status"); !strings.Contains(out, "resumed episodes: 1 (last ") {
		t.Fatalf("status does not count the episode:\n%s", out)
	}
}

// Only a hold that keepgoing raised for a spent per-episode allowance starts
// a new episode. A resume with nothing held, or of a hold that a command
// created, starts none. Run before every check, by a person or by the owning
// agent, each of these cycles used to refund the running episode: a session
// in error was re-prompted at every check with no backoff, no cap and no
// hold. Now each gives one re-prompt in eight checks, as if resume had not
// been run.
func TestHoldingAndResumingByHandDoesNotRefundTheRunningEpisode(t *testing.T) {
	for _, hold := range []string{"", "pause", "await-user", "stop", "complete"} {
		name := hold
		if name == "" {
			name = "nothing held"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0600)
			if code, _, errs := f.run("enable"); code != 0 {
				t.Fatal(errs)
			}
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			dir := cfg.Loop.StateDir
			var actions []string
			for i := 0; i < 8; i++ {
				if hold != "" {
					if code, _, errs := f.run(hold); code != 0 {
						t.Fatalf("%s %d: %s", hold, i, errs)
					}
				}
				if code, out, errs := f.run("resume"); code != 0 || strings.Contains(out, "new error episode") {
					t.Fatalf("resume %d: %d %s %s", i, code, out, errs)
				}
				actions = append(actions, fmt.Sprint(f.tick()["action"]))
			}
			if got := strings.Join(actions, ","); got != "reprompt,none,none,none,none,none,none,none" {
				t.Fatalf("%s then resume before each check: %s", name, got)
			}
			st, _ := state.Load(dir)
			if st.Reprompts != 1 || st.LastRepromptAt.IsZero() || st.ResumeEpisodes != 0 {
				t.Fatalf("reprompts=%d last=%v episodes=%d", st.Reprompts, st.LastRepromptAt, st.ResumeEpisodes)
			}
			j, err := incident.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range j.Records {
				if c := r.ControlChange; c != nil && c.Operation == "resume" && (c.NewEpisode || c.Caller != "terminal") {
					t.Fatalf("%+v", c)
				}
			}
			if _, out, _ := f.run("status"); strings.Contains(out, "resumed episodes") {
				t.Fatalf("status counts an episode that was not started:\n%s", out)
			}
		})
	}
}

// A resume that starts no episode still gives a repaired fallback fresh
// takeover briefs, as before.
func TestResumeWithNothingHeldStillClearsRejectedBriefs(t *testing.T) {
	f := newFixture(t)
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := cfg.Loop.StateDir
	st := state.File{Reprompts: 1, LastRepromptAt: time.Now().UTC(), SwitchBackRejected: 2,
		HandoffBriefs: map[string]state.BriefRecord{"fallback-example": {Failed: 3, LastAt: time.Now().UTC()}}}
	if err := state.Save(dir, st); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := f.run("resume"); code != 0 {
		t.Fatal(errs)
	}
	if st, _ = state.Load(dir); len(st.HandoffBriefs) != 0 || st.SwitchBackRejected != 0 || st.Reprompts != 1 || st.ResumeEpisodes != 0 {
		t.Fatalf("%+v", st)
	}
}

// When the counts cannot be reset, resume changes nothing and says so: the
// loop stays held, nothing is written to the history, and the exit code is
// not zero.
func TestResumeChangesNothingWhenTheEpisodeCannotBeSaved(t *testing.T) {
	f := newFixture(t)
	for _, cmd := range []string{"enable", "pause"} {
		if code, _, errs := f.run(cmd); code != 0 {
			t.Fatal(errs)
		}
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := cfg.Loop.StateDir
	if _, err := control.Update(dir, func(c *control.File) { c.AwaitingUser, c.EpisodeHold = true, true }); err != nil {
		t.Fatal(err)
	}
	before, _ := incident.Load(dir)
	write(t, filepath.Join(dir, state.FileName), "fictional broken record", 0600)
	code, out, errs := f.run("resume")
	if code == 0 || !strings.Contains(errs, "could not be reset, so nothing was changed and the loop is still paused") || strings.Contains(out, "new error episode") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if ctl, _ := control.Load(dir); !ctl.AwaitingUser || !ctl.Paused || !ctl.EpisodeHold {
		t.Fatalf("a failed resume changed the holds: %+v", ctl)
	}
	if after, _ := incident.Load(dir); len(after.Records) != len(before.Records) {
		t.Fatal("a failed resume was recorded as a saved control")
	}
}

// Declaring the task complete ends a wait on a person: the loop shows
// completed, not awaiting user. What the person was being asked is not
// thrown away: the hold may have been raised by a check, its note is stored
// nowhere else, and a completed loop stops observing. The completion says a
// question was open, status keeps it until a resume, and the history records
// that the hold was outstanding. A stop or pause stays.
func TestCompleteOnAHeldLoopEndsTheWaitAndKeepsTheQuestion(t *testing.T) {
	f := newFixture(t)
	for _, cmd := range [][]string{{"enable"}, {"await-user", "--note", "fictional question"}, {"pause"}} {
		if code, _, errs := f.run(cmd...); code != 0 {
			t.Fatal(cmd, errs)
		}
	}
	// The completion says a question was open without repeating its note.
	code, out, errs := f.run("complete")
	if code != 0 || !strings.Contains(out, "a question to a person was still open when completion was declared") || strings.Contains(out, "fictional question") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := cfg.Loop.StateDir
	ctl, err := control.Load(dir)
	if err != nil || !ctl.Completed || ctl.AwaitingUser || !ctl.Paused || ctl.Note != "" || ctl.OpenQuestion == nil || ctl.OpenQuestion.Note != "fictional question" {
		t.Fatalf("%+v %v", ctl, err)
	}
	if _, out, _ = f.run("status"); !strings.Contains(out, "open question: the loop was awaiting a person when completion was declared") || !strings.Contains(out, "fictional question") {
		t.Fatalf("status lost the question:\n%s", out)
	}
	// The history has no note, by design; it records that the hold was open.
	_, out, _ = f.run("timeline")
	if !strings.Contains(out, "an awaiting-user hold was outstanding and ended with the completion") || strings.Contains(out, "fictional question") {
		t.Fatalf("timeline:\n%s", out)
	}
	// A second complete does not replace the question, and a new note for
	// the completion does not either.
	if code, out, _ = f.run("complete", "--note", "fictional summary"); code != 0 || strings.Contains(out, "still open") {
		t.Fatalf("%d %s", code, out)
	}
	if ctl, _ = control.Load(dir); ctl.OpenQuestion == nil || ctl.OpenQuestion.Note != "fictional question" || ctl.Note != "fictional summary" {
		t.Fatalf("%+v", ctl)
	}
	// Resume reopens the task and the question goes with the completion.
	for _, cmd := range []string{"resume"} {
		if code, _, errs := f.run(cmd); code != 0 {
			t.Fatal(errs)
		}
	}
	if ctl, _ = control.Load(dir); ctl.OpenQuestion != nil || ctl.Completed {
		t.Fatalf("%+v", ctl)
	}
}

// A completion with no hold outstanding records no open question.
func TestCompleteWithNothingHeldRecordsNoOpenQuestion(t *testing.T) {
	f := newFixture(t)
	for _, cmd := range []string{"enable", "complete"} {
		if code, out, errs := f.run(cmd); code != 0 || strings.Contains(out, "still open") {
			t.Fatal(cmd, out, errs)
		}
	}
	cfg, _ := config.Load(f.cfg)
	if ctl, _ := control.Load(cfg.Loop.StateDir); ctl.OpenQuestion != nil || !ctl.Completed {
		t.Fatalf("%+v", ctl)
	}
}

// A resume settles the sends recorded by a check that could not save what
// happened before it starts a new episode, so they count in the episode that
// is ending. The next check does not count them into the new one. A brief
// that may have put the task on a second conversation is left for the next
// check, which holds for a person.
func TestResumeSettlesUnsavedSendsIntoTheEndingEpisode(t *testing.T) {
	for _, tc := range []struct {
		action, target string
		held           bool
	}{
		{"reprompt", state.Primary, false},
		{"handoff", "fallback-example", true},
	} {
		t.Run(tc.action, func(t *testing.T) {
			f := newFixture(t)
			write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0600)
			if code, _, errs := f.run("enable"); code != 0 {
				t.Fatal(errs)
			}
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			dir := cfg.Loop.StateDir
			at := time.Now().Add(-time.Hour).UTC()
			if err := state.Save(dir, state.File{Reprompts: cfg.Loop.Errors.MaxReprompts, LastRepromptAt: at}); err != nil {
				t.Fatal(err)
			}
			if _, err := state.Reserve(dir, 0, 0, state.SendIntent{At: at.Add(time.Minute), Action: tc.action, Target: tc.target}); err != nil {
				t.Fatal(err)
			}
			if _, err := control.Update(dir, func(c *control.File) {
				c.AwaitingUser, c.EpisodeHold, c.Note = true, true, "error persisted after re-prompts"
			}); err != nil {
				t.Fatal(err)
			}
			if code, _, errs := f.run("resume"); code != 0 {
				t.Fatal(errs)
			}
			got := f.tick()
			st, _ := state.Load(dir)
			if tc.held {
				if got["action"] != "await_user" || !strings.HasPrefix(fmt.Sprint(got["reason"]), "a handoff brief to fallback-example may have been delivered") {
					t.Fatalf("%v", got)
				}
				return
			}
			if got["action"] != "reprompt" || st.Reprompts != 1 || st.UnconfirmedReprompts != 0 {
				t.Fatalf("the unsaved re-prompt was counted into the new episode: %v reprompts=%d unconfirmed=%d", got, st.Reprompts, st.UnconfirmedReprompts)
			}
		})
	}
}

// Resuming a hold for a brief that may have been delivered says which brief
// it settles and where the loop continues: on its current target, with the
// briefed route spaced like a rejected brief and no route briefed on the
// next check. After `retarget` to the briefed route, the loop continues
// there instead.
func TestResumeOfABriefHoldSaysWhereTheLoopContinues(t *testing.T) {
	f := newFixture(t)
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := cfg.Loop.StateDir
	if _, err := control.Update(dir, func(c *control.File) {
		c.AwaitingUser, c.Note = true, "a handoff brief to fallback-example may have been delivered"
		c.BriefHold = []control.Brief{{Action: "handoff", Target: "fallback-example"}}
	}); err != nil {
		t.Fatal(err)
	}
	code, out, errs := f.run("resume")
	if code != 0 || !strings.Contains(out, "the handoff brief to fallback-example is settled as delivered and not taken") || !strings.Contains(out, "the loop continues on primary; no route is briefed on the next check") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	st, _ := state.Load(dir)
	if !st.SkipHandoffOnce || st.HandoffBriefs["fallback-example"].Failed != 1 {
		t.Fatalf("%+v", st)
	}
	if ctl, _ := control.Load(dir); ctl.AwaitingUser || len(ctl.BriefHold) != 0 {
		t.Fatalf("%+v", ctl)
	}
}

// resume --keep-stop settles a brief hold and keeps a requested stop, so a
// person who stopped the loop is not led to undo it.
func TestResumeKeepStopSettlesABriefHoldAndKeepsTheStop(t *testing.T) {
	f := newFixture(t)
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := cfg.Loop.StateDir
	if _, err := control.Update(dir, func(c *control.File) {
		c.Stop, c.AwaitingUser = true, true
		c.BriefHold = []control.Brief{{Action: "handoff", Target: "fallback-example"}}
	}); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := f.run("resume", "--keep-stop"); code != 0 || !strings.Contains(out, "is settled as delivered and not taken") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	ctl, _ := control.Load(dir)
	if !ctl.Stop || ctl.AwaitingUser || len(ctl.BriefHold) != 0 {
		t.Fatalf("%+v", ctl)
	}
	if st, _ := state.Load(dir); len(st.ResumedBriefs) != 1 {
		t.Fatalf("%+v", st.ResumedBriefs)
	}
	if code, _, _ := f.run("pause", "--keep-stop"); code == 0 {
		t.Fatal("--keep-stop accepted by pause")
	}
}

// With max_reprompts 1, a resume of a brief hold must not cap the briefed
// fallback: a cap of one would leave the loop with no fallback, waiting out
// a limit with no hold. The fallback is not spaced either; the next check
// briefs no route. A switch-back brief is spaced by retry_every and never
// counted toward its cap, so the resume cannot end switch-back by itself.
func TestResumeOfABriefHoldNeverCapsTheRoute(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := state.Save(dir, state.File{SwitchBackRejected: 0}); err != nil {
		t.Fatal(err)
	}
	briefs := []control.Brief{{Action: "handoff", Target: "fallback-example"}, {Action: "switch_back", Target: "fallback-example-2"}}
	if err := resumeEpisode(dir, false, at, 1, briefs...); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load(dir)
	r := st.HandoffBriefs["fallback-example"]
	if r.Failed != 0 || !r.LastAt.Equal(at) || st.SwitchBackRejected != 0 || !st.LastSwitchBackTry.Equal(at) || !st.SkipHandoffOnce || len(st.ResumedBriefs) != 2 {
		t.Fatalf("cap 1: %+v", st)
	}
	// With a larger cap the fallback is spaced like a rejected brief. Every
	// resume clears earlier rejected briefs (a repaired fallback gets fresh
	// ones), so this one counts from zero.
	dir = t.TempDir()
	if err := state.Save(dir, state.File{HandoffBriefs: map[string]state.BriefRecord{"fallback-example": {Failed: 2, LastAt: at.Add(-time.Hour)}}}); err != nil {
		t.Fatal(err)
	}
	if err := resumeEpisode(dir, false, at, 3, briefs[0]); err != nil {
		t.Fatal(err)
	}
	if st, _ = state.Load(dir); st.HandoffBriefs["fallback-example"].Failed != 1 || !st.HandoffBriefs["fallback-example"].LastAt.Equal(at) {
		t.Fatalf("cap 3: %+v", st.HandoffBriefs)
	}
}
