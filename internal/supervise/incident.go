package supervise

import (
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
	r := incident.Record{Kind: "tick", Decisions: t.incidentDecisions, CheckStartedAt: t.now, Build: t.opts.Version, Adapter: t.incidentAdapter,
		TargetKey: incident.TargetKey(t.incidentTarget), Observation: t.incidentObservation,
		// The target after this check differs only when a handoff or switch-back moved the loop.
		TargetKeyAfter: incident.TargetKey(t.res.Target), Probes: t.incidentProbes,
		ControlBefore: t.incidentControl, Cause: cause, Action: t.res.Action, Outcome: outcome, DryRun: t.opts.DryRun,
		CountersAvailable: t.incidentStateLoaded, Nudges: t.st.Nudges, Reprompts: t.st.Reprompts,
		LimitWaitReport: t.incidentLimitWaitReport}
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
	t.handoffAvailable = in.Failover.Available
	return d
}
