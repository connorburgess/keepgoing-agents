package supervise

import (
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
)

func (t *tick) recordIncident(failed bool) error {
	outcome := "succeeded"
	if t.opts.DryRun {
		outcome = "dry_run"
	}
	if failed {
		outcome = "failed"
	}
	t.incidentDecisions.Availability = "captured"
	t.incidentProbes.Availability = "captured"
	// The supervisor may append context to a reason; the typed rule is unaffected.
	cause := incident.Cause(t.res.Reason)
	if t.incidentLimitWait != "" && t.res.Action == string(decide.None) {
		cause = string(t.incidentLimitWait)
	}
	// A check in which usage-aware timing changed the thread has its cause;
	// the engine's own decision stays among the record's evaluations.
	if t.incidentTimingCause != "" {
		cause = t.incidentTimingCause
	}
	r := incident.Record{Kind: "tick", Decisions: t.incidentDecisions, CheckStartedAt: t.now, Build: t.opts.Version, Adapter: t.incidentAdapter,
		TargetKey: incident.TargetKey(t.incidentTarget), Observation: t.incidentObservation,
		// The target after this check differs when a handoff, a switch-back,
		// a usage-aware timing step or a route leaving the configuration moved the loop.
		TargetKeyAfter: incident.TargetKey(t.res.Target), Probes: t.incidentProbes,
		ControlBefore: t.incidentControl, Cause: cause, Action: t.res.Action, Outcome: outcome, DryRun: t.opts.DryRun,
		CountersAvailable: t.incidentStateLoaded, Nudges: t.st.Nudges, Reprompts: t.st.Reprompts,
		LimitWaitReport: t.incidentLimitWaitReport, NewThread: t.incidentNewThread, RouteListChanged: t.incidentRouteListChanged, TurnRetry: t.incidentTurnRetry, Lanes: t.incidentLanes, Refusal: t.refusalMark, ModelFallback: t.fallbackMark}
	if t.returnSaved {
		r.Returned = t.incidentReturn
	}
	if c, err := control.Load(t.loop.StateDir); err == nil {
		r.ControlAfter = incident.Gate(c.Gate())
	}
	if s := t.st.StallRetry; s != nil && s.Target == t.incidentTarget && !s.Waiting && !s.ResumedAt.IsZero() {
		at := s.ResumedAt
		r.LastActiveRecoveryAt = &at
	}
	return incident.Append(t.loop.StateDir, r)
}

func (t *tick) decide(stage string, in decide.Input) decide.Decision {
	d := decide.Decide(in)
	t.incidentDecisions.Add(stage, in, d)
	t.incidentLimitWait = d.LimitWait
	// A limit wait with a structured reset still ahead: a daemon checks again
	// at that reset rather than at the end of its interval.
	t.limitResetsAt = time.Time{}
	if d.LimitWait != "" && in.Obs.ResetsAt.After(t.now) {
		t.limitResetsAt = in.Obs.ResetsAt
	}
	t.handoffAvailable = in.Failover.Available
	return d
}
