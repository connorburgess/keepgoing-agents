package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// childReportWorld is an enabled loop subscribed to child_error. Every tick
// loads state from disk, as a restarted supervisor would.
func childReportWorld(t *testing.T) (*world, *fakeAdapter) {
	t.Helper()
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Events: []string{"child_error", "await_user"}}
	return w, w.primary(decide.Observation{State: decide.Idle})
}

func childReports(w *world) []report.Event {
	var out []report.Event
	for _, e := range w.reports {
		if e.Type == "child_error" {
			out = append(out, e)
		}
	}
	return out
}

func savedChildReport(t *testing.T, w *world) *state.ChildErrorReport {
	t.Helper()
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	return st.ChildErrorReport
}

// An outage fails every child. The owner is told once, with counts only;
// again only when a child newly fails; at most three times in an episode; and
// afresh once every covered child has stayed out of error for
// min_wake_interval (one hour here).
func TestChildErrorReportIsSentOncePerNewFailureAndCapped(t *testing.T) {
	w, p := childReportWorld(t)
	all := children("thr_child_a", "thr_child_b", "thr_child_c", "thr_child_d", "thr_child_e", "thr_child_f", "thr_child_g")
	m := time.Minute
	steps := []struct {
		name        string
		at          time.Duration
		errored     []string
		running     int
		wantReports int // total child_error reports so far
		wantOutcome string
	}{
		{"children running, none in error", 0, nil, 3, 0, ""},
		{"outage: three in error, two still running", 1 * m, all[:3], 2, 1, state.ChildReportRequested},
		{"unchanged", 2 * m, all[:3], 2, 1, state.ChildReportRequested},
		{"unchanged after the running ones finish", 3 * m, all[:3], 0, 1, state.ChildReportRequested},
		{"one retried and recovered: the set only shrank", 4 * m, all[1:3], 1, 1, state.ChildReportRequested},
		{"it fails again at once: the same failure", 5 * m, all[:3], 0, 1, state.ChildReportRequested},
		{"a fourth fails", 6 * m, all[:4], 0, 2, state.ChildReportRequested},
		{"a fifth fails", 7 * m, all[:5], 0, 3, state.ChildReportRequested},
		{"a sixth fails: the episode's cap is reached", 8 * m, all[:6], 0, 3, state.ChildReportCapped},
		{"a seventh fails: still capped", 9 * m, all[:7], 0, 3, state.ChildReportCapped},
		{"unchanged while capped", 10 * m, all[:7], 0, 3, state.ChildReportCapped},
		{"every child recovers: the episode is not over yet", 11 * m, nil, 0, 3, state.ChildReportCapped},
		{"one fails again inside the hour: still the same episode", 30 * m, all[:1], 0, 3, state.ChildReportCapped},
		{"and recovers", 31 * m, nil, 0, 3, state.ChildReportCapped},
		{"an hour after the others recovered, the last is still remembered", 80 * m, nil, 0, 3, state.ChildReportCapped},
		{"an hour after the last recovered: the episode ends", 91 * m, nil, 0, 3, ""},
		{"still none", 92 * m, nil, 0, 3, ""},
		{"two fail again: a new episode", 93 * m, all[:2], 1, 4, state.ChildReportRequested},
	}
	for _, step := range steps {
		p.obs.ErroredChildren, p.obs.ActiveChildren = step.errored, step.running
		res := runTick(t, w, t0.Add(step.at))
		got := childReports(w)
		rec := savedChildReport(t, w)
		if len(got) != step.wantReports || (rec == nil) != (step.wantOutcome == "") || (rec != nil && rec.Outcome != step.wantOutcome) {
			t.Fatalf("%s: %d reports, record %+v, check %+v", step.name, len(got), rec, res)
		}
		if ctl, _ := control.Load(w.dir); ctl.AwaitingUser || ctl.Paused {
			t.Fatalf("%s: the loop was held", step.name)
		}
	}
	reports := childReports(w)
	first := reports[0]
	if first.Loop != "example-loop" || first.Data["erroredChildren"] != 3 || first.Data["runningChildren"] != 2 || first.Data["listingComplete"] != true || first.Data["report"] != 1 || len(first.Data) != 4 {
		t.Fatalf("payload: %+v", first.Data)
	}
	if !strings.Contains(first.Text, "3 direct child thread(s) of the supervised thread are in error and 2 are still running") || !strings.Contains(first.Text, "The child listing was complete.") {
		t.Fatalf("text: %s", first.Text)
	}
	if reports[1].Data["report"] != 2 || reports[2].Data["report"] != 3 || reports[2].Data["erroredChildren"] != 5 || reports[3].Data["report"] != 1 {
		t.Fatalf("report numbers: %v %v %v", reports[1].Data, reports[2].Data, reports[3].Data)
	}
	// No child ID or prompt reaches a report or the saved record.
	raw, _ := os.ReadFile(filepath.Join(w.dir, state.FileName))
	payload, _ := json.Marshal(reports)
	if strings.Contains(string(raw), "thr_child") || strings.Contains(string(payload), "thr_child") || strings.Contains(string(payload), "keep going") {
		t.Fatalf("child ID or prompt leaked: %s\n%s", raw, payload)
	}
}

// The report is sent when the failure is first seen, whatever the parent is
// doing: it does not wait for the parent to be woken, and it changes nothing
// about the check.
func TestChildErrorReportDoesNotDependOnTheParentOrChangeTheCheck(t *testing.T) {
	errored := children("thr_child_a", "thr_child_b")
	for name, obs := range map[string]decide.Observation{
		"parent idle":                  {State: decide.Idle},
		"parent active":                {State: decide.Active},
		"parent errored":               {State: decide.Error, ErrorClass: decide.ErrUnknown},
		"parent waiting on a person":   {State: decide.Idle, AwaitingInput: true},
		"parent with queued messages":  {State: decide.Idle, Queued: 1},
		"another child still running":  {State: decide.Idle, ActiveChildren: 1},
		"parent idle, provider retry":  {State: decide.Error, ErrorClass: decide.ErrOverloaded, Retrying: true},
		"parent pending":               {State: decide.Pending},
		"parent in an unknown state":   {State: decide.Unknown},
		"parent idle, background jobs": {State: decide.Idle, Background: 1},
	} {
		t.Run(name, func(t *testing.T) {
			// The same checks without any child in error, for comparison.
			plainWorld, plain := childReportWorld(t)
			plain.obs = obs
			w, p := childReportWorld(t)
			obs.ErroredChildren = errored
			p.obs = obs
			for i := 0; i < 3; i++ {
				at := t0.Add(time.Duration(i) * time.Minute)
				want, got := runTick(t, plainWorld, at), runTick(t, w, at)
				// The parent notice may add a wake while another child runs; that
				// is decided by the engine, not by the report.
				if name != "another child still running" && (got.Action != want.Action || got.Reason != want.Reason) {
					t.Fatalf("check %d changed: %+v, without errored children %+v", i, got, want)
				}
				if len(got.Warnings) != 0 {
					t.Fatalf("warnings: %v", got.Warnings)
				}
			}
			if got := childReports(w); len(got) != 1 || got[0].Data["erroredChildren"] != 2 {
				t.Fatalf("%d reports: %+v", len(got), got)
			}
		})
	}
}

// Holds win: a loop that does not observe its target neither reports nor
// forgets. Stop observes the target only to stop it, and reports nothing.
func TestChildErrorReportRespectsHoldsStopAndDryRun(t *testing.T) {
	errored := children("thr_child_a", "thr_child_b")
	for _, hold := range []string{"paused", "disabled", "completed", "awaiting user", "stop", "dry run"} {
		t.Run(hold, func(t *testing.T) {
			w, p := childReportWorld(t)
			p.obs.ErroredChildren = errored
			control.Update(w.dir, func(f *control.File) {
				f.Paused, f.Completed, f.AwaitingUser, f.Stop = hold == "paused", hold == "completed", hold == "awaiting user", hold == "stop"
				f.Enabled = hold != "disabled"
			})
			if hold == "stop" {
				p.obs.State = decide.Active
			}
			for i := 0; i < 3; i++ {
				opts := w.opts(t0.Add(time.Duration(i) * time.Minute))
				opts.DryRun = hold == "dry run"
				if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
					t.Fatal(err)
				}
			}
			if len(childReports(w)) != 0 || savedChildReport(t, w) != nil {
				t.Fatalf("%s: %d reports, record %+v", hold, len(childReports(w)), savedChildReport(t, w))
			}
			if hold == "stop" && p.stopped != 1 {
				t.Fatal("stop lost")
			}
		})
	}
	// A hold after the report keeps the record; resuming does not report again.
	w, p := childReportWorld(t)
	p.obs.ErroredChildren = errored
	runTick(t, w, t0)
	control.Update(w.dir, func(f *control.File) { f.Paused = true })
	for i := 1; i < 4; i++ {
		runTick(t, w, t0.Add(time.Duration(i)*time.Hour))
	}
	if rec := savedChildReport(t, w); rec == nil || rec.Outcome != state.ChildReportRequested {
		t.Fatalf("hold forgot the record: %+v", rec)
	}
	control.Update(w.dir, func(f *control.File) { f.Paused = false })
	runTick(t, w, t0.Add(5*time.Hour))
	if got := childReports(w); len(got) != 1 {
		t.Fatalf("%d reports after resume", len(got))
	}
}

// An incomplete child listing is reported as such, never forgets a child, and
// a later complete listing of the same children says nothing new.
func TestChildErrorReportWithAPartialListing(t *testing.T) {
	w, p := childReportWorld(t)
	p.obs.ErroredChildren, p.obs.ChildrenUnverified, p.obs.ActiveChildren = children("thr_child_a", "thr_child_b"), true, 1
	runTick(t, w, t0)
	got := childReports(w)
	if len(got) != 1 || got[0].Data["listingComplete"] != false || got[0].Data["erroredChildren"] != 2 || !strings.Contains(got[0].Text, "listing was incomplete, so both counts are lower bounds") {
		t.Fatalf("%+v", got)
	}
	if rec := savedChildReport(t, w); rec.Complete || len(rec.Children) != 2 {
		t.Fatalf("%+v", rec)
	}
	// A partial listing that misses both children, then none at all: nothing is forgotten.
	p.obs.ErroredChildren = nil
	runTick(t, w, t0.Add(time.Minute))
	p.obs.ErroredChildren = children("thr_child_b")
	runTick(t, w, t0.Add(2*time.Minute))
	// Hours of partial listings that do not show a child prove nothing about it.
	runTick(t, w, t0.Add(5*time.Hour))
	if rec := savedChildReport(t, w); rec == nil || len(rec.Children) != 2 {
		t.Fatalf("a partial listing forgot a child: %+v", rec)
	}
	// The complete listing shows the same two: no second report.
	p.obs.ErroredChildren, p.obs.ChildrenUnverified = children("thr_child_a", "thr_child_b"), false
	runTick(t, w, t0.Add(5*time.Hour+3*time.Minute))
	// A third child, seen through another partial listing, is reported.
	p.obs.ErroredChildren, p.obs.ChildrenUnverified = children("thr_child_c"), true
	runTick(t, w, t0.Add(5*time.Hour+4*time.Minute))
	got = childReports(w)
	if len(got) != 2 || got[1].Data["listingComplete"] != false || got[1].Data["report"] != 2 {
		t.Fatalf("%+v", got)
	}
}

// Without a subscription nothing is sent, the episode is still recorded, and
// subscribing later sends its report once.
func TestChildErrorReportSubscription(t *testing.T) {
	w, p := childReportWorld(t)
	w.cfg.Loop.Report = &config.Report{Events: []string{"await_user", "handoff"}}
	p.obs.ErroredChildren = children("thr_child_a", "thr_child_b")
	for i := 0; i < 3; i++ {
		runTick(t, w, t0.Add(time.Duration(i)*time.Minute))
	}
	rec := savedChildReport(t, w)
	if len(childReports(w)) != 0 || rec == nil || rec.Outcome != state.ChildReportNotSubscribed || rec.Reports != 0 || len(rec.Children) != 0 || !rec.DetectedAt.Equal(t0) {
		t.Fatalf("%d reports, record %+v", len(childReports(w)), rec)
	}
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	for i := 3; i < 6; i++ {
		runTick(t, w, t0.Add(time.Duration(i)*time.Minute))
	}
	if got := childReports(w); len(got) != 1 || savedChildReport(t, w).Outcome != state.ChildReportRequested {
		t.Fatalf("%d reports after subscribing, record %+v", len(got), savedChildReport(t, w))
	}
	// No report section at all: nothing sent, nothing breaks.
	w, p = childReportWorld(t)
	w.cfg.Loop.Report = nil
	p.obs.ErroredChildren = children("thr_child_a")
	if res := runTick(t, w, t0); len(childReports(w)) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("%+v", res)
	}
}

type childReportFailer struct {
	err   error
	calls int
}

func (c *childReportFailer) Report(_ context.Context, e report.Event) error {
	if e.Type == "child_error" {
		c.calls++
		return c.err
	}
	return nil
}

// A failed delivery only warns. It is recorded and never retried, and the
// check's action and result are unchanged.
func TestChildErrorReportFailureOnlyWarns(t *testing.T) {
	w, p := childReportWorld(t)
	p.obs.ErroredChildren = children("thr_child_a", "thr_child_b")
	failing := &childReportFailer{err: errors.New("report: request failed")}
	for i := 0; i < 5; i++ {
		opts := w.opts(t0.Add(time.Duration(i) * time.Minute))
		opts.Reporter = failing
		res, err := Tick(context.Background(), w.cfg, opts)
		if err != nil || res.Action != "wake" || res.Reason != "idle" {
			t.Fatalf("a failed report failed or changed the check: %+v %v", res, err)
		}
		if (i == 0) != strings.Contains(strings.Join(res.Warnings, ";"), "report child_error") {
			t.Fatalf("check %d warnings: %v", i, res.Warnings)
		}
	}
	if rec := savedChildReport(t, w); failing.calls != 1 || rec.Outcome != state.ChildReportFailed || rec.Reports != 1 || len(p.sent) != 5 {
		t.Fatalf("%d attempts, %+v, sends %d", failing.calls, rec, len(p.sent))
	}
}

// When the episode cannot be saved first, nothing is sent, so an unwritable
// state directory cannot turn into a report on every check.
func TestChildErrorReportIsNotSentWhenItCannotBeRecorded(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory that can be made unwritable")
	}
	w, p := childReportWorld(t)
	p.obs.ErroredChildren = children("thr_child_a")
	if err := os.Chmod(w.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(w.dir, 0o700)
	for i := 0; i < 3; i++ {
		res, _ := Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*time.Minute)))
		if !strings.Contains(strings.Join(res.Warnings, ";"), "report child_error: not sent because the episode could not be recorded") {
			t.Fatalf("check %d warnings: %v", i, res.Warnings)
		}
	}
	if len(childReports(w)) != 0 {
		t.Fatalf("%d reports without a saved record", len(childReports(w)))
	}
}

// A handoff moves the loop to another thread in the same check: the children
// seen belong to the thread just left, so nothing is reported or kept.
func TestChildErrorReportIsNotSentForTheThreadJustLeft(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	obs := limited
	obs.ErroredChildren = children("thr_child_a", "thr_child_b")
	w.primary(obs)
	if res := runTick(t, w, t0); res.Action != "handoff" {
		t.Fatalf("%+v", res)
	}
	if len(childReports(w)) != 0 || savedChildReport(t, w) != nil {
		t.Fatalf("%d reports, record %+v", len(childReports(w)), savedChildReport(t, w))
	}
}

// The real webhook transport and delivery ledger, against a local server: one
// request per report whether it is accepted or rejected, the ledger shows the
// single attempt under the child_error event, and the body holds counts only.
func TestChildErrorReportThroughTheRecordedWebhook(t *testing.T) {
	for _, status := range []int{202, 503} {
		var calls atomic.Int32
		var body atomic.Value
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			b, _ := io.ReadAll(r.Body)
			body.Store(string(b))
			rw.WriteHeader(status)
		}))
		t.Setenv("KG_CHILD_URL", srv.URL)
		t.Setenv("KG_CHILD_SECRET", "fictional-child-error-key")
		w, p := childReportWorld(t)
		w.cfg.Loop.Prompt = "PRIVATE_TASK_TEXT"
		// The default event list: child_error is in it.
		w.cfg.Loop.Report = &config.Report{URLEnv: "KG_CHILD_URL", SecretEnv: "KG_CHILD_SECRET", Scheme: "bearer", Format: "standard", Events: config.DefaultReportEvents, Timeout: config.Duration{Duration: 2 * time.Second}}
		p.obs.ErroredChildren, p.obs.ActiveChildren = children("thr_child_a", "thr_child_b"), 1
		sender, err := report.NewRecorded(*w.cfg.Loop.Report, w.dir)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 4; i++ {
			opts := w.opts(t0.Add(time.Duration(i) * 10 * time.Minute))
			opts.Reporter = sender
			if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
				t.Fatalf("%d: %v", status, err)
			}
		}
		srv.Close()
		sent, _ := body.Load().(string)
		if calls.Load() != 1 || !strings.Contains(sent, "child_error") || !strings.Contains(sent, `"erroredChildren":2`) || !strings.Contains(sent, `"runningChildren":1`) ||
			strings.Contains(sent, "PRIVATE_TASK_TEXT") || strings.Contains(sent, "thr_child") || strings.Contains(sent, "thr_primary") {
			t.Fatalf("%d: %d requests, body %s", status, calls.Load(), sent)
		}
		h, err := report.LoadDeliveries(w.dir)
		wantOutcome, wantSaved := "http_accepted", state.ChildReportRequested
		if status == 503 {
			wantOutcome, wantSaved = "http_rejected", state.ChildReportFailed
		}
		if err != nil || len(h.Records) != 1 || h.Records[0].Event != "child_error" || h.Records[0].Outcome != wantOutcome || len(h.Records[0].Attempts) != 1 || h.Records[0].Attempts[0].HTTPStatus != status {
			t.Fatalf("%d: %+v %v", status, h.Records, err)
		}
		if rec := savedChildReport(t, w); rec == nil || rec.Outcome != wantSaved {
			t.Fatalf("%d: %+v", status, rec)
		}
	}
}

// A child that flaps between error and a provider retry is one failure. It is
// not reported on each flap, does not spend the episode's reports, and does
// not stop other children's failures from being reported.
func TestFlappingChildIsNotReportedOnEveryFlap(t *testing.T) {
	t.Run("only child", func(t *testing.T) {
		w, p := childReportWorld(t)
		for i := 0; i < 20; i++ {
			p.obs = decide.Observation{State: decide.Idle, ActiveChildren: 1, HealthyChildren: children("thr_child_a")}
			if i%2 == 0 {
				p.obs = decide.Observation{State: decide.Idle, ErroredChildren: children("thr_child_a")}
			}
			runTick(t, w, t0.Add(time.Duration(i)*time.Minute))
		}
		if got := childReports(w); len(got) != 1 {
			t.Fatalf("%d reports for one flapping child over 20 checks", len(got))
		}
		// Flapping slower than the floor is a new failure each time, at most one
		// report per min_wake_interval.
		w, p = childReportWorld(t)
		for i := 0; i < 8; i++ {
			at := t0.Add(time.Duration(i) * 2 * time.Hour)
			p.obs = decide.Observation{State: decide.Idle, ErroredChildren: children("thr_child_a")}
			runTick(t, w, at)
			p.obs = decide.Observation{State: decide.Idle}
			runTick(t, w, at.Add(time.Minute))
			runTick(t, w, at.Add(62*time.Minute))
		}
		if got := childReports(w); len(got) != 8 {
			t.Fatalf("%d reports for eight failures two hours apart", len(got))
		}
	})
	t.Run("beside a sibling in error", func(t *testing.T) {
		w, p := childReportWorld(t)
		for i := 0; i < 8; i++ {
			errored := children("thr_child_s")
			if i%2 == 0 {
				errored = children("thr_child_s", "thr_child_a")
			}
			p.obs = decide.Observation{State: decide.Idle, ErroredChildren: errored}
			runTick(t, w, t0.Add(time.Duration(i)*time.Minute))
		}
		if got := childReports(w); len(got) != 1 {
			t.Fatalf("%d reports while one child flapped", len(got))
		}
		// The episode's remaining reports are still there for real new failures.
		p.obs.ErroredChildren = children("thr_child_s", "thr_child_x", "thr_child_y")
		runTick(t, w, t0.Add(9*time.Minute))
		if got := childReports(w); len(got) != 2 || savedChildReport(t, w).Outcome != state.ChildReportRequested || got[1].Data["report"] != 2 {
			t.Fatalf("%d reports, record %+v", len(got), savedChildReport(t, w))
		}
	})
}

// A listing that never completes (a child with a status this build does not
// know) must not freeze the episode: children that are verifiably out of
// error still age out, the record stays bounded, and once they are forgotten a
// later failure starts a new episode.
func TestChildErrorReportWithAListingThatNeverCompletes(t *testing.T) {
	w, p := childReportWorld(t)
	id := func(i int) string { return "thr_child_" + string(rune('a'+i)) }
	// Three children fail one per hour; each earlier one is seen healthy again.
	for i := 0; i < 3; i++ {
		var healthy []string
		for j := 0; j < i; j++ {
			healthy = append(healthy, id(j))
		}
		p.obs = decide.Observation{State: decide.Idle, ChildrenUnverified: true, ErroredChildren: children(id(i)), HealthyChildren: healthy}
		runTick(t, w, t0.Add(time.Duration(i)*time.Hour))
	}
	rec := savedChildReport(t, w)
	if got := childReports(w); len(got) != 3 || rec == nil || len(rec.Children) > 2 {
		t.Fatalf("%d reports, record %+v", len(got), rec)
	}
	// All three are healthy for more than an hour, still through partial
	// listings: the episode ends without ever seeing a complete one.
	p.obs = decide.Observation{State: decide.Idle, ChildrenUnverified: true, HealthyChildren: children(id(0), id(1), id(2))}
	runTick(t, w, t0.Add(3*time.Hour))
	runTick(t, w, t0.Add(4*time.Hour+time.Minute))
	if rec := savedChildReport(t, w); rec != nil {
		t.Fatalf("episode frozen by partial listings: %+v", rec)
	}
	// A later failure is a new episode, not a capped one.
	p.obs = decide.Observation{State: decide.Idle, ChildrenUnverified: true, ErroredChildren: children(id(5))}
	runTick(t, w, t0.Add(5*time.Hour))
	if got := childReports(w); len(got) != 4 || got[3].Data["report"] != 1 || got[3].Data["listingComplete"] != false {
		t.Fatalf("%d reports: %+v", len(got), got[len(got)-1].Data)
	}
	// A listing too large to inspect yields no child at all: nothing is
	// reported and nothing recorded is lost.
	p.obs = decide.Observation{State: decide.Idle, ChildrenUnverified: true}
	runTick(t, w, t0.Add(6*time.Hour))
	if got := childReports(w); len(got) != 4 || savedChildReport(t, w) == nil {
		t.Fatalf("%d reports, record %+v", len(got), savedChildReport(t, w))
	}
}

// An archived or deleted parent is not scanned for children. That is no
// evidence of recovery: the episode is kept, and a restored parent with the
// same children in error is not reported again.
func TestGoneParentDoesNotEndTheEpisode(t *testing.T) {
	w, p := childReportWorld(t)
	p.obs = decide.Observation{State: decide.Idle, ErroredChildren: children("thr_child_a", "thr_child_b")}
	runTick(t, w, t0)
	p.obs = decide.Observation{State: decide.Unknown, Gone: true}
	runTick(t, w, t0.Add(2*time.Hour))
	if rec := savedChildReport(t, w); rec == nil || len(rec.Children) != 2 || rec.Reports != 1 {
		t.Fatalf("a gone parent changed the episode: %+v", rec)
	}
	// The hold for a missing target is cleared and the thread is back.
	control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false })
	p.obs = decide.Observation{State: decide.Idle, ErroredChildren: children("thr_child_a", "thr_child_b")}
	runTick(t, w, t0.Add(3*time.Hour))
	if got := childReports(w); len(got) != 1 {
		t.Fatalf("%d reports after the parent came back", len(got))
	}
}

// A failure first seen while the loop is held is reported by the first check
// that observes the target after the hold is cleared.
func TestFailureDuringAHoldIsReportedAfterResume(t *testing.T) {
	w, p := childReportWorld(t)
	control.Update(w.dir, func(f *control.File) { f.Paused = true })
	p.obs.ErroredChildren = children("thr_child_a")
	for i := 0; i < 3; i++ {
		runTick(t, w, t0.Add(time.Duration(i)*time.Hour))
	}
	if len(childReports(w)) != 0 {
		t.Fatal("reported while paused")
	}
	control.Update(w.dir, func(f *control.File) { f.Paused = false })
	runTick(t, w, t0.Add(4*time.Hour))
	if got := childReports(w); len(got) != 1 {
		t.Fatalf("%d reports after resume", len(got))
	}
}
