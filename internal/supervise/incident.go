package supervise

import (
	"github.com/connorburgess/keepgoing-agents/internal/control"
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
	r := incident.Record{CheckStartedAt: t.now, Build: t.opts.Version, Adapter: t.incidentAdapter,
		TargetKey: incident.TargetKey(t.incidentTarget), Observation: t.incidentObservation,
		ControlBefore: t.incidentControl, Cause: incident.Cause(t.res.Reason), Action: t.res.Action, Outcome: outcome, DryRun: t.opts.DryRun,
		CountersAvailable: t.incidentStateLoaded, Nudges: t.st.Nudges, Reprompts: t.st.Reprompts}
	if c, err := control.Load(t.loop.StateDir); err == nil {
		r.ControlAfter = incident.Gate(c.Gate())
	}
	if s := t.st.StallRetry; s != nil && s.Target == t.incidentTarget && !s.Waiting && !s.ResumedAt.IsZero() {
		at := s.ResumedAt
		r.LastActiveRecoveryAt = &at
	}
	return incident.Append(t.loop.StateDir, r)
}
