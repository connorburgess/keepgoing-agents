package supervise

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// holdOffset runs checks every ten minutes from `from` until the loop is
// held, and returns how long after `from` that was.
func holdOffset(t *testing.T, run func(time.Duration, bool, string) Result, from time.Duration) time.Duration {
	t.Helper()
	for after := time.Duration(0); after <= 12*time.Hour; after += 10 * time.Minute {
		if r := run(from+after, true, "same"); r.Action == "await_user" {
			return after
		}
	}
	t.Fatal("never held")
	return 0
}

// The wait asks a person when the stall rule would have held the same loop:
// stall_after, then max_nudges nudges min_wake_interval apart. Never later,
// whatever min_wake_interval is.
func TestHungTurnHoldIsNoLaterThanTheStallRule(t *testing.T) {
	for _, tc := range []struct {
		name    string
		minWake time.Duration
	}{{"min_wake_interval 10m", 10 * time.Minute}, {"defaults", config.DefaultMinWakeInterval}} {
		t.Run(tc.name, func(t *testing.T) {
			set := func(l *config.Loop) { l.Progress.MinWakeInterval = config.Duration{Duration: tc.minWake} }
			// The stall rule, on the same silent turn with no outage: its
			// stall clock starts at the first check.
			_, sp, srun := hungWorld(t, set)
			stall := holdOffset(t, srun, 0)
			if len(sp.sent) != 3 {
				t.Fatalf("the stall rule sent %d nudges", len(sp.sent))
			}
			// The wait: its stall clock starts when the network is back.
			_, p, run, back := hungJourney(t, 4*time.Hour, set)
			wait := holdOffset(t, run, back)
			want := decide.SilentRetryPatience(30*time.Minute, tc.minWake, 3)
			if wait != want || wait > stall || len(p.sent) != 0 {
				t.Fatalf("held %s after the network came back (want %s), the stall rule %s; %d sends", wait, want, stall, len(p.sent))
			}
		})
	}
}

// A turn that logs something during the wait has come back: the stall clock
// starts again at that check, so it gets a full stall_after before a nudge.
func TestHungTurnThatComesBackGetsAFullStallAfter(t *testing.T) {
	_, p, run, back := hungJourney(t, 4*time.Hour)
	waitFrom(t, run, back)
	p.obs.Turn = &decide.TurnActivity{ID: "turn-example-1", LastActivityAt: t0.Add(back + 45*time.Minute)}
	for _, after := range []time.Duration{50 * time.Minute, 60 * time.Minute, 70 * time.Minute} {
		if r := run(back+after, true, "same"); r.Action != "none" || r.Reason != "active" || len(p.sent) != 0 {
			t.Fatalf("+%s after it came back: %s (%s)", after-50*time.Minute, r.Action, r.Reason)
		}
	}
	if r := run(back+80*time.Minute, true, "same"); r.Action != "nudge" || r.Reason != "no progress for 30m0s" {
		t.Fatalf("stall_after after it came back: %s (%s)", r.Action, r.Reason)
	}
}

// A check that cannot read the turn decides nothing and keeps the watch: the
// next check that can read it waits on it again, without a nudge or a second
// report.
func TestHungTurnWatchSurvivesAFailedLogRead(t *testing.T) {
	w, p, run, back := hungJourney(t, 4*time.Hour)
	waitFrom(t, run, back)
	turn := p.obs.Turn
	p.obs.Turn, p.obs.TurnUnknown, p.obs.RetryUnverified = nil, true, true
	if r := run(back+50*time.Minute, true, "same"); r.Action != "none" || len(p.sent) != 0 {
		t.Fatalf("log unreadable: %s (%s)", r.Action, r.Reason)
	}
	if st, _ := state.Load(w.dir); st.HungTurn == nil || st.HungTurn.Since.IsZero() {
		t.Fatalf("the watch was dropped: %+v", st.HungTurn)
	}
	p.obs.Turn, p.obs.TurnUnknown, p.obs.RetryUnverified = turn, false, false
	if r := run(back+60*time.Minute, true, "same"); r.Reason != decide.HungTurnReason || len(p.sent) != 0 {
		t.Fatalf("log readable again: %s (%s)", r.Action, r.Reason)
	}
	if n := strings.Count(w.reportTypes(), "hung_turn"); n != 1 {
		t.Fatalf("%d hung_turn reports", n)
	}
}

// Without Git tracking the stall rule never acts on an active turn, and
// neither does the watch: no wait, no report, no hold, as before. With
// tracking the same journey is waited on and held.
func TestHungTurnNeedsGitTracking(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		untrack := func(l *config.Loop) {
			if !tracked {
				l.Progress.Repositories = nil
			}
		}
		w, p, run, back := hungJourney(t, 4*time.Hour, untrack)
		var last Result
		for after := time.Duration(0); after <= 5*time.Hour; after += 10 * time.Minute {
			last = run(back+after, true, "same")
		}
		ctl, _ := control.Load(w.dir)
		st, _ := state.Load(w.dir)
		hung := strings.Contains(w.reportTypes(), "hung_turn")
		if len(p.sent) != 0 || ctl.AwaitingUser != tracked || hung != tracked || (st.HungTurn != nil) != tracked {
			t.Fatalf("tracked %v: %s (%s), held %v, reports %s, watch %+v, %d sends", tracked, last.Action, last.Reason, ctl.AwaitingUser, w.reportTypes(), st.HungTurn, len(p.sent))
		}
	}
}

// A second outage during the wait is found by the connectivity check made
// before the hold, as the stall rule finds it before a nudge: no hold while
// it lasts, and once the network is back the stall clock and the time since
// it came back start again, so the first check after it is not held and the
// wait resumes after a full stall_after. One report in all.
func TestHungTurnSecondOutageRestartsTheGrace(t *testing.T) {
	w, p, run, back := hungJourney(t, 4*time.Hour)
	waitFrom(t, run, back)
	for after := 50 * time.Minute; after <= 5*time.Hour; after += 10 * time.Minute {
		if r := run(back+after, false, "same"); r.Action != "none" || len(p.sent) != 0 {
			t.Fatalf("+%s offline again: %s (%s)", after, r.Action, r.Reason)
		}
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("held during the second outage")
	}
	again := back + 5*time.Hour + 10*time.Minute
	if r := run(again, true, "same"); r.Action != "none" || r.Reason != "active" {
		t.Fatalf("first check after the second outage: %s (%s)", r.Action, r.Reason)
	}
	if st, _ := state.Load(w.dir); st.HungTurn == nil || !st.HungTurn.BackAt.Equal(t0.Add(again)) || !st.HungTurn.Since.IsZero() {
		t.Fatalf("back not reset: %+v", st.HungTurn)
	}
	if r := run(again+30*time.Minute, true, "same"); r.Reason != decide.HungTurnReason || len(p.sent) != 0 {
		t.Fatalf("stall_after after the second outage: %s (%s)", r.Action, r.Reason)
	}
	if n := strings.Count(w.reportTypes(), "hung_turn"); n != 1 {
		t.Fatalf("%d hung_turn reports", n)
	}
}

// ledgerReporter checks, as each report is sent, that a hung_turn report has
// already been recorded in sends.json.
// It keeps sends.json as it was on disk at that moment: what a supervisor
// killed while delivering it would leave.
type ledgerReporter struct {
	w      *world
	t      *testing.T
	dir    string
	atSend *[]byte
}

func (r ledgerReporter) Report(ctx context.Context, e report.Event) error {
	if e.Type == "hung_turn" {
		l, err := state.LoadLedger(r.dir)
		found := false
		for _, p := range l.Pending {
			found = found || p.Action == "report:hung_turn"
		}
		if err != nil || !found {
			r.t.Errorf("hung_turn sent before it was recorded: %v %+v", err, l.Pending)
		}
		*r.atSend, _ = os.ReadFile(filepath.Join(r.dir, state.SendsName))
	}
	return r.w.Report(ctx, e)
}

// The hung_turn report is recorded in sends.json before it is sent. A
// supervisor killed after sending it, before state.json was saved, does not
// send it again: the next check finds the record and counts it as sent.
func TestHungTurnReportIsRecordedBeforeItIsSent(t *testing.T) {
	w, p, _, back := hungJourney(t, 4*time.Hour)
	var atSend []byte
	check := func(at time.Duration) Result {
		t.Helper()
		opts := w.opts(t0.Add(at))
		opts.Fingerprint = func([]string) (string, error) { return "same", nil }
		opts.Online = func(context.Context) bool { return true }
		opts.Reporter = ledgerReporter{w: w, t: t, dir: w.dir, atSend: &atSend}
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for after := time.Duration(0); after < 30*time.Minute; after += 10 * time.Minute {
		check(back + after)
	}
	before, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	if r := check(back + 30*time.Minute); r.Reason != decide.HungTurnReason || !strings.Contains(w.reportTypes(), "hung_turn") {
		t.Fatalf("the wait did not begin: %s (%s) %s", r.Action, r.Reason, w.reportTypes())
	}
	// Killed after the report went out, before state.json was saved.
	if len(atSend) == 0 {
		t.Fatal("sends.json was not captured when the report was sent")
	}
	if err := os.WriteFile(filepath.Join(w.dir, state.SendsName), atSend, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(w.dir, before); err != nil {
		t.Fatal(err)
	}
	for after := 40 * time.Minute; after <= 60*time.Minute; after += 10 * time.Minute {
		if r := check(back + after); r.Reason != decide.HungTurnReason || len(p.sent) != 0 {
			t.Fatalf("+%s: %s (%s)", after, r.Action, r.Reason)
		}
	}
	if n := strings.Count(w.reportTypes(), "hung_turn"); n != 1 {
		t.Fatalf("%d hung_turn reports: %s", n, w.reportTypes())
	}
}
