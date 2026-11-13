package supervise

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// historyReporter records incident-history reports and fails the first
// `fail` deliveries.
type historyReporter struct {
	got  []string
	fail int
}

func (r *historyReporter) Report(_ context.Context, e report.Event) error {
	if e.Data["cause"] != "incident_history" {
		return nil
	}
	r.got = append(r.got, e.Type)
	if r.fail > 0 {
		r.fail--
		return errors.New("report: request failed")
	}
	return nil
}

// historyChecks runs one check per entry; true means the incident journal
// cannot be written during that check.
func historyChecks(t *testing.T, rep *historyReporter, blocked ...bool) *world {
	t.Helper()
	w := newWorld(t, true, false)
	w.primary(decide.Observation{State: decide.Active, AwaitingChildren: []string{"thr_child"}})
	for i, b := range blocked {
		var held *lock.Lock
		if b {
			l, err := lock.AcquireNamed(w.dir, "incident")
			if err != nil {
				t.Fatal(err)
			}
			held = l
		}
		opts := w.opts(t0.Add(time.Duration(i) * 10 * time.Minute))
		opts.Reporter = rep
		r, err := Tick(context.Background(), w.cfg, opts)
		if held != nil {
			held.Release()
		}
		if err != nil || !r.Recorded || (r.History == "unavailable") != b {
			t.Fatalf("check %d: %+v %v", i, r, err)
		}
	}
	return w
}

func savedHistory(t *testing.T, w *world) state.File {
	t.Helper()
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A brief collision on the journal's lock is not an outage: one failing
// check reports nothing, so no degraded and recovered pair is sent.
func TestOneFailedJournalWriteReportsNothing(t *testing.T) {
	rep := &historyReporter{}
	w := historyChecks(t, rep, true, false)
	if len(rep.got) != 0 {
		t.Fatal(rep.got)
	}
	if st := savedHistory(t, w); !st.HistoryUnavailableSince.IsZero() || st.HistoryDegradedAttempts != 0 {
		t.Fatalf("%+v", st)
	}
}

// When it persists across checks, at most one degraded report is sent for
// the episode, and one recovered report when the journal is written again.
func TestALastingJournalFailureIsReportedOncePerEpisode(t *testing.T) {
	rep := &historyReporter{}
	w := historyChecks(t, rep, true, true, true, true, false, false)
	if !slices.Equal(rep.got, []string{"degraded", "recovered"}) {
		t.Fatal(rep.got)
	}
	if st := savedHistory(t, w); !st.HistoryUnavailableSince.IsZero() || st.HistoryDegradedSent {
		t.Fatalf("%+v", st)
	}
}

// A degraded report whose delivery fails is tried again by the next checks.
func TestAFailedDegradedReportIsTriedAgain(t *testing.T) {
	rep := &historyReporter{fail: 2}
	historyChecks(t, rep, true, true, true, true, true, false)
	if !slices.Equal(rep.got, []string{"degraded", "degraded", "degraded", "recovered"}) {
		t.Fatal(rep.got)
	}
}

// The attempts are bounded, and a recovery whose degraded report never got
// through is not reported either.
func TestDegradedReportAttemptsAreBounded(t *testing.T) {
	rep := &historyReporter{fail: 100}
	w := historyChecks(t, rep, true, true, true, true, true, true, true, false)
	if len(rep.got) != historyReportAttempts {
		t.Fatal(rep.got)
	}
	for _, typ := range rep.got {
		if typ != "degraded" {
			t.Fatal(rep.got)
		}
	}
	if st := savedHistory(t, w); !st.HistoryUnavailableSince.IsZero() || st.HistoryDegradedAttempts != 0 {
		t.Fatalf("%+v", st)
	}
}
