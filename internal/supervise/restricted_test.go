package supervise

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func declinedWorld(t *testing.T, class decide.ErrorClass) (*world, *fakeAdapter) {
	t.Helper()
	w := newWorld(t, true, true)
	w.cfg.Loop.Errors.FailoverOn = []string{"rate_limit", "auth", "overloaded", "network"}
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: class})
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"same-account", "claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	return w, p
}

func resume(t *testing.T, w *world) {
	t.Helper()
	// As keepgoing resume does: the hold, its note and the open question end.
	if _, err := control.Update(w.dir, func(f *control.File) { f.AwaitingUser, f.Note, f.Question = false, "", nil }); err != nil {
		t.Fatal(err)
	}
}

func count(reports, kind string) int { return strings.Count(","+reports+",", ","+kind+",") }

func touched(t *testing.T, w *world) {
	t.Helper()
	for id, a := range w.adapters {
		if id != "primary" && (len(a.sent) != 0 || a.activated != 0) {
			t.Fatalf("candidate %s was touched: sent=%v activated=%d", id, a.sent, a.activated)
		}
	}
}

// A provider that restricted the request is not out of quota. With healthy
// fallbacks and every class the configuration accepts listed in failover_on,
// the loop holds for a person: nothing is sent, no candidate is probed,
// activated or briefed, the target does not change, and a question of kind
// restricted is raised, recorded by ID.
func TestRestrictedSessionHoldsAndRaisesAQuestion(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRestricted)
	res := runTick(t, w, t0)
	if res.Action != "await_user" || res.Reason != decide.RestrictedReason || res.Handoff != nil || res.Target != "primary" || len(p.sent) != 0 {
		t.Fatalf("%+v sent=%v", res, p.sent)
	}
	touched(t, w)
	for _, n := range res.Probes {
		if n.Tier == probe.Exact {
			t.Fatalf("an exact probe ran: %+v", n)
		}
	}
	c, err := control.Load(w.dir)
	if err != nil || !c.AwaitingUser || c.Question == nil {
		t.Fatalf("%+v %v", c, err)
	}
	q := c.Question
	if q.Version != 1 || q.Kind != "restricted" || q.Holds != "loop" || !control.QuestionIDPattern.MatchString(q.ID) || len(q.Basis) != 64 || strings.Join(q.Answerers, ",") != "person" {
		t.Fatalf("question %+v", q)
	}
	var opts []string
	for _, o := range q.Options {
		opts = append(opts, o.ID)
	}
	if strings.Join(opts, ",") != "resume,pause,stop" {
		t.Fatalf("options %v", opts)
	}
	if !questionRecorded(t, w, "question_raised", q.ID) {
		t.Fatal("no question_raised record")
	}
	if count(w.reportTypes(), "await_user") != 1 || strings.Contains(w.reportTypes(), "handoff") {
		t.Fatalf("reports = %s", w.reportTypes())
	}
	// Held: later checks send and report nothing.
	if res := runTick(t, w, t0.Add(time.Hour)); res.Action != "none" || len(p.sent) != 0 || count(w.reportTypes(), "await_user") != 1 {
		t.Fatalf("after the hold: %+v sent=%v reports=%s", res, p.sent, w.reportTypes())
	}
}

func questionRecorded(t *testing.T, w *world, op, id string) bool {
	t.Helper()
	j, err := incident.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range j.Records {
		if r.Kind == "control" && r.ControlChange != nil && r.ControlChange.Operation == op && r.ControlChange.QuestionID == id {
			return true
		}
	}
	return false
}

// A refused turn is continued in the same session with the plain prompt,
// spaced 1m, 2m, 5m, then 10m, with no per-episode cap and without spending
// the re-prompt allowance. It never touches a fallback. Each refused turn is
// counted once, and a turn that ends without a refusal after a continue is
// counted as recovered.
func TestRefusedTurnIsContinuedWithGrowingSpacing(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRefused)
	off := 0
	w.cfg.Loop.Refusal.AskAfter = &off // never ask, to see the spacing go on
	at := t0
	sends := 0
	for i, gap := range []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 10 * time.Minute} {
		// The refused turn is seen; the continue waits for its spacing.
		if res := runTick(t, w, at); res.Action != "none" || res.Reason != decide.RefusalWaitReason {
			t.Fatalf("refusal %d, first check: %+v", i+1, res)
		}
		if res := runTick(t, w, at.Add(gap-time.Second)); res.Action != "none" {
			t.Fatalf("refusal %d, before %s: %+v", i+1, gap, res)
		}
		res := runTick(t, w, at.Add(gap))
		if res.Action != "reprompt" || res.Reason != decide.RefusedContinueReason || res.Target != "primary" {
			t.Fatalf("refusal %d, at %s: %+v", i+1, gap, res)
		}
		sends++
		if len(p.sent) != sends {
			t.Fatalf("refusal %d: sent %d", i+1, len(p.sent))
		}
		st, _ := state.Load(w.dir)
		if st.Refusals.Streak != i+1 || st.Refusals.Total != i+1 || st.Reprompts != 0 {
			t.Fatalf("refusal %d: gauge %+v reprompts %d", i+1, st.Refusals, st.Reprompts)
		}
		at = at.Add(gap + time.Minute) // the next turn is refused too
	}
	touched(t, w)
	if c, _ := control.Load(w.dir); c.AwaitingUser || c.Question != nil {
		t.Fatalf("a refusal with asking off never holds: %+v", c)
	}
	// The next turn ends without a refusal: recovered, and the streak ends.
	p.obs = decide.Observation{State: decide.Idle}
	runTick(t, w, at)
	st, _ := state.Load(w.dir)
	g := st.Refusals
	if g.Streak != 0 || g.LongestStreak != 5 || g.Total != 5 || g.Recovered != 1 {
		t.Fatalf("after recovery: %+v", g)
	}
	if day, rec := g.InLastDay(at); day != 5 || rec != 1 {
		t.Fatalf("last day %d recovered %d", day, rec)
	}
	j, _ := incident.Load(w.dir)
	marks := map[string]int{}
	for _, r := range j.Records {
		if r.Refusal != "" {
			marks[r.Refusal]++
		}
	}
	if marks["refused"] != 5 || marks["recovered"] != 1 {
		t.Fatalf("journal marks %v", marks)
	}
}

// The streak report goes out at 3 and 10 and every 10 after; it never holds.
// At refusal.ask_after (default 10) the loop stops continuing and raises a
// question of kind refused, held only until a person answers; a resume
// answers it, and the streak starts over.
func TestRefusalStreakReportsAndTheQuestion(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRefused)
	w.cfg.Loop.Report = &config.Report{Events: config.DefaultReportEvents}
	at := t0
	for i := 1; i <= 9; i++ {
		runTick(t, w, at)
		res := runTick(t, w, at.Add(10*time.Minute))
		if res.Action != "reprompt" {
			t.Fatalf("refusal %d: %+v", i, res)
		}
		at = at.Add(11 * time.Minute)
	}
	if n := count(w.reportTypes(), "refusal_streak"); n != 1 {
		t.Fatalf("streak reports before 10: %d (%s)", n, w.reportTypes())
	}
	for _, e := range w.reports {
		if e.Type == "refusal_streak" && (!strings.Contains(e.Text, "False flags are common") || !strings.Contains(e.Text, "keepgoing pause or keepgoing stop") || e.Data["streak"] != 3) {
			t.Fatalf("%+v", e)
		}
	}
	// The tenth refusal reaches ask_after: no continue, a question instead.
	res := runTick(t, w, at)
	if res.Action != "await_user" || res.Reason != decide.RefusalAskReason || len(p.sent) != 9 {
		t.Fatalf("at ask_after: %+v sent=%d", res, len(p.sent))
	}
	// The check that asks sends one message, which says it has stopped and
	// is asking; never a streak report that says it keeps continuing.
	if count(w.reportTypes(), "refusal_streak") != 1 {
		t.Fatalf("a streak report on the asking check: %s", w.reportTypes())
	}
	asked := w.reports[len(w.reports)-1]
	if asked.Type != "await_user" || !strings.Contains(asked.Text, "has stopped sending continues and is asking") || strings.Contains(asked.Text, "keeps sending") || asked.Data["streak"] != 10 {
		t.Fatalf("%+v", asked)
	}
	c, _ := control.Load(w.dir)
	q := c.Question
	if q == nil || q.Kind != "refused" || q.Holds != "loop" || strings.Join(q.Answerers, ",") != "person" {
		t.Fatalf("question %+v", q)
	}
	var opts []string
	ev := map[string]string{}
	for _, o := range q.Options {
		opts = append(opts, o.ID)
		if o.ID == "continue" && !o.Primary {
			t.Fatal("continue is the primary answer")
		}
	}
	for _, e := range q.Evidence {
		ev[e.Key] = e.Type + ":" + e.Value
	}
	if strings.Join(opts, ",") != "continue,fresh_session,pause" || ev["current_streak"] != "count:10" || ev["declined_turns"] != "count:10" || ev["error_class"] != "class:refused" || ev["route"] != "route:primary" || ev["recovered_by_continue_24h"] != "count:0" {
		t.Fatalf("options %v evidence %v", opts, ev)
	}
	touched(t, w)
	if runTick(t, w, at.Add(time.Hour)); len(p.sent) != 9 {
		t.Fatal("sent while the question was open")
	}
	// A resume answers it: the question is gone, and the streak starts over,
	// so the continue comes after 1 minute again.
	resume(t, w)
	res = runTick(t, w, at.Add(2*time.Hour))
	if res.Action != "none" || res.Reason != decide.RefusalWaitReason {
		t.Fatalf("after the answer: %+v", res)
	}
	if st, _ := state.Load(w.dir); st.Refusals.Streak != 1 || st.Refusals.Asked {
		t.Fatalf("streak after the answer: %+v", st.Refusals)
	}
	if res := runTick(t, w, at.Add(2*time.Hour+time.Minute)); res.Action != "reprompt" || len(p.sent) != 10 {
		t.Fatalf("continue after the answer: %+v", res)
	}
}

// A question that is still recorded when a check runs (its hold ended some
// other way) is withdrawn, and the withdrawal is recorded by ID.
func TestAnOpenQuestionIsWithdrawnWhenTheLoopRunsAgain(t *testing.T) {
	w, _ := declinedWorld(t, decide.ErrRestricted)
	runTick(t, w, t0)
	c, _ := control.Load(w.dir)
	id := c.Question.ID
	if _, err := control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false }); err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0.Add(time.Hour))
	if c, _ := control.Load(w.dir); c.Question != nil && c.Question.ID == id {
		t.Fatal("not withdrawn")
	}
	if !questionRecorded(t, w, "question_withdrawn", id) {
		t.Fatal("no question_withdrawn record")
	}
}

// The way out. A person who resumes the held loop asks for one more try: the
// next check re-prompts once, on the same target, outside the re-prompt cap.
// If the turn is declined again the loop holds again, with one report for
// that new refusal. keepgoing never tries twice for one resume, and never
// moves.
func TestResumeAfterARestrictedTurnSendsExactlyOneReprompt(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRestricted)
	w.cfg.Loop.Errors.MaxReprompts = 1
	runTick(t, w, t0)
	for i := 1; i <= 3; i++ {
		at := t0.Add(time.Duration(i) * time.Hour)
		resume(t, w)
		res := runTick(t, w, at)
		if res.Action != "reprompt" || res.Reason != decide.DeclineAcknowledgedReason || res.Target != "primary" || res.Handoff != nil {
			t.Fatalf("resume %d: %+v", i, res)
		}
		if len(p.sent) != i {
			t.Fatalf("resume %d: sent %d prompts", i, len(p.sent))
		}
		if st, _ := state.Load(w.dir); st.DeclineHeldTarget != "" {
			t.Fatalf("resume %d: the acknowledgement was not used up: %+v", i, st)
		}
		// The new turn is declined too: one hold and one report, nothing sent.
		res = runTick(t, w, at.Add(time.Minute))
		if res.Action != "await_user" || res.Reason != decide.RestrictedReason || len(p.sent) != i {
			t.Fatalf("declined again after resume %d: %+v sent=%d", i, res, len(p.sent))
		}
		if got := count(w.reportTypes(), "await_user"); got != i+1 {
			t.Fatalf("resume %d: %d await_user reports, want %d (%s)", i, got, i+1, w.reportTypes())
		}
	}
	for id, a := range w.adapters {
		if id != "primary" && (len(a.sent) != 0 || a.activated != 0) {
			t.Fatalf("candidate %s was touched: sent=%v activated=%d", id, a.sent, a.activated)
		}
	}
}

// After the one try, a turn that goes well continues the loop; and a person
// who fixes things by hand, so that the target no longer shows a declined
// turn, leaves no acknowledgement behind for a later refusal.
func TestDeclineAcknowledgementDoesNotOutliveTheTurnItWasFor(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRestricted)
	runTick(t, w, t0)
	resume(t, w)
	if res := runTick(t, w, t0.Add(time.Hour)); res.Action != "reprompt" {
		t.Fatalf("%+v", res)
	}
	p.obs = decide.Observation{State: decide.Idle}
	if res := runTick(t, w, t0.Add(3*time.Hour)); res.Action != "wake" {
		t.Fatalf("recovered session: %+v", res)
	}

	w, p = declinedWorld(t, decide.ErrRestricted)
	runTick(t, w, t0)
	resume(t, w)
	p.obs = decide.Observation{State: decide.Active} // the person started a turn by hand
	if res := runTick(t, w, t0.Add(time.Hour)); res.Action != "none" || len(p.sent) != 0 {
		t.Fatalf("turn started by hand: %+v sent=%v", res, p.sent)
	}
	if st, _ := state.Load(w.dir); st.DeclineHeldTarget != "" {
		t.Fatalf("a stale acknowledgement was kept: %+v", st)
	}
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRestricted}
	if res := runTick(t, w, t0.Add(2*time.Hour)); res.Action != "await_user" || len(p.sent) != 0 {
		t.Fatalf("a later restriction must hold, not be prompted: %+v sent=%v", res, p.sent)
	}
}

// A dry run changes nothing, so it leaves no acknowledgement either.
func TestDryRunDoesNotArmADeclineAcknowledgement(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRestricted)
	o := w.opts(t0)
	o.DryRun = true
	if _, err := Tick(t.Context(), w.cfg, o); err != nil {
		t.Fatal(err)
	}
	if res := runTick(t, w, t0.Add(time.Minute)); res.Action != "await_user" || len(p.sent) != 0 {
		t.Fatalf("after a dry run: %+v sent=%v", res, p.sent)
	}
}

// A primary whose switch-back probe the provider refuses or restricts is not
// asked again every retry_every: switch-back stops until a person acts.
func TestSwitchBackStopsWhenThePrimarysProbeIsDeclined(t *testing.T) {
	w, _, _ := switchBackWorld(t)
	w.probeClass = map[string]string{"primary": "restricted"}
	after := t0.Add(2*time.Hour + time.Minute)
	res := runTick(t, w, after)
	if res.Target != "claude-other" || !strings.Contains(res.Reason, "primary failed its probe") {
		t.Fatalf("declined probe: %+v", res)
	}
	for i := 1; i <= 6; i++ {
		runTick(t, w, after.Add(time.Duration(i)*45*time.Minute))
	}
	if n := strings.Count(strings.Join(w.probes, ","), "primary:exact"); n != 1 {
		t.Fatalf("the declined primary was probed %d times", n)
	}
	stopped := 0
	for _, e := range w.reports {
		if e.Type == "switch_back" && strings.Contains(e.Text, "refused or restricted") {
			stopped++
		}
	}
	if stopped != 1 {
		t.Fatalf("want one report that switch-back stopped, got %d (%s)", stopped, w.reportTypes())
	}
}

// An earlier re-prompt that was recorded but could not be confirmed as sent
// rewrites the reason of an ordinary cap hold. It must not rewrite the hold
// for a declined turn, which carries its own reason and arms the one
// re-prompt a resume asks for.
func TestUnconfirmedRepromptsDoNotHideADeclinedTurn(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRestricted)
	st, _ := state.Load(w.dir)
	st.Reprompts, st.UnconfirmedReprompts = w.cfg.Loop.Errors.MaxReprompts, 1
	if err := state.Save(w.dir, st); err != nil {
		t.Fatal(err)
	}
	res := runTick(t, w, t0)
	if res.Action != "await_user" || res.Reason != decide.RestrictedReason {
		t.Fatalf("the restriction was hidden: %+v", res)
	}
	resume(t, w)
	if res := runTick(t, w, t0.Add(time.Hour)); res.Action != "reprompt" || res.Reason != decide.DeclineAcknowledgedReason || len(p.sent) != 1 {
		t.Fatalf("the resume's one re-prompt was blocked: %+v sent=%v", res, p.sent)
	}
}

// The resume's one try belongs to the first check that sees the declined
// turn. If that check does not re-prompt (here a message is queued), the try
// is spent: the queued turn, declined again, holds again.
func TestTheResumesTryIsSpentByACheckThatDoesNotUseIt(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRestricted)
	runTick(t, w, t0)
	resume(t, w)
	p.obs.Queued = 1
	if res := runTick(t, w, t0.Add(time.Hour)); res.Action != "none" || len(p.sent) != 0 {
		t.Fatalf("with a queued message: %+v sent=%v", res, p.sent)
	}
	if st, _ := state.Load(w.dir); st.DeclineHeldTarget != "" {
		t.Fatalf("the try was not spent: %+v", st)
	}
	p.obs.Queued = 0 // the queued turn ran and was restricted too
	if res := runTick(t, w, t0.Add(2*time.Hour)); res.Action != "await_user" || res.Reason != decide.RestrictedReason || len(p.sent) != 0 {
		t.Fatalf("declined again must hold again: %+v sent=%v", res, p.sent)
	}
}

// A resume armed for one target's declined turn does not re-prompt another
// target: after the loop is moved to a fallback whose turn is refused, the
// fallback holds.
func TestTheResumesTryIsBoundToItsTarget(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRestricted)
	runTick(t, w, t0)
	if st, _ := state.Load(w.dir); st.DeclineHeldTarget != "primary" {
		t.Fatalf("not armed for the primary: %+v", st)
	}
	// A person moves the loop to a fallback (as retarget does) and resumes.
	st, _ := state.Load(w.dir)
	st.ActiveTarget = "codex-main"
	if err := state.Save(w.dir, st); err != nil {
		t.Fatal(err)
	}
	fb := &fakeAdapter{name: "codex-main", obs: decide.Observation{State: decide.Error, ErrorClass: decide.ErrRestricted}}
	w.adapters["codex-main"] = fb
	resume(t, w)
	res := runTick(t, w, t0.Add(time.Hour))
	if res.Target != "codex-main" || res.Action != "await_user" || res.Reason != decide.RestrictedReason || len(fb.sent) != 0 || len(p.sent) != 0 {
		t.Fatalf("the primary's arm re-prompted the fallback: %+v fb=%v p=%v", res, fb.sent, p.sent)
	}
}

// A model fallback after a refusal is a timeline note: it is recorded once,
// holds nothing and sends nothing. The first sight of a log sets the baseline.
func TestModelFallbackAfterARefusalIsNotedOnce(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Active, ModelFallback: 4})
	runTick(t, w, t0)
	p.obs.ModelFallback = 9
	res := runTick(t, w, t0.Add(time.Minute))
	runTick(t, w, t0.Add(2*time.Minute))
	if res.Action != "none" || len(p.sent) != 0 {
		t.Fatalf("%+v sent=%v", res, p.sent)
	}
	if c, _ := control.Load(w.dir); c.AwaitingUser || c.Question != nil {
		t.Fatalf("a fallback held the loop: %+v", c)
	}
	j, _ := incident.Load(w.dir)
	n := 0
	for _, r := range j.Records {
		if r.ModelFallback {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("noted %d times", n)
	}
}

// A continue the adapter did not accept does not make the same refused turn
// a new one: four failed sends after one refusal leave the streak at 1. The
// spacing still runs from each attempt, so a failing send is not repeated on
// every check.
func TestAFailedContinueDoesNotCountTheRefusalAgain(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRefused)
	p.sendErr = errors.New("example send failure")
	at := t0
	runTick(t, w, at)
	for i := 1; i <= 4; i++ {
		at = at.Add(10 * time.Minute)
		Tick(context.Background(), w.cfg, w.opts(at)) // the send fails; the check reports it
		if len(p.sent) != i {
			t.Fatalf("attempt %d: sent %d", i, len(p.sent))
		}
		if res := runTick(t, w, at.Add(30*time.Second)); res.Action != "none" || len(p.sent) != i {
			t.Fatalf("attempt %d repeated at once: %+v", i, res)
		}
	}
	st, _ := state.Load(w.dir)
	if st.Refusals.Streak != 1 || st.Refusals.Total != 1 {
		t.Fatalf("one refused turn counted as %+v", st.Refusals)
	}
}

// Where the adapter names the errored turn, the name decides: the same
// refused turn seen again after an accepted continue (the new turn has not
// started yet) is not counted again, and a turn with a new name is.
func TestARefusedTurnIsCountedOnceByItsName(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRefused)
	p.obs.ErrorTurn = "thr_example/bb-turn-4"
	runTick(t, w, t0)
	runTick(t, w, t0.Add(time.Minute)) // continue, accepted
	runTick(t, w, t0.Add(2*time.Minute))
	if st, _ := state.Load(w.dir); st.Refusals.Streak != 1 {
		t.Fatalf("the same turn counted again: %+v", st.Refusals)
	}
	p.obs.ErrorTurn = "thr_example/bb-turn-9"
	runTick(t, w, t0.Add(3*time.Minute))
	if st, _ := state.Load(w.dir); st.Refusals.Streak != 2 || st.Refusals.TurnKey == "" || strings.Contains(st.Refusals.TurnKey, "thr_example") {
		t.Fatalf("a new turn: %+v", st.Refusals)
	}
}

// A check killed after recording a continue and before saving state leaves
// only the send record. The next check counts it as a continue made, in the
// gauge: it spends no re-prompt allowance, starts the spacing, and does not
// count the refusal again.
func TestAContinueRecordedByAKilledCheckIsSettledIntoTheGauge(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRefused)
	runTick(t, w, t0) // the refusal is counted; the continue waits 1 minute
	at := t0.Add(time.Minute)
	unsaved(t, w, at, "refusal_continue", state.Primary)
	// The same refused turn is still showing. Without the record it would be
	// continued again at once.
	if res := runTick(t, w, at.Add(30*time.Second)); res.Action != "none" || res.Reason != decide.RefusalWaitReason || len(p.sent) != 0 {
		t.Fatalf("after the kill: %+v sent=%d", res, len(p.sent))
	}
	st, _ := state.Load(w.dir)
	if st.Reprompts != 0 || st.UnconfirmedReprompts != 0 || !st.Refusals.LastContinueAt.Equal(at) || !st.Refusals.Continued || st.Refusals.Streak != 1 || st.Refusals.Total != 1 {
		t.Fatalf("settled as %+v reprompts=%d unconfirmed=%d", st.Refusals, st.Reprompts, st.UnconfirmedReprompts)
	}
	// The spacing ran from the recorded continue: the next one goes a
	// minute after it, and is then a continue the gauge knows was made.
	if res := runTick(t, w, at.Add(time.Minute)); res.Action != "reprompt" || len(p.sent) != 1 {
		t.Fatalf("the next continue: %+v", res)
	}
	runTick(t, w, at.Add(2*time.Minute))
	if st, _ := state.Load(w.dir); st.Refusals.Streak != 2 {
		t.Fatalf("%+v", st.Refusals)
	}
}

// Refused, then a rate limit on the same route: the declined work is not
// handed to another account. The limit is waited out in place; once a turn
// on that route finishes without a refusal, the ordinary rules return.
func TestARefusalThenALimitNeverMovesTheWork(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRefused)
	runTick(t, w, t0)
	runTick(t, w, t0.Add(time.Minute)) // the continue
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit, ResetsAt: t0.Add(5 * time.Hour)}
	for i, at := range []time.Time{t0.Add(5 * time.Minute), t0.Add(time.Hour), t0.Add(3 * time.Hour)} {
		res := runTick(t, w, at)
		if res.Action != "none" || res.Handoff != nil || res.Target != "primary" || res.Reason != "limit resets in "+t0.Add(5*time.Hour).Sub(at).Round(time.Minute).String()+"; no handoff while a refusal on this route is unrecovered" {
			t.Fatalf("check %d: %+v", i, res)
		}
	}
	touched(t, w)
	if st, _ := state.Load(w.dir); st.Refusals.Streak != 1 || st.Refusals.Recovered != 0 {
		t.Fatalf("a limit ended the streak: %+v", st.Refusals)
	}
	// The limit lifts and a turn finishes: recovered, and the rules are the
	// ordinary ones again.
	p.obs = decide.Observation{State: decide.Idle}
	runTick(t, w, t0.Add(6*time.Hour))
	if st, _ := state.Load(w.dir); st.Refusals.Streak != 0 || st.Refusals.Recovered != 1 {
		t.Fatalf("after a finished turn: %+v", st.Refusals)
	}
}

// A dry run previews what the real check would do, here the first wait, and
// saves nothing.
func TestADryRunPreviewsTheRefusalWait(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRefused)
	o := w.opts(t0)
	o.DryRun = true
	res, err := Tick(context.Background(), w.cfg, o)
	if err != nil || res.Action != "none" || res.Reason != decide.RefusalWaitReason || len(p.sent) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if st, _ := state.Load(w.dir); st.Refusals.Total != 0 {
		t.Fatalf("a dry run saved the gauge: %+v", st.Refusals)
	}
}

// A refusal recorded in the future, after the clock went back, is treated as
// now: the continue waits its spacing from now, and does not stall.
func TestAClockThatWentBackDoesNotStallTheContinue(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRefused)
	runTick(t, w, t0.Add(48*time.Hour)) // counted with the clock ahead
	if res := runTick(t, w, t0); res.Action != "none" || res.Reason != decide.RefusalWaitReason {
		t.Fatalf("%+v", res)
	}
	if res := runTick(t, w, t0.Add(time.Minute)); res.Action != "reprompt" || len(p.sent) != 1 {
		t.Fatalf("stalled: %+v", res)
	}
}

// A move to another target ends the streak without counting a recovery.
func TestAMoveEndsTheStreakWithoutARecovery(t *testing.T) {
	w, _ := declinedWorld(t, decide.ErrRefused)
	runTick(t, w, t0)
	st, _ := state.Load(w.dir)
	st.Refusals.Target = "claude-other" // as if the streak belonged to another target
	if err := state.Save(w.dir, st); err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0.Add(time.Minute))
	st, _ = state.Load(w.dir)
	if st.Refusals.Recovered != 0 || st.Refusals.Streak != 1 || st.Refusals.Target != "primary" || st.Refusals.Total != 2 {
		t.Fatalf("%+v", st.Refusals)
	}
}
