package incident

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var retentionStart = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func retentionTick(n int) Record {
	on := &Controls{Enabled: true}
	return Record{Kind: "tick", Build: "v0.1.5", Adapter: "bb", TargetKey: TargetKey("primary"), TargetKeyAfter: TargetKey("primary"), CheckStartedAt: retentionStart.Add(time.Duration(n) * 10 * time.Minute),
		Action: "none", Outcome: "succeeded", Cause: "active", ControlBefore: on, ControlAfter: on, Observation: Observation{Availability: "observed", State: "active"},
		Decisions: Decisions{Availability: "captured"}, Probes: Probes{Availability: "captured"}}
}

func mustAppend(t *testing.T, dir string, r Record) {
	t.Helper()
	if err := Append(dir, r); err != nil {
		t.Fatal(err)
	}
}

// Every allocated sequence is retained, pruned or collapsed, and the file
// stays inside its stated bounds.
func loadAccounted(t *testing.T, dir string) Journal {
	t.Helper()
	j, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if j.NextSequence-1 != uint64(len(j.Records))+j.Dropped+j.Collapsed || len(j.Records) > MaxRecords {
		t.Fatalf("unaccounted sequences: next %d retained %d dropped %d collapsed %d", j.NextSequence, len(j.Records), j.Dropped, j.Collapsed)
	}
	counts := map[string]int{}
	for _, r := range j.Records {
		counts[r.Tier]++
	}
	if counts[TierKey] > MaxKeyRecords || counts[TierActivity] > MaxActivityRecords || counts[TierRoutine] > MaxRoutineRecords {
		t.Fatalf("tier cap exceeded: %v", counts)
	}
	if info, err := os.Stat(filepath.Join(dir, FileName)); err == nil && info.Size() > MaxBytes {
		t.Fatalf("file is %d bytes", info.Size())
	}
	return j
}

func TestRetentionTiers(t *testing.T) {
	on, held := &Controls{Enabled: true}, &Controls{Enabled: true, AwaitingUser: true}
	tick := retentionTick(0)
	with := func(change func(*Record)) Record {
		r := tick
		change(&r)
		return clean(r)
	}
	control := func(op string, before, after *Controls, owner bool) Record {
		return clean(Record{Kind: "control", CheckStartedAt: retentionStart, ControlBefore: before, ControlAfter: after,
			ControlChange: &ControlChange{Operation: op, SaveObservedAt: retentionStart, OwnershipChanged: owner}})
	}
	var failed, passed Probes
	failed.Add("fictional-candidate", "scheduled", false, "auth")
	passed.Add("fictional-candidate", "scheduled", true, "")
	for name, tc := range map[string]struct {
		r          Record
		tier       string
		repeatable bool
	}{
		"nothing happened":         {clean(tick), TierRoutine, true},
		"held check":               {with(func(r *Record) { r.ControlBefore, r.ControlAfter, r.Cause = held, held, "awaiting_user" }), TierRoutine, true},
		"passing probe":            {with(func(r *Record) { r.Probes = passed }), TierRoutine, true},
		"dry run":                  {with(func(r *Record) { r.DryRun, r.Outcome, r.Action = true, "dry_run", "wake" }), TierRoutine, true},
		"legacy check":             {with(func(r *Record) { r.TargetKeyAfter, r.Probes = "", Probes{} }), TierRoutine, true},
		"limit wait":               {with(func(r *Record) { r.Cause = "limit_failover_disabled" }), TierKey, true},
		"dry-run limit wait":       {with(func(r *Record) { r.Cause, r.DryRun, r.Outcome = "limit_wait", true, "dry_run" }), TierRoutine, true},
		"failed observation":       {with(func(r *Record) { r.Outcome, r.Observation = "failed", Observation{Availability: "unavailable"} }), TierActivity, true},
		"wake":                     {with(func(r *Record) { r.Action = "wake" }), TierActivity, false},
		"reprompt":                 {with(func(r *Record) { r.Action = "reprompt" }), TierActivity, false},
		"stop":                     {with(func(r *Record) { r.Action = "stop" }), TierActivity, false},
		"hold entered":             {with(func(r *Record) { r.Action, r.ControlAfter = "await_user", held }), TierKey, false},
		"hold cleared during tick": {with(func(r *Record) { r.ControlBefore = held }), TierKey, false},
		"handoff":                  {with(func(r *Record) { r.Action, r.TargetKeyAfter = "handoff", TargetKey("fictional-fallback") }), TierKey, false},
		"failed handoff":           {with(func(r *Record) { r.Action, r.Outcome = "handoff", "failed" }), TierKey, false},
		"switch back":              {with(func(r *Record) { r.Action, r.TargetKey = "switch_back", TargetKey("fictional-fallback") }), TierKey, false},
		"failed probe":             {with(func(r *Record) { r.Probes = failed }), TierKey, false},
		"retarget":                 {clean(Record{Kind: "retarget", CheckStartedAt: retentionStart, TargetKey: TargetKey("primary"), TargetKeyAfter: TargetKey("fictional-fallback")}), TierKey, false},
		"pause":                    {control("pause", on, &Controls{Enabled: true, Paused: true}, false), TierKey, false},
		"resume":                   {control("resume", held, on, false), TierKey, false},
		"repeated pause":           {control("pause", held, held, false), TierActivity, false},
		"ownership change":         {control("enable", on, on, true), TierKey, false},
		"daemon takeover":          {control("daemon_takeover", on, on, false), TierKey, false},
		"unknown kind":             {clean(Record{Kind: "PRIVATE_KIND", CheckStartedAt: retentionStart}), TierActivity, false},
	} {
		if tc.r.Tier != tc.tier || repeatable(tc.r) != tc.repeatable {
			t.Errorf("%s: tier %s repeatable %t, want %s %t", name, tc.r.Tier, repeatable(tc.r), tc.tier, tc.repeatable)
		}
	}
	// A stored tier is recomputed, never trusted.
	if r := with(func(r *Record) { r.Tier = TierKey }); r.Tier != TierRoutine {
		t.Fatal(r.Tier)
	}
}

// A five-day limit wait at ten-minute checks is 720 identical checks. It
// survives as its first and latest check, and outlives later routine history.
func TestLimitWaitSurvivesAsFirstAndLatestCheck(t *testing.T) {
	dir := t.TempDir()
	mustAppend(t, dir, Record{Kind: "retarget", CheckStartedAt: retentionStart.Add(-time.Hour), TargetKey: TargetKey("fictional-fallback"), TargetKeyAfter: TargetKey("primary")})
	const checks = 720
	for i := 0; i < checks; i++ {
		r := retentionTick(i)
		r.Cause, r.Observation = "limit_failover_disabled", Observation{Availability: "observed", State: "error", ErrorClass: "rate_limit"}
		// The reset interval shrinks on every check and must not break the run.
		r.Decisions.Evaluations = []Evaluation{{Stage: "normal", Action: "none", Cause: "limit_failover_disabled", Input: DecisionInput{ResetIn: time.Duration(checks-i) * 10 * time.Minute}}}
		if i%24 == 12 {
			r.Probes.Add("fictional-candidate", "scheduled", true, "")
		}
		// A caller cannot plant a summary.
		r.Collapsed = &Collapsed{Count: 99, FirstAt: retentionStart, LastAt: retentionStart}
		mustAppend(t, dir, r)
	}
	j := loadAccounted(t, dir)
	if len(j.Records) != 3 || j.Dropped != 0 || j.Collapsed != checks-2 {
		t.Fatalf("%d records, dropped %d, collapsed %d", len(j.Records), j.Dropped, j.Collapsed)
	}
	first, latest := j.Records[1], j.Records[2]
	if first.Sequence != 2 || first.Collapsed != nil || first.Tier != TierKey || !first.CheckStartedAt.Equal(retentionStart) || first.Decisions.Evaluations[0].Input.ResetIn != 120*time.Hour {
		t.Fatalf("%+v", first)
	}
	c := latest.Collapsed
	if latest.Sequence != checks+1 || latest.Tier != TierKey || c == nil || c.Count != checks-2 || !c.FirstAt.Equal(retentionStart.Add(10*time.Minute)) || !c.LastAt.Equal(retentionStart.Add((checks-2)*10*time.Minute)) || c.PassedProbes != 30 {
		t.Fatalf("%+v %+v", latest, c)
	}
	if latest.Decisions.Evaluations[0].Input.ResetIn != 10*time.Minute {
		t.Fatalf("latest check lost its own decision input: %+v", latest.Decisions)
	}
	// Weeks of busy later history: alternating routine checks that cannot
	// collapse, and a wake every third check.
	for i := 0; i < 1000; i++ {
		r := retentionTick(checks + i)
		switch i % 3 {
		case 0:
			r.Observation.State, r.Cause = "idle", "idle"
		case 1:
			r.Action, r.Cause = "wake", "idle"
		}
		mustAppend(t, dir, r)
	}
	j = loadAccounted(t, dir)
	if j.Records[0].Kind != "retarget" || j.Records[1].Sequence != 2 || j.Records[2].Sequence != checks+1 || j.Records[2].Collapsed.Count != checks-2 {
		t.Fatalf("key events were evicted by routine history: %+v", j.Records[:3])
	}
	if j.Pruned.Key.Count != 0 || j.Pruned.Routine.Count == 0 || j.Pruned.Activity.Count == 0 || j.Pruned.Routine.NewestAt == nil || j.Pruned.Activity.NewestAt == nil {
		t.Fatalf("%+v", j.Pruned)
	}
	if j.Dropped != j.Pruned.Routine.Count+j.Pruned.Activity.Count || len(j.Records) != 3+MaxActivityRecords+MaxRoutineRecords {
		t.Fatalf("dropped %d pruned %+v retained %d", j.Dropped, j.Pruned, len(j.Records))
	}
	// The pruning horizon is the newest pruned record of that tier, and
	// everything retained in the tier is newer.
	for _, r := range j.Records {
		if r.Tier == TierRoutine && !r.CheckStartedAt.After(*j.Pruned.Routine.NewestAt) {
			t.Fatalf("routine record %d is older than the pruning horizon", r.Sequence)
		}
	}
}

func TestCollapseKeepsEveryChangeAndEveryRunBoundary(t *testing.T) {
	dir := t.TempDir()
	on, paused := &Controls{Enabled: true}, &Controls{Enabled: true, Paused: true}
	n := 0
	add := func(change func(*Record)) {
		r := retentionTick(n)
		n++
		if change != nil {
			change(&r)
		}
		mustAppend(t, dir, r)
	}
	for i := 0; i < 5; i++ {
		add(nil) // 1-5: one run
	}
	add(func(r *Record) { r.Action = "nudge" }) // 6: a send is never folded
	for i := 0; i < 4; i++ {
		add(nil) // 7-10: a new run, although identical to the first
	}
	mustAppend(t, dir, Record{Kind: "control", CheckStartedAt: retentionStart, ControlBefore: on, ControlAfter: paused, ControlChange: &ControlChange{Operation: "pause", SaveObservedAt: retentionStart}}) // 11
	for i := 0; i < 3; i++ {
		add(func(r *Record) {
			r.ControlBefore, r.ControlAfter, r.Cause, r.Observation = paused, paused, "paused", Observation{Availability: "not_observed"}
		}) // 12-14
	}
	add(func(r *Record) { r.Build = "v0.1.6" }) // 15: a new build breaks a run
	add(func(r *Record) { r.Build = "v0.1.6" }) // 16: two checks are both kept
	j := loadAccounted(t, dir)
	var got []uint64
	var folded []uint64
	for _, r := range j.Records {
		got = append(got, r.Sequence)
		if r.Collapsed != nil {
			folded = append(folded, r.Collapsed.Count)
		} else {
			folded = append(folded, 0)
		}
	}
	want, wantFolded := []uint64{1, 5, 6, 7, 10, 11, 12, 14, 15, 16}, []uint64{0, 3, 0, 0, 2, 0, 0, 1, 0, 0}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] || folded[i] != wantFolded[i] {
			t.Fatalf("sequences %v folded %v", got, folded)
		}
	}
	if j.Collapsed != 6 || j.Dropped != 0 {
		t.Fatalf("collapsed %d dropped %d", j.Collapsed, j.Dropped)
	}
}

func TestTierCapsPruneOldestWithinTheTier(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < MaxKeyRecords+5; i++ {
		// Alternate direction: each retarget is a distinct key event.
		from, to := "primary", "fictional-fallback"
		if i%2 == 1 {
			from, to = to, from
		}
		mustAppend(t, dir, Record{Kind: "retarget", CheckStartedAt: retentionStart.Add(time.Duration(i) * time.Minute), TargetKey: TargetKey(from), TargetKeyAfter: TargetKey(to)})
	}
	j := loadAccounted(t, dir)
	if len(j.Records) != MaxKeyRecords || j.Records[0].Sequence != 6 || j.Pruned.Key.Count != 5 || !j.Pruned.Key.NewestAt.Equal(retentionStart.Add(4*time.Minute)) || j.Pruned.Routine.Count != 0 || j.Dropped != 5 {
		t.Fatalf("%d %+v", len(j.Records), j.Pruned)
	}
}

func TestLegacyJournalIsCollapsedOnUpgradeNotPrunedInBulk(t *testing.T) {
	dir := t.TempDir()
	old := Journal{SchemaVersion: 5, NextSequence: 300, Dropped: 43, Records: []Record{}}
	for i := 0; i < 256; i++ {
		r := retentionTick(i)
		if i == 100 {
			r.Action, r.ControlAfter = "await_user", &Controls{Enabled: true, AwaitingUser: true}
		}
		if i > 100 {
			held := &Controls{Enabled: true, AwaitingUser: true}
			r.ControlBefore, r.ControlAfter, r.Cause, r.Observation = held, held, "awaiting_user", Observation{Availability: "not_observed"}
		}
		r.Sequence = uint64(44 + i)
		old.Records = append(old.Records, r)
	}
	b, _ := json.Marshal(old)
	// A schema-5 writer produced none of the retention fields.
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "collapsed")
	delete(raw, "pruned")
	for _, rec := range raw["records"].([]any) {
		delete(rec.(map[string]any), "tier")
		delete(rec.(map[string]any), "collapsed")
	}
	b, _ = json.Marshal(raw)
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	j, err := Load(dir)
	if err != nil || j.SchemaVersion != 5 || len(j.Records) != 256 || j.Collapsed != 0 || j.Records[0].Tier != TierRoutine || j.Records[100].Tier != TierKey {
		t.Fatal(j.SchemaVersion, len(j.Records), err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(b) {
		t.Fatal("read mutated the legacy journal")
	}
	r := retentionTick(256)
	r.Action = "wake"
	mustAppend(t, dir, r)
	j = loadAccounted(t, dir)
	// first..latest of the active run, the hold, first..latest of the held run, the wake.
	if j.SchemaVersion != 6 || j.Dropped != 43 || j.Pruned != (Pruned{}) || len(j.Records) != 6 || j.Collapsed != 98+153 || j.NextSequence != 301 {
		t.Fatalf("schema %d dropped %d pruned %+v retained %d collapsed %d", j.SchemaVersion, j.Dropped, j.Pruned, len(j.Records), j.Collapsed)
	}
	for i, want := range []uint64{44, 143, 144, 145, 299, 300} {
		if j.Records[i].Sequence != want {
			t.Fatalf("record %d has sequence %d, want %d", i, j.Records[i].Sequence, want)
		}
	}
	if j.Records[2].Action != "await_user" || j.Records[1].Collapsed.Count != 98 || j.Records[4].Collapsed.Count != 153 {
		t.Fatalf("%+v", j.Records)
	}
}

func TestInconsistentRetentionAccountingIsRefused(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 6; i++ {
		mustAppend(t, dir, retentionTick(i))
	}
	good := loadAccounted(t, dir)
	path := filepath.Join(dir, FileName)
	for name, tamper := range map[string]func(*Journal){
		"lost collapsed count":      func(j *Journal) { j.Collapsed = 0 },
		"summary exceeds the total": func(j *Journal) { j.Records[1].Collapsed.Count, j.Collapsed, j.NextSequence = 9, 4, 7 },
		"empty summary":             func(j *Journal) { j.Records[1].Collapsed.Count = 0 },
		"sequence out of order":     func(j *Journal) { j.Records[0].Sequence = 6 },
		"sequence beyond the next":  func(j *Journal) { j.Records[1].Sequence = 7 },
		"pruned exceeds dropped":    func(j *Journal) { j.Pruned.Key.Count = 1 },
		"collapsed before schema 6": func(j *Journal) { j.SchemaVersion = 5 },
		"summary on a control save": func(j *Journal) { j.Records[1].Kind = "control" },
	} {
		j := good
		j.Records = append([]Record{}, good.Records...)
		c := *good.Records[1].Collapsed
		j.Records[1].Collapsed = &c
		tamper(&j)
		b, _ := json.Marshal(j)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err != ErrUnavailable {
			t.Errorf("%s was accepted", name)
		}
		if Append(dir, retentionTick(9)) != ErrUnavailable {
			t.Errorf("%s was overwritten", name)
		}
	}
}
