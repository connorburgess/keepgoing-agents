package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
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
	}
	if err := state.Save(dir, spent); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := state.ReserveSend(dir, 10); err != nil {
			t.Fatal(err)
		}
	}
	ledger := filepath.Join(dir, state.ProgressName)
	write(t, ledger, `{"version":1,"fingerprint":"fictional","changedAt":"2030-01-01T00:00:00Z","observedAt":"2030-01-01T01:00:00Z"}`, 0600)
	progress, _ := os.ReadFile(ledger)
	if code, _, errs := f.run("await-user", "--note", "fictional hold"); code != 0 {
		t.Fatal(errs)
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
	if ctl, _ := control.Load(dir); ctl.AwaitingUser || ctl.Note != "" {
		t.Fatalf("%+v", ctl)
	}
}

// A loop held because its re-prompts were spent is not held again by the
// check after a resume: the session, still in error, is recovered again.
// Before, resume cleared the hold and the next check raised it again.
func TestResumeAfterASpentRepromptCapRecoversOnTheNextCheck(t *testing.T) {
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
	if err := state.Save(dir, state.File{Reprompts: cfg.Loop.Errors.MaxReprompts, LastRepromptAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := f.tick(); got["action"] != "await_user" {
		t.Fatalf("spent cap: %v", got)
	}
	if got := f.tick(); got["action"] != "none" {
		t.Fatalf("held: %v", got)
	}
	if code, _, errs := f.run("resume"); code != 0 {
		t.Fatal(errs)
	}
	got := f.tick()
	if got["action"] != "reprompt" {
		t.Fatalf("after resume: %v", got)
	}
	if ctl, _ := control.Load(dir); ctl.AwaitingUser {
		t.Fatal("held again by the check after resume")
	}
	if st, _ := state.Load(dir); st.Reprompts != 1 {
		t.Fatalf("re-prompts counted in the new episode: %d", st.Reprompts)
	}
}

// Declaring the task complete ends a wait on a person: the loop shows
// completed, not awaiting user with the old note. A stop or pause stays.
func TestCompleteOnAHeldLoopEndsTheWaitOnAPerson(t *testing.T) {
	f := newFixture(t)
	for _, cmd := range [][]string{{"enable"}, {"await-user", "--note", "fictional question"}, {"pause"}, {"complete"}} {
		if code, _, errs := f.run(cmd...); code != 0 {
			t.Fatal(cmd, errs)
		}
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := control.Load(cfg.Loop.StateDir)
	if err != nil || !ctl.Completed || ctl.AwaitingUser || !ctl.Paused || ctl.Note != "" {
		t.Fatalf("%+v %v", ctl, err)
	}
}

// The registered resume, used by local clients, is the same explicit act and
// starts a new episode too.
func TestClientResumeStartsANewErrorEpisode(t *testing.T) {
	f := newResumeFixture(t)
	if err := state.Save(f.entry.StateDir, state.File{Reprompts: 3, Nudges: 2, HandoffDeferrals: 1, NewThreadsCreated: 1}); err != nil {
		t.Fatal(err)
	}
	p := f.prepare(t)
	if r, c := f.call("resume", p.Ticket, false, nil); c != 0 || r.ControlSave != "confirmed" || len(r.Issues) != 0 {
		t.Fatal(c, r)
	}
	st, err := state.Load(f.entry.StateDir)
	if err != nil || st.Reprompts != 0 || st.Nudges != 0 || st.HandoffDeferrals != 0 || st.NewThreadsCreated != 1 {
		t.Fatalf("%+v %v", st, err)
	}
}
