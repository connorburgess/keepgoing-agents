package health

import (
	"os"
	"path/filepath"
	"strings"
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

func TestFailedManualProbeDoesNotDelaySchedule(t *testing.T) {
	fb := fallbacks(1, "none")
	c := fb.Candidates[0]
	r := &Registry{Candidates: map[string]*Entry{}}
	r.Record(c.ID, probe.Result{At: now.Add(-time.Minute), Tier: probe.Manual, ErrorClass: "rate_limit"})
	if due, why := r.Due(c, fb.Probe, now); !due {
		t.Errorf("failed manual probe blocked the schedule: %s", why)
	}
	r.Record(c.ID, probe.Result{At: now.Add(-time.Minute), Tier: probe.Manual, OK: true})
	if due, _ := r.Due(c, fb.Probe, now); due {
		t.Error("a passing manual probe is fresh evidence")
	}
	r2 := &Registry{Candidates: map[string]*Entry{}}
	r2.Record(c.ID, probe.Result{At: now.Add(-time.Hour), Tier: probe.Manual, OK: true})
	if r2.Candidates[c.ID].ProbesToday != 0 {
		t.Error("manual probes must not count toward the daily cap")
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

func TestUsageReadingIsKeptPerCandidateAndBounded(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{Candidates: map[string]*Entry{}}
	withUsage := ok(now.Add(-2 * time.Hour))
	withUsage.Raw = []byte("fictional raw output")
	withUsage.UsageIdentity = "account-a"
	withUsage.Usage = &probe.Usage{At: withUsage.At, Source: probe.UsageSourceClaude, Identity: "account-a", Windows: []probe.UsageWindow{{Key: "seven_day", Used: 0.22, Status: "allowed"}}}
	r.Record("claude-alt", withUsage)
	// A later probe of the same account without a reading keeps the earlier
	// one, with its own time.
	sameAccount := fail(now.Add(-time.Hour))
	sameAccount.UsageIdentity = "account-a"
	r.Record("claude-alt", sameAccount)
	r.Record("codex-main", ok(now))
	if err := r.Save(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Count(string(b), `"usage"`) != 1 || strings.Contains(string(b), "fictional raw output") {
		t.Fatalf("stored record: %s", b)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	u := got.Candidates["claude-alt"].Usage
	if !u.Available() || !u.At.Equal(now.Add(-2*time.Hour)) || u.Windows[0].Used != 0.22 || !got.Candidates["claude-alt"].Last.At.Equal(now.Add(-time.Hour)) {
		t.Fatalf("reading: %+v", u)
	}
	if got.Candidates["codex-main"].Usage != nil {
		t.Fatal("reading invented for a probe that carried none")
	}
	// Once the candidate is probed as another account the reading is dropped,
	// whether or not the new probe carries one.
	otherAccount := ok(now)
	otherAccount.UsageIdentity = "account-b"
	got.Record("claude-alt", otherAccount)
	if got.Candidates["claude-alt"].Usage != nil {
		t.Fatal("reading kept after the account identity changed")
	}
	otherAccount.Usage = &probe.Usage{At: now, Source: probe.UsageSourceClaude, Identity: "account-b", Windows: []probe.UsageWindow{{Key: "seven_day", Used: 0.5}}}
	got.Record("claude-alt", otherAccount)
	if u := got.Candidates["claude-alt"].Usage; !u.For("account-b") || u.Windows[0].Used != 0.5 {
		t.Fatalf("new account's reading: %+v", u)
	}
}

func TestHealthFilesWithoutOrWithBadUsageStillLoad(t *testing.T) {
	last := `"last":{"at":"2026-11-09T11:00:00Z","tier":"scheduled","kind":"claude","ok":true,"latencyMs":5,"exitCode":0}`
	for name, usage := range map[string]string{
		"file written before readings existed": ``,
		"null":                                 `,"usage":null`,
		"wrong type":                           `,"usage":"seven_day 22%"`,
		"wrong field types":                    `,"usage":{"at":5,"windows":"x"}`,
		"out of range":                         `,"usage":{"at":"2026-11-09T11:00:00Z","source":"claude_rate_limit_event","identity":"account-a","windows":[{"key":"seven_day","used":22}]}`,
		"no account identity":                  `,"usage":{"at":"2026-11-09T11:00:00Z","source":"claude_rate_limit_event","windows":[{"key":"seven_day","used":0.22}]}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"candidates":{"claude-alt":{`+last+usage+`}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := Load(dir)
		if err != nil || r.Candidates["claude-alt"].Usage != nil || !r.Healthy("claude-alt", now, 6*time.Hour) {
			t.Errorf("%s: %+v err=%v", name, r, err)
		}
	}
}
