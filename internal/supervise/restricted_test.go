package supervise

import (
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
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
	if _, err := control.Update(w.dir, func(f *control.File) { f.AwaitingUser, f.Note = false, "" }); err != nil {
		t.Fatal(err)
	}
}

func count(reports, kind string) int { return strings.Count(","+reports+",", ","+kind+",") }

// A provider that refused or restricted the request is not out of quota.
// With healthy fallbacks and every class the configuration accepts listed in
// failover_on, the loop still holds for a person: nothing is sent to the
// session, no candidate is probed, activated or briefed, and the target does
// not change. Each kind of decision has its own reason.
func TestDeclinedSessionHoldsWithHealthyFallbacks(t *testing.T) {
	for class, reason := range map[decide.ErrorClass]string{decide.ErrRestricted: decide.RestrictedReason, decide.ErrRefused: decide.RefusedReason} {
		w, p := declinedWorld(t, class)
		res := runTick(t, w, t0)
		if res.Action != "await_user" || res.Reason != reason || res.Handoff != nil || res.Target != "primary" {
			t.Fatalf("%s: %+v %+v", class, res, res.Handoff)
		}
		if len(p.sent) != 0 {
			t.Fatalf("%s: the session was prompted: %v", class, p.sent)
		}
		for id, a := range w.adapters {
			if id != "primary" && (len(a.sent) != 0 || a.activated != 0) {
				t.Fatalf("%s: candidate %s was touched: sent=%v activated=%d", class, id, a.sent, a.activated)
			}
		}
		for _, n := range res.Probes {
			if n.Tier == probe.Exact {
				t.Fatalf("%s: an exact probe ran for a handoff that must not happen: %+v", class, n)
			}
		}
		st, _ := state.Load(w.dir)
		if st.ActiveTarget != "primary" && st.ActiveTarget != "" || st.Handoffs != 0 || st.DeclineHeldTarget != "primary" {
			t.Fatalf("%s: state = %+v", class, st)
		}
		c, err := control.Load(w.dir)
		if err != nil || !c.AwaitingUser || c.Note != reason {
			t.Fatalf("%s: the loop is not held for a person: %+v %v", class, c, err)
		}
		if count(w.reportTypes(), "await_user") != 1 || strings.Contains(w.reportTypes(), "handoff") {
			t.Fatalf("%s: reports = %s", class, w.reportTypes())
		}
		// Held: later checks send and report nothing.
		res = runTick(t, w, t0.Add(time.Hour))
		if res.Action != "none" || len(p.sent) != 0 || res.Target != "primary" || count(w.reportTypes(), "await_user") != 1 {
			t.Fatalf("%s: after the hold: %+v sent=%v reports=%s", class, res, p.sent, w.reportTypes())
		}
	}
}

// The way out. A person who resumes the held loop asks for one more try: the
// next check re-prompts once, on the same target, outside the re-prompt cap.
// If the turn is declined again the loop holds again, with one report for
// that new refusal. keepgoing never tries twice for one resume, and never
// moves.
func TestResumeAfterADeclinedTurnSendsExactlyOneReprompt(t *testing.T) {
	w, p := declinedWorld(t, decide.ErrRefused)
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
		if res.Action != "await_user" || res.Reason != decide.RefusedReason || len(p.sent) != i {
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
	w, p := declinedWorld(t, decide.ErrRefused)
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
