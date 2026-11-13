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

// A provider that refused or restricted the request is not out of quota.
// With healthy fallbacks and every class the configuration accepts listed in
// failover_on, the loop still holds for a person: nothing is sent to the
// session, no candidate is probed, activated or briefed, and the target does
// not change.
func TestRestrictedSessionHoldsWithHealthyFallbacks(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.Loop.Errors.FailoverOn = []string{"rate_limit", "auth", "overloaded", "network"}
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrRestricted})
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"same-account", "claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}

	res := runTick(t, w, t0)
	if res.Action != "await_user" || res.Reason != decide.RestrictedReason || res.Handoff != nil || res.Target != "primary" {
		t.Fatalf("%+v %+v", res, res.Handoff)
	}
	if len(p.sent) != 0 {
		t.Fatalf("the restricted session was prompted: %v", p.sent)
	}
	for id, a := range w.adapters {
		if id != "primary" && (len(a.sent) != 0 || a.activated != 0) {
			t.Fatalf("candidate %s was touched: sent=%v activated=%d", id, a.sent, a.activated)
		}
	}
	for _, n := range res.Probes {
		if n.Tier == probe.Exact {
			t.Fatalf("an exact probe ran for a handoff that must not happen: %+v", n)
		}
	}
	st, _ := state.Load(w.dir)
	if st.ActiveTarget != "primary" && st.ActiveTarget != "" || st.Handoffs != 0 {
		t.Fatalf("state = %+v", st)
	}
	c, err := control.Load(w.dir)
	if err != nil || !c.AwaitingUser {
		t.Fatalf("the loop is not held for a person: %+v %v", c, err)
	}
	if !strings.Contains(w.reportTypes(), "await_user") || strings.Contains(w.reportTypes(), "handoff") {
		t.Fatalf("reports = %s", w.reportTypes())
	}

	// Held: later checks send nothing either.
	res = runTick(t, w, t0.Add(time.Hour))
	if res.Action != "none" || len(p.sent) != 0 || res.Target != "primary" {
		t.Fatalf("after the hold: %+v sent=%v", res, p.sent)
	}
}
