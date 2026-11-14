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

// Every probe that carries a reading adds it to the candidate's history for
// that window instance; a probe without one adds nothing, and a probe of
// another account starts over. The history survives a save and load.
func TestUsageHistoryAccumulatesPerAccount(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{Candidates: map[string]*Entry{}}
	resets := now.Add(72 * time.Hour)
	probeAt := func(at time.Time, identity string, used float64) probe.Result {
		res := ok(at)
		res.UsageIdentity = identity
		if used >= 0 {
			res.Usage = &probe.Usage{At: at, Source: probe.UsageSourceClaude, Identity: identity, Windows: []probe.UsageWindow{{Key: "seven_day", Used: used, ResetsAt: &resets}}}
		}
		return res
	}
	r.Record("claude-alt", probeAt(now.Add(-12*time.Hour), "account-a", 0.1))
	r.Record("claude-alt", probeAt(now.Add(-8*time.Hour), "account-a", -1))
	r.Record("claude-alt", probeAt(now.Add(-6*time.Hour), "account-a", 0.15))
	r.Record("claude-alt", probeAt(now, "account-a", 0.2))
	if err := r.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := got.Candidates["claude-alt"].UsageHistory
	if s := h.Find("seven_day"); !h.For("account-a") || s == nil || len(s.Samples) != 3 || s.Samples[2].Used != 0.2 || !s.ResetsAt.Equal(resets) {
		t.Fatalf("history: %+v", h)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Count(string(b), `"usageHistory"`) != 1 {
		t.Fatalf("stored record: %s", b)
	}
	got.Record("claude-alt", probeAt(now.Add(time.Hour), "account-b", -1))
	if got.Candidates["claude-alt"].UsageHistory != nil {
		t.Fatal("history kept after the account identity changed")
	}
	// A stored history that is not one loads as absent and leaves health intact.
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"candidates":{"claude-alt":{"last":{"at":"2026-11-09T11:00:00Z","tier":"scheduled","kind":"claude","ok":true,"latencyMs":5,"exitCode":0},"usageHistory":"seven_day 20%"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(dir); err != nil || got.Candidates["claude-alt"].UsageHistory != nil || !got.Healthy("claude-alt", now, 6*time.Hour) {
		t.Fatalf("%+v %v", got, err)
	}
}

// When the first route's timing probe is due, in table form.
func TestTimingProbeTable(t *testing.T) {
	c := config.Candidate{ID: "primary"}
	timing := config.Timing{Enabled: true, Windows: []string{"seven_day"}, NearReset: config.Duration{Duration: 6 * time.Hour}, ProbeEvery: config.Duration{Duration: 12 * time.Hour}}
	policy := config.ProbePolicy{DailyCap: 2}
	week := now.Add(5 * time.Hour)
	reading := func(at time.Time, identity string, windows ...probe.UsageWindow) *probe.Usage {
		return &probe.Usage{At: at, Source: probe.UsageSourceClaude, Identity: identity, Windows: windows}
	}
	weekWin := probe.UsageWindow{Key: "seven_day", Used: 0.4, ResetsAt: &week}
	shortWin := probe.UsageWindow{Key: "five_hour", Used: 0.4, ResetsAt: &week}
	for name, tc := range map[string]struct {
		entry  *Entry
		policy config.ProbePolicy
		due    bool
		why    string
		near   bool
	}{
		"never probed":                                   {nil, policy, true, TimingDue, false},
		"scheduled probes off":                           {nil, config.ProbePolicy{DailyCap: 2, Disabled: true}, false, TimingScheduledOff, false},
		"probed 11 h ago":                                {&Entry{LastSched: now.Add(-11 * time.Hour)}, policy, false, TimingRecent, false},
		"passed a probe of any tier 11 h ago":            {&Entry{Last: &probe.Result{At: now.Add(-11 * time.Hour), OK: true}}, policy, false, TimingRecent, false},
		"probed 12 h ago":                                {&Entry{LastSched: now.Add(-12 * time.Hour)}, policy, true, TimingDue, false},
		"cap reached today":                              {&Entry{ProbeDay: now.Format("2006-01-02"), ProbesToday: 2}, policy, false, TimingCapReached, false},
		"named window resets in 5 h, last read 11 h ago": {&Entry{LastSched: now.Add(-11 * time.Hour), Usage: reading(now.Add(-11*time.Hour), "account-a", weekWin)}, policy, true, TimingNearReset, true},
		"that instance already had its probe": {&Entry{LastSched: now.Add(-11 * time.Hour), Usage: reading(now.Add(-11*time.Hour), "account-a", weekWin),
			NearReset: []NearResetMark{{Key: "seven_day", ResetsAt: week}}}, policy, false, TimingRecent, false},
		"already read within the near period":   {&Entry{LastSched: now.Add(-11 * time.Hour), Usage: reading(now.Add(-time.Hour), "account-a", weekWin)}, policy, false, TimingRecent, false},
		"a window not named":                    {&Entry{LastSched: now.Add(-11 * time.Hour), Usage: reading(now.Add(-11*time.Hour), "account-a", shortWin)}, policy, false, TimingRecent, false},
		"a reading of another account":          {&Entry{LastSched: now.Add(-11 * time.Hour), Usage: reading(now.Add(-11*time.Hour), "account-b", weekWin)}, policy, false, TimingRecent, false},
		"near the reset but the cap is reached": {&Entry{ProbeDay: now.Format("2006-01-02"), ProbesToday: 2, Usage: reading(now.Add(-11*time.Hour), "account-a", weekWin)}, policy, false, TimingCapReached, false},
	} {
		r := &Registry{Candidates: map[string]*Entry{}}
		if tc.entry != nil {
			r.Candidates["primary"] = tc.entry
		}
		due, why, near := r.TimingProbe(c, timing, tc.policy, "account-a", now)
		if due != tc.due || why != tc.why || (near != nil) != tc.near {
			t.Errorf("%s: due %v %q near %+v", name, due, why, near)
		}
		if near != nil && (near.Key != "seven_day" || !near.ResetsAt.Equal(week)) {
			t.Errorf("%s: mark %+v", name, near)
		}
	}
	// Marks of instances that have reset are dropped; the newest are kept.
	r := &Registry{Candidates: map[string]*Entry{}}
	r.MarkNearReset("primary", NearResetMark{Key: "seven_day", ResetsAt: now.Add(-time.Hour)}, now.Add(-2*time.Hour))
	r.MarkNearReset("primary", NearResetMark{Key: "seven_day", ResetsAt: week}, now)
	r.MarkNearReset("primary", NearResetMark{Key: "seven_day", ResetsAt: week}, now)
	if m := r.Candidates["primary"].NearReset; len(m) != 1 || !m[0].ResetsAt.Equal(week) {
		t.Fatalf("marks: %+v", m)
	}
}
