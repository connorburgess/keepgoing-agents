package supervise

import (
	"errors"
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
	if r.Action == "handoff" || r.Cause != "other" || r.TargetKey != incident.TargetKey(state.Primary) || r.TargetKeyAfter != r.TargetKey || len(r.Probes.Results) != 2 {
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

// With a reset beyond the short-wait window the engine first proposes a
// handoff; when every candidate fails its probe the re-evaluation waits, and
// the supervisor appends context to that reason.
func TestIncidentLabelsALimitWaitAfterAFailedHandoff(t *testing.T) {
	w := newWorld(t, true, true)
	obs := limited
	obs.ResetsAt = t0.Add(4 * time.Hour)
	p := w.primary(obs)
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	res := runTick(t, w, t0)
	if res.Action != "none" || res.Reason != "limit resets in 4h0m0s; no confirmed fallback (no fallback could take over; see probes and warnings)" {
		t.Fatalf("%+v", res)
	}
	r := lastIncident(t, w)
	e := r.Decisions.Evaluations
	if r.Cause != "limit_no_fallback" || len(e) != 2 || e[0].Action != "handoff" || e[0].Cause != "other" || e[1].Stage != "handoff_failed" || e[1].Cause != "limit_no_fallback" || e[1].Input.ResetIn != 4*time.Hour {
		t.Fatalf("%+v", r)
	}
	if r.TargetKeyAfter != r.TargetKey || len(r.Probes.Results) != 2 || len(p.sent) != 0 {
		t.Fatalf("%+v sent=%v", r, p.sent)
	}
}

// A handoff attempt whose candidates pass their exact probes but cannot be
// activated ends in the same wait as the checks around it. The attempt must
// stay its own record instead of being folded into that wait.
func TestIncidentKeepsAFailedHandoffAttemptOutOfTheCollapsedWait(t *testing.T) {
	w := newWorld(t, true, true)
	obs := limited
	obs.ResetsAt = t0.Add(10 * time.Hour)
	p := w.primary(obs)
	candidates := []string{"claude-other", "codex-main"}
	setHealth := func(at time.Time, ok bool) {
		t.Helper()
		reg := &health.Registry{Candidates: map[string]*health.Entry{}}
		for _, id := range candidates {
			reg.Record(id, probe.Result{At: at, Tier: probe.Scheduled, OK: ok, ErrorClass: "auth"})
		}
		if err := reg.Save(w.dir); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range candidates {
		w.adapters[id] = &fakeAdapter{obs: decide.Observation{State: decide.Idle}, activateErr: errors.New("model not available")}
		w.probeOK[id] = true
	}
	wait := func(minute int) {
		t.Helper()
		res := runTick(t, w, t0.Add(time.Duration(minute)*time.Minute))
		if res.Action != "none" || !strings.HasPrefix(res.Reason, "limit resets in ") || len(res.Probes) != 0 || res.Handoff != nil {
			t.Fatalf("minute %d was not a plain wait: %+v", minute, res)
		}
	}
	// No confirmed fallback, and no scheduled probe due: four plain waits.
	setHealth(t0.Add(-time.Hour), false)
	for _, minute := range []int{0, 10, 20, 30} {
		wait(minute)
	}
	// Both fallbacks become healthy, pass their exact probes, and cannot be activated.
	setHealth(t0.Add(35*time.Minute), true)
	res := runTick(t, w, t0.Add(40*time.Minute))
	if res.Action != "none" || !strings.HasSuffix(res.Reason, "(no fallback could take over; see probes and warnings)") || len(res.Probes) != 2 || res.Handoff == nil || res.Target != state.Primary {
		t.Fatalf("%+v", res)
	}
	setHealth(t0.Add(35*time.Minute), false)
	for _, minute := range []int{50, 60, 70, 80} {
		wait(minute)
	}
	j, err := incident.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []uint64
	for _, r := range j.Records {
		got = append(got, r.Sequence)
		if r.Cause != "limit_no_fallback" || r.Action != "none" || r.TargetKeyAfter != r.TargetKey {
			t.Fatalf("%+v", r)
		}
	}
	if len(got) != 5 || got[0] != 1 || got[1] != 4 || got[2] != 5 || got[3] != 6 || got[4] != 9 || j.Collapsed != 4 {
		t.Fatalf("sequences %v, collapsed %d", got, j.Collapsed)
	}
	attempt := j.Records[2]
	e := attempt.Decisions.Evaluations
	if attempt.Tier != incident.TierCondition || attempt.Collapsed != nil || len(e) != 2 || e[0].Action != "handoff" || e[1].Stage != "handoff_failed" || len(attempt.Probes.Results) != 2 {
		t.Fatalf("%+v", attempt)
	}
	for _, pr := range attempt.Probes.Results {
		if pr.Tier != "exact" || !pr.OK {
			t.Fatalf("%+v", pr)
		}
	}
	for _, i := range []int{1, 4} {
		if c := j.Records[i].Collapsed; c == nil || c.Count != 2 || j.Records[i].Tier != incident.TierCondition {
			t.Fatalf("record %d: %+v", i, j.Records[i])
		}
	}
	if len(p.sent) != 0 {
		t.Fatalf("sent %v", p.sent)
	}
}
