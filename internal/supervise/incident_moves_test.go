package supervise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestIncidentRecordsHandoffSwitchBackTargetsAndProbes(t *testing.T) {
	w, p, _ := switchBackWorld(t)
	primary, fallback := incident.TargetKey(state.Primary), incident.TargetKey("claude-other")
	// The setup tick handed off: source and destination are both retained.
	r := lastIncident(t, w)
	if r.Action != "handoff" || r.TargetKey != primary || r.TargetKeyAfter != fallback || r.Probes.Availability != "captured" || len(r.Probes.Results) != 1 {
		t.Fatalf("%+v", r)
	}
	if pr := r.Probes.Results[0]; pr.TargetKey != fallback || pr.Tier != "exact" || !pr.OK || pr.ErrorClass != "none" {
		t.Fatalf("%+v", pr)
	}
	exactPrimary := func(r incident.Record) *incident.ProbeResult {
		for _, pr := range r.Probes.Results {
			if pr.Tier == "exact" && pr.TargetKey == primary {
				return &pr
			}
		}
		return nil
	}
	// An ordinary check on the fallback moves nothing; a due scheduled probe of
	// another candidate is retained as such, never as an exact handoff probe.
	runTick(t, w, t0.Add(time.Hour))
	if r = lastIncident(t, w); r.Action != "wake" || r.TargetKey != fallback || r.TargetKeyAfter != fallback || r.Probes.Availability != "captured" || exactPrimary(r) != nil {
		t.Fatalf("%+v", r)
	}
	for _, pr := range r.Probes.Results {
		if pr.Tier != "scheduled" || pr.TargetKey == fallback || pr.TargetKey == primary {
			t.Fatalf("%+v", pr)
		}
	}
	// A failed switch-back probe is retained and the target stays put.
	runTick(t, w, t0.Add(2*time.Hour+time.Minute))
	r = lastIncident(t, w)
	if pr := exactPrimary(r); r.Action != "wake" || r.TargetKeyAfter != fallback || pr == nil || pr.OK || pr.ErrorClass != "rate_limit" {
		t.Fatalf("%+v", r)
	}
	w.probeOK[state.Primary] = true
	p.obs = decide.Observation{State: decide.Idle}
	runTick(t, w, t0.Add(3*time.Hour))
	r = lastIncident(t, w)
	if pr := exactPrimary(r); r.Action != "switch_back" || r.TargetKey != fallback || r.TargetKeyAfter != primary || pr == nil || !pr.OK || pr.ErrorClass != "none" {
		t.Fatalf("%+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(w.dir, incident.FileName))
	for _, private := range []string{"claude-other", "codex-main", "thr_"} {
		if strings.Contains(string(b), private) {
			t.Fatalf("%s leaked into incident history", private)
		}
	}
}

func TestIncidentRecordsFailedHandoffProbesWithoutAMove(t *testing.T) {
	w := newWorld(t, true, true)
	w.primary(limited)
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	runTick(t, w, t0)
	r := lastIncident(t, w)
	if r.Action == "handoff" || r.TargetKey != incident.TargetKey(state.Primary) || r.TargetKeyAfter != r.TargetKey || len(r.Probes.Results) != 2 {
		t.Fatalf("%+v", r)
	}
	for i, id := range []string{"claude-other", "codex-main"} {
		if pr := r.Probes.Results[i]; pr.TargetKey != incident.TargetKey(id) || pr.OK || pr.ErrorClass != "rate_limit" || pr.Tier != "exact" {
			t.Fatalf("%+v", pr)
		}
	}
}

func TestIncidentLabelsALongLimitWaitWithFailoverDisabled(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Errors.FailoverOn = []string{}
	obs := limited
	obs.ResetsAt = t0.Add(72 * time.Hour)
	p := w.primary(obs)
	runTick(t, w, t0)
	r := lastIncident(t, w)
	e := r.Decisions.Evaluations
	if r.Action != "none" || r.Cause != "limit_failover_disabled" || len(e) != 1 || e[0].Cause != "limit_failover_disabled" || e[0].Input.ResetIn != 72*time.Hour || e[0].Input.FailoverEligible {
		t.Fatalf("%+v", r)
	}
	// Recording the wait changes nothing: no send, no hold, no report.
	if len(p.sent) != 0 || r.ControlAfter.AwaitingUser || len(w.reports) != 0 {
		t.Fatalf("sent=%v control=%+v reports=%s", p.sent, r.ControlAfter, w.reportTypes())
	}
}
