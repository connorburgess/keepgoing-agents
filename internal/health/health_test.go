package health

import (
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func fallbacks(min int, span string) *config.Fallbacks {
	mk := func(id, provider, group string) config.Candidate {
		return config.Candidate{ID: id, Agent: config.Agent{Adapter: "bb", Provider: provider, Group: group, BB: &config.BB{Thread: "thr_" + id}}}
	}
	return &config.Fallbacks{
		Redundancy: config.Redundancy{MinHealthy: min, Span: span},
		Probe:      config.ProbePolicy{Every: config.Duration{Duration: 4 * time.Hour}, TTL: config.Duration{Duration: 6 * time.Hour}, DailyCap: 2},
		Candidates: []config.Candidate{
			mk("claude-alt", "claude-code", "claude-account-a"),
			mk("claude-second", "claude-code", "claude-account-b"),
			mk("codex-main", "codex", "codex"),
		},
	}
}

func ok(at time.Time) probe.Result { return probe.Result{At: at, Tier: probe.Scheduled, OK: true} }
func fail(at time.Time) probe.Result {
	return probe.Result{At: at, Tier: probe.Scheduled, ErrorClass: "rate_limit"}
}

func TestEvaluateSpansProviders(t *testing.T) {
	fb := fallbacks(2, "provider")
	r := &Registry{Candidates: map[string]*Entry{}}
	r.Record("claude-alt", ok(now.Add(-time.Hour)))
	r.Record("claude-second", ok(now.Add(-time.Hour)))
	s := Evaluate(fb, r, now, "primary", "primary")
	if len(s.Healthy) != 2 || s.Distinct != 1 || s.Meets {
		t.Errorf("two claude accounts are one provider: %+v", s)
	}
	r.Record("codex-main", ok(now.Add(-time.Hour)))
	s = Evaluate(fb, r, now, "primary", "primary")
	if s.Distinct != 2 || !s.Meets || s.Healthy[0] != "claude-alt" {
		t.Errorf("with codex: %+v", s)
	}
	// Spanning groups instead counts accounts.
	fb.Redundancy.Span = "group"
	r2 := &Registry{Candidates: map[string]*Entry{}}
	r2.Record("claude-alt", ok(now))
	r2.Record("claude-second", ok(now))
	if s := Evaluate(fb, r2, now, "primary", "primary"); s.Distinct != 2 || !s.Meets {
		t.Errorf("group span: %+v", s)
	}
}

func TestEvaluateExcludesActiveAndItsGroup(t *testing.T) {
	fb := fallbacks(1, "provider")
	r := &Registry{Candidates: map[string]*Entry{}}
	for _, id := range []string{"claude-alt", "claude-second", "codex-main"} {
		r.Record(id, ok(now))
	}
	s := Evaluate(fb, r, now, "claude-alt", "claude-account-a")
	if len(s.Eligible) != 2 || s.Eligible[0] != "claude-second" {
		t.Errorf("%+v", s)
	}
}

func TestStaleAndFailedProbesAreNotHealthy(t *testing.T) {
	fb := fallbacks(1, "none")
	r := &Registry{Candidates: map[string]*Entry{}}
	r.Record("claude-alt", ok(now.Add(-7*time.Hour)))
	r.Record("codex-main", fail(now))
	if s := Evaluate(fb, r, now, "", ""); len(s.Healthy) != 0 || s.Meets {
		t.Errorf("%+v", s)
	}
}

func TestDueRespectsIntervalAndDailyCap(t *testing.T) {
	fb := fallbacks(1, "none")
	c := fb.Candidates[0]
	r := &Registry{Candidates: map[string]*Entry{}}
	if due, _ := r.Due(c, fb.Probe, now); !due {
		t.Error("never probed should be due")
	}
	r.Record(c.ID, ok(now.Add(-time.Hour)))
	if due, why := r.Due(c, fb.Probe, now); due || why != "probed recently" {
		t.Errorf("%v %s", due, why)
	}
	r.Record(c.ID, ok(now.Add(-5*time.Hour+time.Minute))) // second probe today
	if due, why := r.Due(c, fb.Probe, now.Add(time.Hour)); due || why != "daily cap reached" {
		t.Errorf("%v %s", due, why)
	}
	if due, _ := r.Due(c, fb.Probe, now.Add(13*time.Hour)); !due {
		t.Error("cap should reset the next UTC day")
	}
	fb.Probe.Disabled = true
	if due, _ := r.Due(c, fb.Probe, now.Add(24*time.Hour)); due {
		t.Error("disabled scheduled probes are never due")
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{Candidates: map[string]*Entry{}}
	for i := 0; i < 7; i++ {
		r.Record("x", ok(now.Add(time.Duration(i)*time.Minute)))
	}
	if err := r.Save(dir); err != nil {
		t.Fatal(err)
	}
	g, err := Load(dir)
	if err != nil || len(g.Candidates["x"].History) != 5 || !g.Healthy("x", now.Add(time.Hour), 6*time.Hour) {
		t.Errorf("%+v %v", g, err)
	}
}
