package incident

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// memJournal applies the journal's own add path without a file write per
// record; flush stores the result so it is checked by the real loader.
type memJournal struct{ j Journal }

func newMemJournal() *memJournal {
	return &memJournal{Journal{SchemaVersion: SchemaVersion, NextSequence: 1, Records: []Record{}}}
}

func (m *memJournal) add(t *testing.T, r Record) {
	t.Helper()
	if !validRecord(r) {
		t.Fatal("invalid fixture record")
	}
	r.Sequence = m.j.NextSequence
	m.j.NextSequence++
	m.j.add(clean(r))
}

func (m *memJournal) flush(t *testing.T) Journal {
	t.Helper()
	dir := t.TempDir()
	b, err := m.j.encode()
	if err != nil || os.WriteFile(filepath.Join(dir, FileName), b, 0600) != nil {
		t.Fatal(err)
	}
	return loadAccounted(t, dir)
}

func hasSequence(j Journal, seq uint64) bool {
	for _, r := range j.Records {
		if r.Sequence == seq {
			return true
		}
	}
	return false
}

// After a handoff, the primary fails its exact switch-back probe every 30
// minutes for weeks. Each attempt is a wake on the fallback and stays its own
// record, but none of them may evict the handoff.
func TestFailedSwitchBackProbesCannotEvictTheHandoff(t *testing.T) {
	m := newMemJournal()
	primary, fallback := TargetKey("primary"), TargetKey("fictional-fallback")
	move := retentionTick(0)
	move.Action, move.TargetKeyAfter, move.Cause = "handoff", fallback, "other"
	m.add(t, move) // 1
	for i := 1; i <= 1000; i++ {
		r := retentionTick(i * 3)
		r.TargetKey, r.TargetKeyAfter, r.Action, r.Cause = fallback, fallback, "wake", "idle"
		r.Observation.State = "idle"
		r.Decisions.Evaluations = []Evaluation{{Stage: "normal", Action: "switch_back", Cause: "other"}, {Stage: "switch_back_failed", Action: "wake", Cause: "idle"}}
		r.Probes.Add("primary", "exact", false, "rate_limit")
		m.add(t, r)
	}
	j := m.flush(t)
	if !hasSequence(j, 1) || j.Records[0].Action != "handoff" || j.Records[0].TargetKey != primary || j.Pruned.Key.Count != 0 {
		t.Fatalf("the handoff was evicted by probe noise: first retained %d, pruned %+v", j.Records[0].Sequence, j.Pruned)
	}
	if j.Pruned.Condition.Count == 0 || j.Collapsed != 0 {
		t.Fatalf("attempts were folded or never pruned: %+v collapsed %d", j.Pruned, j.Collapsed)
	}
}

// A retarget, then a limit wait with no confirmed fallback while the fallback
// fails its scheduled probe on every probe interval. The first failure is an
// event; identical repeats are folded, at any probe interval.
func TestRepeatedFailedScheduledProbesFoldIntoTheWait(t *testing.T) {
	for _, every := range []int{24, 6, 1} {
		m := newMemJournal()
		m.add(t, Record{Kind: "retarget", CheckStartedAt: retentionStart.Add(-time.Hour), TargetKey: TargetKey("fictional-fallback"), TargetKeyAfter: TargetKey("primary")}) // 1
		const checks = 1440                                                                                                                                                  // ten days at ten-minute checks
		for i := 0; i < checks; i++ {
			r := retentionTick(i)
			r.Cause, r.Observation = "limit_no_fallback", Observation{Availability: "observed", State: "error", ErrorClass: "rate_limit"}
			if i%every == every-1 {
				r.Probes.Add("fictional-fallback", "scheduled", false, "auth")
			}
			m.add(t, r)
		}
		j := m.flush(t)
		if !hasSequence(j, 1) || j.Dropped != 0 || len(j.Records) > 6 {
			t.Fatalf("every %d: %d records, dropped %d, first %d", every, len(j.Records), j.Dropped, j.Records[0].Sequence)
		}
		// The first failed probe stands as its own record with its result.
		first := uint64(every + 1)
		if !hasSequence(j, first) {
			t.Fatalf("every %d: the first failed probe (#%d) was folded", every, first)
		}
		latest := j.Records[len(j.Records)-1]
		if latest.Sequence != checks+1 || latest.Collapsed == nil || latest.Collapsed.FailedProbes != checks/every-1-len(latest.Probes.Results) {
			t.Fatalf("every %d: %+v", every, latest.Collapsed)
		}
	}
}

// A probe result that differs from the run's first record is news.
func TestAChangedProbeResultEndsTheRun(t *testing.T) {
	dir := t.TempDir()
	n := 0
	add := func(alias string, ok bool, class string) {
		r := retentionTick(n)
		n++
		if alias != "" {
			r.Probes.Add(alias, "scheduled", ok, class)
		}
		mustAppend(t, dir, r)
	}
	add("", false, "")                      // 1
	add("", false, "")                      // 2
	add("fictional-a", true, "")            // 3 a's first result in the run: kept
	add("", false, "")                      // 4
	add("fictional-a", false, "auth")       // 5 first failure: kept
	add("", false, "")                      // 6 folded
	add("fictional-a", false, "auth")       // 7 same failure: folded
	add("", false, "")                      // 8
	add("fictional-a", false, "rate_limit") // 9 a different class: kept
	add("", false, "")                      // 10
	add("fictional-b", false, "rate_limit") // 11 another candidate: kept
	add("", false, "")                      // 12
	add("fictional-b", true, "")            // 13 recovered: kept
	add("", false, "")                      // 14 folded
	add("", false, "")                      // 15 folded
	add("fictional-b", true, "")            // 16 same pass: folded
	add("", false, "")                      // 17
	j := loadAccounted(t, dir)
	var got []uint64
	for _, r := range j.Records {
		got = append(got, r.Sequence)
	}
	want := []uint64{1, 2, 3, 4, 5, 8, 9, 10, 11, 12, 13, 17}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v, want %v", got, want)
		}
	}
	if c := j.Records[5].Collapsed; c == nil || c.Count != 2 || c.FailedProbes != 1 || c.PassedProbes != 0 || len(c.ProbeTargetKeys) != 1 || c.ProbeTargetKeys[0] != TargetKey("fictional-a") {
		t.Fatalf("%+v", c)
	}
	if c := j.Records[11].Collapsed; c == nil || c.Count != 3 || c.PassedProbes != 1 || c.FailedProbes != 0 || len(c.ProbeTargetKeys) != 1 || c.ProbeTargetKeys[0] != TargetKey("fictional-b") {
		t.Fatalf("%+v", c)
	}
}

// When the record that showed a candidate failing has been pruned, its next
// result is not assumed to be a repeat: a recovery stays visible.
func TestARecoveryIsKeptAfterItsFailureRecordIsPruned(t *testing.T) {
	m := newMemJournal()
	probe := func(n int, ok bool) {
		r := retentionTick(n)
		if n%2 == 1 {
			r.Probes.Add("fictional-a", "scheduled", ok, "auth")
		}
		m.add(t, r)
	}
	for n := 0; n < 8; n++ {
		probe(n, false) // 1, 2 (first failure), then folded into 8
	}
	if len(m.j.Records) != 3 || m.j.Records[1].Sequence != 2 || m.j.Records[2].Collapsed.FailedProbes != 2 {
		t.Fatalf("%+v", m.j.Records)
	}
	// Retention removes the run's first check and the first-failure record.
	if !m.j.dropOldest(TierRoutine) || !m.j.dropOldest(TierCondition) || len(m.j.Records) != 1 {
		t.Fatalf("%+v", m.j.Records)
	}
	probe(8, true)
	probe(9, true) // the candidate passes: nothing retained says it was failing
	probe(10, true)
	j := m.flush(t)
	var recovery *Record
	for i, r := range j.Records {
		if len(r.Probes.Results) == 1 && r.Probes.Results[0].OK {
			recovery = &j.Records[i]
		}
	}
	if recovery == nil || recovery.Sequence != 10 || recovery.Collapsed != nil {
		t.Fatalf("the recovery was folded: %+v", j.Records)
	}
}

func TestCollapsedSpanSurvivesAClockRollback(t *testing.T) {
	dir := t.TempDir()
	for _, minute := range []int{0, 50, 10, 40, 20, 30} {
		r := retentionTick(0)
		r.CheckStartedAt = retentionStart.Add(time.Duration(minute) * time.Minute)
		mustAppend(t, dir, r)
	}
	j := loadAccounted(t, dir)
	c := j.Records[1].Collapsed
	if len(j.Records) != 2 || c == nil || c.Count != 4 || !c.FirstAt.Equal(retentionStart.Add(10*time.Minute)) || !c.LastAt.Equal(retentionStart.Add(50*time.Minute)) {
		t.Fatalf("%+v", c)
	}
}

func TestCollapsingContinuesAfterTheRunsFirstRecordIsPruned(t *testing.T) {
	m := newMemJournal()
	for i := 0; i < 5; i++ {
		m.add(t, retentionTick(i)) // 1 and 5 retained, three folded
	}
	if !m.j.dropOldest(TierRoutine) || len(m.j.Records) != 1 || m.j.Records[0].Sequence != 5 {
		t.Fatalf("%+v", m.j.Records)
	}
	// The run's surviving summary is not a first record to fold into, and
	// stays as it is; the run goes on from it.
	m.add(t, retentionTick(5)) // 6
	if len(m.j.Records) != 2 || m.j.Records[1].Collapsed != nil {
		t.Fatalf("%+v", m.j.Records)
	}
	m.add(t, retentionTick(6)) // 7 folds 6
	m.add(t, retentionTick(7)) // 8 folds 7
	j := m.flush(t)
	if len(j.Records) != 2 || j.Records[0].Sequence != 5 || j.Records[0].Collapsed.Count != 3 || j.Records[1].Sequence != 8 || j.Records[1].Collapsed.Count != 2 {
		t.Fatalf("%+v", j.Records)
	}
	if j.Collapsed != 5 || j.Dropped != 1 || j.Pruned.Routine.Count != 1 || j.NextSequence != 9 {
		t.Fatalf("collapsed %d dropped %d pruned %+v", j.Collapsed, j.Dropped, j.Pruned)
	}
}

func TestSummaryOnANonRepeatableCheckIsRefused(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		mustAppend(t, dir, retentionTick(i))
	}
	good := loadAccounted(t, dir)
	path := filepath.Join(dir, FileName)
	var exact Probes
	exact.Add("fictional-candidate", "exact", true, "")
	for name, tamper := range map[string]func(*Record){
		"summary on a send":         func(r *Record) { r.Action = "wake" },
		"summary on a hold entry":   func(r *Record) { r.ControlAfter = &Controls{Enabled: true, Paused: true} },
		"summary on an exact probe": func(r *Record) { r.Probes = exact },
		"summary on a failed move": func(r *Record) {
			r.Decisions.Evaluations = []Evaluation{{Stage: "handoff_failed", Action: "none", Cause: "limit_no_fallback"}}
		},
		"inverted span":          func(r *Record) { r.Collapsed.FirstAt = r.Collapsed.LastAt.Add(time.Minute) },
		"negative failed probes": func(r *Record) { r.Collapsed.FailedProbes = -1 },
	} {
		j := good
		j.Records = append([]Record{}, good.Records...)
		c := *good.Records[1].Collapsed
		j.Records[1].Collapsed = &c
		tamper(&j.Records[1])
		b, _ := json.Marshal(j)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err != ErrUnavailable {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A schema-5 journal of checks that cannot collapse keeps them all: tiers
// borrow unused room, and the upgrade prunes only what the total bound needs.
func TestUpgradeDoesNotPruneAlternatingLegacyChecksInBulk(t *testing.T) {
	dir := t.TempDir()
	old := Journal{SchemaVersion: 5, NextSequence: 257, Records: []Record{}}
	for i := 0; i < 256; i++ {
		r := retentionTick(i)
		if i%2 == 1 {
			r.Observation.State, r.Cause = "idle", "idle"
		}
		r.Sequence = uint64(i + 1)
		old.Records = append(old.Records, r)
	}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, FileName), b, 0600); err != nil {
		t.Fatal(err)
	}
	mustAppend(t, dir, retentionTick(256))
	j := loadAccounted(t, dir)
	if j.SchemaVersion != SchemaVersion || len(j.Records) != MaxRecords || j.Dropped != 1 || j.Pruned.Routine.Count != 1 || j.Collapsed != 0 || j.Records[0].Sequence != 2 {
		t.Fatalf("retained %d dropped %d pruned %+v", len(j.Records), j.Dropped, j.Pruned)
	}
}

// The check that detects a long limit wait is an event: it is never folded,
// and its marker survives only as a known value on a tick.
func TestLimitWaitReportMarkerIsKeptOutOfCollapsedRuns(t *testing.T) {
	m := newMemJournal()
	for i, mark := range []string{"", "", "", "requested", "", "", "PRIVATE_MARKER", ""} {
		r := retentionTick(i)
		r.Cause, r.LimitWaitReport = "limit_failover_disabled", mark
		m.add(t, r)
	}
	j := m.flush(t)
	var got []uint64
	for _, r := range j.Records {
		got = append(got, r.Sequence)
	}
	// 1..3 collapse to 1,3; 4 is the report; 5..8 collapse to 5,8 (the unknown marker is not a report).
	if len(got) != 5 || got[0] != 1 || got[1] != 3 || got[2] != 4 || got[3] != 5 || got[4] != 8 {
		t.Fatalf("%v", got)
	}
	mark := j.Records[2]
	if mark.LimitWaitReport != "requested" || mark.Tier != TierCondition || repeatable(mark) || mark.Collapsed != nil || j.Records[4].LimitWaitReport != "none" {
		t.Fatalf("%+v", mark)
	}
	c := clean(Record{Kind: "control", CheckStartedAt: retentionStart, LimitWaitReport: "requested", ControlBefore: &Controls{}, ControlAfter: &Controls{}, ControlChange: &ControlChange{Operation: "pause", SaveObservedAt: retentionStart}})
	if c.LimitWaitReport != "none" {
		t.Fatal("a control save carried a report marker")
	}
}

// A schema-6 journal is already tiered and collapsed. Upgrading it keeps its
// accounting exactly and cannot claim a report an older build never made.
func TestSchemaSixUpgradeKeepsAccounting(t *testing.T) {
	m := newMemJournal()
	for i := 0; i < 300; i++ {
		r := retentionTick(i)
		if i >= 20 {
			r.Observation.Queued = 1 + i%2
		}
		m.add(t, r)
	}
	m.j.SchemaVersion = 6
	m.j.Records[0].LimitWaitReport = "requested"
	dir := t.TempDir()
	b, _ := json.Marshal(m.j)
	if err := os.WriteFile(filepath.Join(dir, FileName), b, 0600); err != nil {
		t.Fatal(err)
	}
	old := loadAccounted(t, dir)
	if old.SchemaVersion != 6 || old.Collapsed != 18 || old.Dropped == 0 || old.Pruned.Routine.Count != old.Dropped || old.Records[0].LimitWaitReport != "none" {
		t.Fatalf("schema %d collapsed %d dropped %d", old.SchemaVersion, old.Collapsed, old.Dropped)
	}
	r := retentionTick(300)
	r.Action = "wake"
	mustAppend(t, dir, r)
	j := loadAccounted(t, dir)
	if j.SchemaVersion != SchemaVersion || j.Collapsed != old.Collapsed || j.Dropped != old.Dropped+1 || j.Pruned.Routine.Count != old.Pruned.Routine.Count+1 || j.NextSequence != old.NextSequence+1 {
		t.Fatalf("schema %d collapsed %d dropped %d pruned %+v", j.SchemaVersion, j.Collapsed, j.Dropped, j.Pruned)
	}
}

// The turn-retry marker is a fixed vocabulary on ticks only, and a journal
// written before it existed cannot claim one.
func TestTurnRetryMarkerIsTypedAndAbsentFromOlderJournals(t *testing.T) {
	for in, want := range map[string]string{"retried": "retried", "queued": "queued", "nothing_to_retry": "nothing_to_retry", "already_queued": "already_queued", "refused": "refused", "unsupported": "unsupported", "": "none", "PRIVATE_MARKER": "none"} {
		r := retentionTick(0)
		r.Action, r.TurnRetry = "reprompt", in
		if got := clean(r); got.TurnRetry != want || repeatable(got) {
			t.Errorf("%q: %s", in, got.TurnRetry)
		}
	}
	c := clean(Record{Kind: "control", CheckStartedAt: retentionStart, TurnRetry: "retried", ControlBefore: &Controls{}, ControlAfter: &Controls{}, ControlChange: &ControlChange{Operation: "pause", SaveObservedAt: retentionStart}})
	if c.TurnRetry != "none" {
		t.Fatal("a control save carried a turn-retry marker")
	}
	dir := t.TempDir()
	r := retentionTick(0)
	r.Action, r.TurnRetry, r.Sequence = "reprompt", "retried", 1
	b, _ := json.Marshal(Journal{SchemaVersion: 7, NextSequence: 2, Records: []Record{r}})
	if err := os.WriteFile(filepath.Join(dir, FileName), b, 0600); err != nil {
		t.Fatal(err)
	}
	j, err := Load(dir)
	if err != nil || j.SchemaVersion != 7 || j.Records[0].TurnRetry != "none" {
		t.Fatalf("%+v %v", j.Records, err)
	}
	next := retentionTick(1)
	next.Action, next.TurnRetry = "reprompt", "queued"
	mustAppend(t, dir, next)
	if j = loadAccounted(t, dir); j.SchemaVersion != SchemaVersion || j.Records[0].TurnRetry != "none" || j.Records[1].TurnRetry != "queued" {
		t.Fatalf("%+v", j.Records)
	}
}
