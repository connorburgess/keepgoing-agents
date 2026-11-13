package supervise

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// limitWaitWorld is a loop with failover off whose primary reports a limit
// that resets in resetIn. Every tick loads state from disk, as a restarted
// supervisor would; nothing but the fake adapters lives in memory.
func limitWaitWorld(t *testing.T, resetIn time.Duration) (*world, *fakeAdapter) {
	t.Helper()
	w := newWorld(t, true, false)
	w.cfg.Loop.Errors.FailoverOn = []string{}
	w.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: 6 * time.Hour}
	w.cfg.Loop.Report = &config.Report{Events: []string{"limit_wait", "await_user"}}
	obs := limited
	obs.ResetsAt = t0.Add(resetIn)
	return w, w.primary(obs)
}

func limitWaitReports(w *world) []report.Event {
	var out []report.Event
	for _, e := range w.reports {
		if e.Type == "limit_wait" {
			out = append(out, e)
		}
	}
	return out
}

// waitTick runs one check and requires that the report changed nothing about it.
func waitTick(t *testing.T, w *world, at time.Time) Result {
	t.Helper()
	res := runTick(t, w, at)
	if res.Action != "none" || !strings.HasPrefix(res.Reason, "limit resets in ") {
		t.Fatalf("not a limit wait: %+v", res)
	}
	if ctl, err := control.Load(w.dir); err != nil || ctl.AwaitingUser || ctl.Paused || !ctl.Enabled {
		t.Fatalf("the loop was held: %+v %v", ctl, err)
	}
	return res
}

func savedLimitWait(t *testing.T, w *world) *state.LimitWaitReport {
	t.Helper()
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	return st.LimitWaitReport
}

func TestLongLimitWaitIsReportedOncePerWait(t *testing.T) {
	w, p := limitWaitWorld(t, 72*time.Hour)
	// Two days of checks, each from freshly loaded state.
	for i := 0; i < 288; i++ {
		waitTick(t, w, t0.Add(time.Duration(i)*10*time.Minute))
	}
	got := limitWaitReports(w)
	if len(got) != 1 || len(w.reports) != 1 || len(p.sent) != 0 {
		t.Fatalf("%d limit_wait reports, all reports %s, sends %d", len(got), w.reportTypes(), len(p.sent))
	}
	e := got[0]
	if !e.At.Equal(t0) || e.Loop != "example-loop" || !strings.Contains(e.Text, "resets in about 72h0m0s") || !strings.Contains(e.Text, "failover disabled") || !strings.Contains(e.Text, "keeps checking") {
		t.Fatalf("%+v", e)
	}
	if e.Data["target"] != state.Primary || e.Data["reason"] != "limit_failover_disabled" || e.Data["reportWaitAfter"] != "6h0m0s" || e.Data["report"] != 1 || !e.Data["resetsAt"].(time.Time).Equal(t0.Add(72*time.Hour)) || len(e.Data) != 5 {
		t.Fatalf("%+v", e.Data)
	}
	rec := savedLimitWait(t, w)
	if rec == nil || rec.Target != state.Primary || rec.Outcome != state.LimitWaitRequested || rec.Reports != 1 || !rec.DetectedAt.Equal(t0) || !rec.ResetsAt.Equal(t0.Add(72*time.Hour)) {
		t.Fatalf("%+v", rec)
	}
	// History marks only the check that detected it; the rest of the wait
	// still collapses around it.
	j, err := incident.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, r := range j.Records {
		if r.LimitWaitReport != "none" {
			marked++
			if r.Sequence != 1 || r.LimitWaitReport != "requested" || r.Cause != "limit_failover_disabled" || r.Action != "none" || r.Tier != incident.TierCondition || r.Collapsed != nil {
				t.Fatalf("%+v", r)
			}
		}
	}
	if marked != 1 || len(j.Records) != 3 || j.Collapsed != 285 {
		t.Fatalf("marked %d, %d records, collapsed %d", marked, len(j.Records), j.Collapsed)
	}
}

func TestLimitWaitReportThresholdAndUnknownReset(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resetIn time.Duration
		reports int
	}{
		{"below", 5 * time.Hour, 0},
		{"exactly at", 6 * time.Hour, 0},
		{"just above", 6*time.Hour + time.Minute, 1},
	} {
		w, _ := limitWaitWorld(t, tc.resetIn)
		waitTick(t, w, t0)
		waitTick(t, w, t0.Add(10*time.Minute))
		if got := limitWaitReports(w); len(got) != tc.reports || (savedLimitWait(t, w) != nil) != (tc.reports == 1) {
			t.Fatalf("%s: %d reports, saved %+v", tc.name, len(got), savedLimitWait(t, w))
		}
	}
	// A configured threshold is honoured.
	w, _ := limitWaitWorld(t, 5*time.Hour)
	w.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: time.Hour}
	waitTick(t, w, t0)
	if len(limitWaitReports(w)) != 1 {
		t.Fatal("configured threshold ignored")
	}
	// Without a reported reset the engine does not wait: it re-prompts within
	// its bound and then holds for a person, which has its own report.
	w, p := limitWaitWorld(t, 0)
	p.obs.ResetsAt = time.Time{}
	var actions []string
	for i := 0; i < 4; i++ {
		res := runTick(t, w, t0.Add(time.Duration(i)*11*time.Minute))
		actions = append(actions, res.Action)
	}
	if strings.Join(actions, ",") != "reprompt,reprompt,await_user,none" || len(limitWaitReports(w)) != 0 || !strings.Contains(w.reportTypes(), "await_user") || savedLimitWait(t, w) != nil {
		t.Fatalf("%v %s", actions, w.reportTypes())
	}
}

func TestLimitWaitReportRearmsWhenTheResetMovesOrANewWaitBegins(t *testing.T) {
	w, p := limitWaitWorld(t, 24*time.Hour)
	step := 0
	tick := func() {
		t.Helper()
		waitTick(t, w, t0.Add(time.Duration(step)*10*time.Minute))
		step++
	}
	want := func(n int, why string) {
		t.Helper()
		if got := len(limitWaitReports(w)); got != n {
			t.Fatalf("%s: %d reports, want %d", why, got, n)
		}
	}
	tick()
	want(1, "first detection")
	// The provider restates the reset a little later, then earlier: the same wait.
	p.obs.ResetsAt = t0.Add(24*time.Hour + 59*time.Minute)
	tick()
	p.obs.ResetsAt = t0.Add(20 * time.Hour)
	tick()
	want(1, "immaterial movement")
	if rec := savedLimitWait(t, w); !rec.ResetsAt.Equal(t0.Add(24 * time.Hour)) {
		t.Fatalf("the reported reset was rewritten: %+v", rec)
	}
	// The reset moves materially later than the one reported: a person's
	// expectation is now wrong, so it is reported again, once.
	p.obs.ResetsAt = t0.Add(25*time.Hour + time.Minute)
	tick()
	tick()
	want(2, "reset moved later")
	if rec := savedLimitWait(t, w); !rec.ResetsAt.Equal(t0.Add(25*time.Hour+time.Minute)) || !rec.DetectedAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("%+v", rec)
	}
	// The limit lifts early: the next observed check ends the wait and wakes
	// the agent as it always would.
	p.obs = decide.Observation{State: decide.Idle}
	res := runTick(t, w, t0.Add(time.Duration(step)*10*time.Minute))
	step++
	if res.Action != "wake" || savedLimitWait(t, w) != nil {
		t.Fatalf("early recovery did not take effect: %+v %+v", res, savedLimitWait(t, w))
	}
	want(2, "recovery")
	// A new long wait later is a new wait, even with the same reset time.
	p.obs = limited
	p.obs.ResetsAt = t0.Add(25*time.Hour + time.Minute)
	p.sent = nil
	tick()
	tick()
	want(3, "new wait")
	// A wait that drops under the threshold and stays the same wait is not re-reported.
	for i := 0; i < 3; i++ {
		waitTick(t, w, t0.Add(20*time.Hour+time.Duration(i)*10*time.Minute))
	}
	want(3, "same wait near its reset")
	if savedLimitWait(t, w) == nil {
		t.Fatal("the wait's record was dropped while still waiting")
	}
}

func TestNoLimitWaitReportWhenAHandoffIsPossible(t *testing.T) {
	healthy := func(w *world) {
		reg := &health.Registry{Candidates: map[string]*health.Entry{}}
		for _, id := range []string{"claude-other", "codex-main"} {
			reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
		}
		reg.Save(w.dir)
		w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	}
	setup := func() (*world, *fakeAdapter) {
		w := newWorld(t, true, true)
		w.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: 6 * time.Hour}
		w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
		obs := limited
		obs.ResetsAt = t0.Add(72 * time.Hour)
		return w, w.primary(obs)
	}
	// A confirmed fallback takes the loop: a handoff report, never a limit_wait one.
	w, _ := setup()
	healthy(w)
	if res := runTick(t, w, t0); res.Action != "handoff" || len(limitWaitReports(w)) != 0 || savedLimitWait(t, w) != nil {
		t.Fatalf("%+v %s", res, w.reportTypes())
	}
	// A long wait chosen over an available handoff by wait_if_reset_within is
	// the operator's own setting, not a wait with no way out.
	w, p := setup()
	healthy(w)
	w.cfg.Loop.Errors.WaitIfResetWithin = config.Duration{Duration: 8 * time.Hour}
	p.obs.ResetsAt = t0.Add(7 * time.Hour)
	if res := runTick(t, w, t0); res.Action != "none" || !strings.HasSuffix(res.Reason, "; waiting") || len(limitWaitReports(w)) != 0 || savedLimitWait(t, w) != nil {
		t.Fatalf("%+v %s", res, w.reportTypes())
	}
	// Failover is on but no fallback is confirmed: no handoff is possible.
	w, _ = setup()
	res := waitTick(t, w, t0)
	got := limitWaitReports(w)
	if !strings.Contains(res.Reason, "no confirmed fallback") || len(got) != 1 || got[0].Data["reason"] != "limit_no_fallback" || !strings.Contains(got[0].Text, "no confirmed fallback") {
		t.Fatalf("%+v %+v", res, got)
	}
	waitTick(t, w, t0.Add(10*time.Minute))
	if len(limitWaitReports(w)) != 1 {
		t.Fatal("reported twice")
	}
}

func TestLimitWaitReportRespectsSubscriptionAndNeverRetries(t *testing.T) {
	// Not subscribed: nothing is sent, and the wait is still recorded once.
	w, _ := limitWaitWorld(t, 72*time.Hour)
	w.cfg.Loop.Report = &config.Report{Events: []string{"await_user", "handoff"}}
	waitTick(t, w, t0)
	waitTick(t, w, t0.Add(10*time.Minute))
	if len(w.reports) != 0 || savedLimitWait(t, w).Outcome != state.LimitWaitNotSubscribed {
		t.Fatalf("%s %+v", w.reportTypes(), savedLimitWait(t, w))
	}
	j, _ := incident.Load(w.dir)
	if j.Records[0].LimitWaitReport != "not_subscribed" || j.Records[1].LimitWaitReport != "none" {
		t.Fatalf("%s %s", j.Records[0].LimitWaitReport, j.Records[1].LimitWaitReport)
	}
	// No report section at all behaves the same.
	w.cfg.Loop.Report = nil
	waitTick(t, w, t0.Add(20*time.Minute))
	// Subscribing during the wait sends its one report then.
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	waitTick(t, w, t0.Add(30*time.Minute))
	waitTick(t, w, t0.Add(40*time.Minute))
	if len(limitWaitReports(w)) != 1 || savedLimitWait(t, w).Outcome != state.LimitWaitRequested {
		t.Fatalf("%s %+v", w.reportTypes(), savedLimitWait(t, w))
	}

	// A failed delivery is recorded and not retried by later checks or restarts.
	w, p := limitWaitWorld(t, 72*time.Hour)
	failing := &countingReporter{err: errors.New("report: request failed")}
	for i := 0; i < 5; i++ {
		opts := w.opts(t0.Add(time.Duration(i) * 10 * time.Minute))
		opts.Reporter = failing
		res, err := Tick(context.Background(), w.cfg, opts)
		if err != nil || res.Action != "none" {
			t.Fatalf("a failed report failed or changed the check: %+v %v", res, err)
		}
		if (i == 0) != strings.Contains(strings.Join(res.Warnings, ";"), "report limit_wait") {
			t.Fatalf("check %d warnings: %v", i, res.Warnings)
		}
	}
	if failing.limitWaits != 1 || savedLimitWait(t, w).Outcome != state.LimitWaitFailed || len(p.sent) != 0 {
		t.Fatalf("%d attempts, %+v", failing.limitWaits, savedLimitWait(t, w))
	}
	if j, _ = incident.Load(w.dir); j.Records[0].LimitWaitReport != "delivery_failed" {
		t.Fatal(j.Records[0].LimitWaitReport)
	}
}

type countingReporter struct {
	err        error
	limitWaits int
}

func (c *countingReporter) Report(_ context.Context, e report.Event) error {
	if e.Type == "limit_wait" {
		c.limitWaits++
		return c.err
	}
	return nil
}

func TestHoldsStopAndDryRunTakePrecedenceOverTheLimitWaitReport(t *testing.T) {
	for _, hold := range []string{"paused", "disabled", "awaiting user", "completed"} {
		w, p := limitWaitWorld(t, 72*time.Hour)
		if _, err := control.Update(w.dir, func(f *control.File) {
			switch hold {
			case "paused":
				f.Paused = true
			case "disabled":
				f.Enabled = false
			case "awaiting user":
				f.AwaitingUser = true
			default:
				f.Completed = true
			}
		}); err != nil {
			t.Fatal(err)
		}
		res := runTick(t, w, t0)
		if res.Reason != hold || res.State != "not observed" || len(w.reports) != 0 || savedLimitWait(t, w) != nil || len(p.sent) != 0 {
			t.Fatalf("%s: %+v %s", hold, res, w.reportTypes())
		}
	}
	// A wait already reported is neither repeated nor forgotten while held.
	w, _ := limitWaitWorld(t, 72*time.Hour)
	waitTick(t, w, t0)
	if _, err := control.Update(w.dir, func(f *control.File) { f.Paused = true }); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		runTick(t, w, t0.Add(time.Duration(i)*10*time.Minute))
	}
	if len(limitWaitReports(w)) != 1 || savedLimitWait(t, w) == nil {
		t.Fatalf("%s %+v", w.reportTypes(), savedLimitWait(t, w))
	}
	// Resuming into the same wait stays quiet.
	if _, err := control.Update(w.dir, func(f *control.File) { f.Paused = false }); err != nil {
		t.Fatal(err)
	}
	waitTick(t, w, t0.Add(time.Hour))
	if len(limitWaitReports(w)) != 1 {
		t.Fatal("resume re-reported the same wait")
	}
	// Stop is handled before anything else and requests no limit_wait report.
	w, _ = limitWaitWorld(t, 72*time.Hour)
	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
		t.Fatal(err)
	}
	if res := runTick(t, w, t0); strings.Contains(w.reportTypes(), "limit_wait") || savedLimitWait(t, w) != nil || !strings.HasPrefix(res.Reason, "stop") {
		t.Fatalf("%+v %s", res, w.reportTypes())
	}
	// A dry run decides the same wait, sends nothing and records nothing.
	w, _ = limitWaitWorld(t, 72*time.Hour)
	opts := w.opts(t0)
	opts.DryRun = true
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || res.Action != "none" || len(w.reports) != 0 || savedLimitWait(t, w) != nil {
		t.Fatalf("%+v %v %s", res, err, w.reportTypes())
	}
	if j, _ := incident.Load(w.dir); j.Records[0].LimitWaitReport != "none" {
		t.Fatal(j.Records[0].LimitWaitReport)
	}
}

// The real webhook transport and delivery ledger, against a local server: one
// request per wait whether it is accepted or rejected, and the ledger shows
// the single attempt under the limit_wait event.
func TestLimitWaitReportThroughTheRecordedWebhook(t *testing.T) {
	for _, status := range []int{202, 503} {
		var calls atomic.Int32
		var body atomic.Value
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			b, _ := io.ReadAll(r.Body)
			body.Store(string(b))
			rw.WriteHeader(status)
		}))
		t.Setenv("KG_LIMIT_URL", srv.URL)
		t.Setenv("KG_LIMIT_SECRET", "fictional-limit-wait-key")
		w, _ := limitWaitWorld(t, 72*time.Hour)
		w.cfg.Loop.Prompt = "PRIVATE_TASK_TEXT"
		w.cfg.Loop.Report = &config.Report{URLEnv: "KG_LIMIT_URL", SecretEnv: "KG_LIMIT_SECRET", Scheme: "bearer", Format: "standard", Events: config.DefaultReportEvents, Timeout: config.Duration{Duration: 2 * time.Second}}
		sender, err := report.NewRecorded(*w.cfg.Loop.Report, w.dir)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 4; i++ {
			opts := w.opts(t0.Add(time.Duration(i) * 10 * time.Minute))
			opts.Reporter = sender
			if res, err := Tick(context.Background(), w.cfg, opts); err != nil || res.Action != "none" {
				t.Fatalf("%d: %+v %v", status, res, err)
			}
		}
		srv.Close()
		sent, _ := body.Load().(string)
		if calls.Load() != 1 || !strings.Contains(sent, "limit_wait") || strings.Contains(sent, "PRIVATE_TASK_TEXT") || strings.Contains(sent, "thr_primary") {
			t.Fatalf("%d: %d requests, body %s", status, calls.Load(), sent)
		}
		h, err := report.LoadDeliveries(w.dir)
		wantOutcome, wantSaved := "http_accepted", state.LimitWaitRequested
		if status == 503 {
			wantOutcome, wantSaved = "http_rejected", state.LimitWaitFailed
		}
		if err != nil || len(h.Records) != 1 || h.Records[0].Event != "limit_wait" || h.Records[0].Outcome != wantOutcome || len(h.Records[0].Attempts) != 1 || h.Records[0].Attempts[0].HTTPStatus != status {
			t.Fatalf("%d: %+v %v", status, h.Records, err)
		}
		if rec := savedLimitWait(t, w); rec == nil || rec.Outcome != wantSaved {
			t.Fatalf("%d: %+v", status, rec)
		}
	}
}

// A check that decides something other than the limit wait, without evidence
// that the limit lifted, must not end the wait: each of these used to clear
// the record and produce a second report when the wait resumed.
func TestLimitWaitSurvivesChecksThatAreNotRecovery(t *testing.T) {
	reset := t0.Add(72 * time.Hour)
	for name, flip := range map[string]decide.Observation{
		"provider retrying":       {State: decide.Active, Retrying: true},
		"retry unverified":        {State: decide.Active, RetryUnverified: true},
		"unknown":                 {State: decide.Unknown},
		"pending":                 {State: decide.Pending},
		"stopping":                {State: decide.Stopping},
		"queued after the error":  {State: decide.Error, ErrorClass: decide.ErrRateLimit, Queued: 1, ResetsAt: reset},
		"pending interaction":     {State: decide.Error, ErrorClass: decide.ErrRateLimit, AwaitingInput: true, ResetsAt: reset},
		"child work running":      {State: decide.Error, ErrorClass: decide.ErrRateLimit, ActiveChildren: 1, ResetsAt: reset},
		"child status unverified": {State: decide.Error, ErrorClass: decide.ErrRateLimit, ChildrenUnverified: true, ResetsAt: reset},
	} {
		w, p := limitWaitWorld(t, 72*time.Hour)
		wait := p.obs
		waitTick(t, w, t0)
		for i := 1; i <= 6; i++ {
			p.obs = wait
			if i%2 == 1 {
				p.obs = flip
			}
			runTick(t, w, t0.Add(time.Duration(i)*10*time.Minute))
		}
		if got := len(limitWaitReports(w)); got != 1 || savedLimitWait(t, w) == nil {
			t.Errorf("%s: %d limit_wait reports, saved %+v", name, got, savedLimitWait(t, w))
		}
	}
	// Positive evidence ends the wait, so a later one is reported.
	for name, recovered := range map[string]decide.Observation{
		"idle":                      {State: decide.Idle},
		"working":                   {State: decide.Active},
		"error without a reset":     {State: decide.Error, ErrorClass: decide.ErrRateLimit},
		"error with a passed reset": {State: decide.Error, ErrorClass: decide.ErrRateLimit, ResetsAt: t0.Add(-time.Minute)},
	} {
		w, p := limitWaitWorld(t, 72*time.Hour)
		wait := p.obs
		waitTick(t, w, t0)
		p.obs = recovered
		runTick(t, w, t0.Add(10*time.Minute))
		if savedLimitWait(t, w) != nil {
			t.Errorf("%s: the wait was not ended", name)
		}
		p.obs = wait
		waitTick(t, w, t0.Add(40*time.Minute))
		if got := len(limitWaitReports(w)); got != 2 {
			t.Errorf("%s: %d limit_wait reports, want 2", name, got)
		}
	}
}

// A loop with a daily window is outside it most of the time. A multi-day wait
// is still one wait.
func TestLimitWaitIsReportedOnceAcrossDailyWindows(t *testing.T) {
	w, _ := limitWaitWorld(t, 100*time.Hour)
	hours := []string{"11:00", "13:00"}
	w.cfg.Loop.Window = &config.Window{TZ: "UTC", Days: map[string][]string{"Mon": hours, "Tue": hours, "Wed": hours, "Thu": hours}}
	outside := 0
	for day := 0; day < 4; day++ {
		for _, hour := range []int{0, 1, 6, 12, 20} {
			res := runTick(t, w, t0.Add(time.Duration(day)*24*time.Hour+time.Duration(hour)*time.Hour))
			if strings.Contains(res.Reason, "outside window") {
				outside++
			} else if res.Action != "none" || !strings.HasPrefix(res.Reason, "limit resets in ") {
				t.Fatalf("day %d hour %d: %+v", day, hour, res)
			}
		}
	}
	if got := len(limitWaitReports(w)); got != 1 || outside == 0 {
		t.Fatalf("%d limit_wait reports over four days, %d checks outside the window", got, outside)
	}
}

// wait_if_reset_within exempts a wait from the report only when a confirmed
// fallback made a handoff possible. With no fallback at all, the same short
// wait rule leaves no way out and is reported.
func TestLongWaitInsideWaitIfResetWithinIsReportedWithoutAFallback(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: 6 * time.Hour}
	w.cfg.Loop.Errors.WaitIfResetWithin = config.Duration{Duration: 12 * time.Hour}
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	obs := limited
	obs.ResetsAt = t0.Add(8 * time.Hour)
	w.primary(obs)
	res := waitTick(t, w, t0)
	waitTick(t, w, t0.Add(10*time.Minute))
	got := limitWaitReports(w)
	if !strings.HasSuffix(res.Reason, "; waiting") || len(got) != 1 || got[0].Data["reason"] != "limit_no_fallback" || !strings.Contains(got[0].Text, "no confirmed fallback") {
		t.Fatalf("%+v %+v", res, got)
	}
}

func TestLimitWaitReportsWhenNoFallbackCanTakeOver(t *testing.T) {
	// Every confirmed fallback rejects its brief until it is capped: no
	// handoff is possible, and the wait behind it is reported once.
	w, _, fallbacks := rejectingWorld(t, true) // reset in 5h
	w.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: 4 * time.Hour}
	w.cfg.Loop.Report = &config.Report{Events: []string{"limit_wait"}}
	var last Result
	for minute := 0; minute < 50; minute += 5 {
		last = runTick(t, w, t0.Add(time.Duration(minute)*time.Minute))
	}
	if !strings.Contains(last.Reason, "every confirmed fallback is capped") || last.Action != "none" || len(fallbacks["claude-other"].sent) == 0 {
		t.Fatalf("fixture did not cap its fallbacks: %+v", last)
	}
	if got := limitWaitReports(w); len(got) != 1 || got[0].Data["reason"] != "limit_no_fallback" || !got[0].At.Equal(t0) {
		t.Fatalf("%d limit_wait reports: %+v", len(got), got)
	}

	// Fallbacks are configured and one is healthy, but redundancy needs two.
	w = newWorld(t, true, true)
	w.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: 6 * time.Hour}
	w.cfg.Loop.Report = &config.Report{Events: []string{"limit_wait"}}
	obs := limited
	obs.ResetsAt = t0.Add(48 * time.Hour)
	w.primary(obs)
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	reg.Record("claude-other", probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	reg.Record("codex-main", probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: false, ErrorClass: "auth"})
	reg.Save(w.dir)
	for i := 0; i < 3; i++ {
		res := waitTick(t, w, t0.Add(time.Duration(i)*10*time.Minute))
		if res.Redundancy == nil || res.Redundancy.Meets || !strings.Contains(res.Reason, "no confirmed fallback") {
			t.Fatalf("%+v %+v", res, res.Redundancy)
		}
	}
	if got := limitWaitReports(w); len(got) != 1 || got[0].Data["reason"] != "limit_no_fallback" {
		t.Fatalf("%d limit_wait reports", len(got))
	}
}

// After a handoff the fallback itself hits a limit and nothing else can take
// over: the wait is on the fallback and names it.
func TestLimitWaitOnAFallbackTargetIsReported(t *testing.T) {
	w, _, fb := switchBackWorld(t) // handed off to claude-other
	w.cfg.Loop.SwitchBack.Enabled = false
	w.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: 6 * time.Hour}
	w.cfg.Loop.Report = &config.Report{Events: []string{"limit_wait"}}
	w.probeOK = map[string]bool{}
	w.reports = nil
	// The remaining fallback is no longer confirmed.
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	reg.Record("codex-main", probe.Result{At: t0.Add(30 * time.Minute), Tier: probe.Scheduled, OK: false, ErrorClass: "auth"})
	reg.Save(w.dir)
	fb.obs = limited
	fb.obs.ResetsAt = t0.Add(30 * time.Hour)
	for i := 0; i < 3; i++ {
		res := waitTick(t, w, t0.Add(time.Hour+time.Duration(i)*10*time.Minute))
		if res.Target != "claude-other" {
			t.Fatalf("%+v", res)
		}
	}
	got := limitWaitReports(w)
	if len(got) != 1 || got[0].Data["target"] != "claude-other" || !strings.Contains(got[0].Text, "claude-other is waiting") || savedLimitWait(t, w).Target != "claude-other" {
		t.Fatalf("%+v", got)
	}
}

// A provider whose reset keeps sliding forward gets a growing margin and a
// hard cap for one wait.
func TestSlidingResetCannotProduceAStreamOfReports(t *testing.T) {
	w, p := limitWaitWorld(t, 24*time.Hour)
	var at []int
	for i := 0; i < 40; i++ {
		p.obs.ResetsAt = t0.Add(24*time.Hour + time.Duration(i)*90*time.Minute)
		before := len(limitWaitReports(w))
		waitTick(t, w, t0.Add(time.Duration(i)*10*time.Minute))
		if len(limitWaitReports(w)) > before {
			at = append(at, i)
		}
	}
	// First detection; +90m beats the 1h margin; then the margin is 2h, so
	// two more slides are needed; then the cap.
	if len(at) != 3 || at[0] != 0 || at[1] != 1 || at[2] != 3 {
		t.Fatalf("reports at checks %v", at)
	}
	got := limitWaitReports(w)
	if got[2].Data["report"] != 3 || !strings.Contains(got[2].Text, "moved later") || strings.Contains(got[0].Text, "moved later") || savedLimitWait(t, w).Reports != 3 {
		t.Fatalf("%+v", got[2])
	}
}

type orderReporter struct {
	t      *testing.T
	dir    string
	called int
}

func (o *orderReporter) Report(_ context.Context, e report.Event) error {
	if e.Type != "limit_wait" {
		return nil
	}
	o.called++
	st, err := state.Load(o.dir)
	if err != nil || st.LimitWaitReport == nil || st.LimitWaitReport.Outcome != state.LimitWaitRequested {
		o.t.Errorf("the wait was not saved before the send: %+v %v", st.LimitWaitReport, err)
	}
	return nil
}

// The wait is saved before the request, so a crash during the send cannot
// repeat it, and a state directory that cannot be written sends nothing.
func TestLimitWaitIntentIsSavedBeforeTheSend(t *testing.T) {
	w, _ := limitWaitWorld(t, 72*time.Hour)
	order := &orderReporter{t: t, dir: w.dir}
	opts := w.opts(t0)
	opts.Reporter = order
	if _, err := Tick(context.Background(), w.cfg, opts); err != nil || order.called != 1 {
		t.Fatalf("%d sends, %v", order.called, err)
	}
	// A supervisor killed mid-send left only the saved intent. The restarted
	// one does not send again: at most once, and the interrupted one is lost.
	w, _ = limitWaitWorld(t, 72*time.Hour)
	if err := state.Save(w.dir, state.File{LimitWaitReport: &state.LimitWaitReport{Target: state.Primary, ResetsAt: t0.Add(72 * time.Hour), DetectedAt: t0, Outcome: state.LimitWaitRequested, Reports: 1}}); err != nil {
		t.Fatal(err)
	}
	waitTick(t, w, t0.Add(10*time.Minute))
	if len(w.reports) != 0 {
		t.Fatalf("%s", w.reportTypes())
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return // directory modes do not deny writes here
	}
	w, _ = limitWaitWorld(t, 72*time.Hour)
	if err := os.Chmod(w.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(w.dir, 0o700) })
	for i := 0; i < 3; i++ {
		// The tick itself fails to save; what matters is that nothing was sent.
		Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*10*time.Minute)))
	}
	if len(w.reports) != 0 {
		t.Fatalf("an unwritable state directory still reported: %s", w.reportTypes())
	}
}
