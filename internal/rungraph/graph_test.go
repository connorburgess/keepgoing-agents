package rungraph

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
)

var at = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const (
	goal         = "goal_66666666666666666666666666666666"
	registration = "loop_77777777777777777777777777777777"
)

func id(prefix string, n int) string { return fmt.Sprintf("%s%032x", prefix, n) }

var enabled = Gate{Enabled: true}

// update applies fn through the store and fails the test on any error.
func update(t *testing.T, dir string, fn func(*Graph) (bool, error)) {
	t.Helper()
	if err := Update(dir, at, fn); err != nil {
		t.Fatal(err)
	}
}

func sync(t *testing.T, dir string, gate Gate, operation string, run int) {
	t.Helper()
	update(t, dir, func(g *Graph) (bool, error) {
		return g.Sync(Boundary{Gate: gate, Operation: operation, At: at, NewRunID: id("run_", run)})
	})
}

func load(t *testing.T, dir string) Graph {
	t.Helper()
	g, availability := Load(dir)
	if availability != Available {
		t.Fatalf("graph is %s", availability)
	}
	return g
}

func session(target, provider, session string, source Source) Link {
	return Link{Kind: rd.SessionLink, Target: target, Provider: provider, Session: session, Source: source, RecordedAt: at}
}

func TestLegacyStateHasNoGraphAndUnchangedUpdatesCreateNone(t *testing.T) {
	dir := t.TempDir()
	if g, availability := Load(dir); availability != Missing || g.LoopID != "" {
		t.Fatalf("%s %#v", availability, g)
	}
	// A disabled or held legacy loop has no run to record.
	for _, gate := range []Gate{{}, {Enabled: true, Paused: true}, {Enabled: true, Stop: true}, {Enabled: true, AwaitingUser: true}, {Enabled: true, PendingTransfer: true}} {
		sync(t, dir, gate, "", 1)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatal("an unchanged update wrote a graph")
	}
	var empty Graph
	if n, status := empty.Lookup(id("run_", 1)); status != NotRecorded || n.Ref != "" {
		t.Fatal(status)
	}
	if _, status := empty.Current(); status != NotRecorded {
		t.Fatal(status)
	}
}

func TestRunBoundariesFollowSavedControlState(t *testing.T) {
	dir := t.TempDir()
	sync(t, dir, enabled, "enable", 1)
	g := load(t, dir)
	loop := g.LoopID
	if !strings.HasPrefix(loop, "supervision_") || len(loop) != 44 || g.CurrentRun != id("run_", 1) || g.Runs[0].Origin != rd.Explicit || g.Runs[0].Source != SourceControl {
		t.Fatalf("%#v", g)
	}
	// Holds, repeated enables and later checks keep the run.
	for _, step := range []struct {
		gate Gate
		op   string
	}{{enabled, "enable"}, {enabled, "start"}, {Gate{Enabled: true, Paused: true}, "pause"}, {Gate{Enabled: true, Stop: true}, "stop"},
		{Gate{}, "disable"}, {enabled, "resume"}, {Gate{Enabled: true, AwaitingUser: true}, ""}, {enabled, ""}} {
		sync(t, dir, step.gate, step.op, 2)
	}
	if g = load(t, dir); g.LoopID != loop || g.CurrentRun != id("run_", 1) || len(g.Runs) != 1 || g.Completed {
		t.Fatalf("a non-boundary operation changed the run: %#v", g)
	}
	// Completion closes it once; a boundary whose command-time write was lost
	// is recorded by the next check with its own source.
	sync(t, dir, Gate{Enabled: true, Completed: true}, "", 2)
	sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 2)
	g = load(t, dir)
	if !g.Completed || g.Runs[0].ClosedAt.IsZero() || g.Runs[0].ClosedSource != SourceReconciled || g.CurrentRun != id("run_", 1) {
		t.Fatalf("%#v", g)
	}
	// Enable does not reopen completed work; clearing completion does.
	sync(t, dir, Gate{Enabled: true, Completed: true}, "enable", 2)
	if g = load(t, dir); len(g.Runs) != 1 {
		t.Fatal("enable reopened a completed run")
	}
	sync(t, dir, enabled, "resume", 2)
	g = load(t, dir)
	if g.Completed || g.CurrentRun != id("run_", 2) || g.Runs[1].Origin != rd.Reopened || g.Runs[1].Reopens != id("run_", 1) || g.LoopID != loop {
		t.Fatalf("%#v", g)
	}
}

func TestAdoptionCompletionWithoutARunAndRoutineInvocation(t *testing.T) {
	dir := t.TempDir()
	// An enabled legacy loop with no hold is adopted by a later check.
	sync(t, dir, enabled, "", 1)
	if g := load(t, dir); g.Runs[0].Origin != rd.Adopted || g.Runs[0].Source != SourceReconciled {
		t.Fatalf("%#v", g.Runs)
	}
	// A legacy loop already completed records completion without inventing a run.
	dir = t.TempDir()
	sync(t, dir, Gate{Enabled: true, Completed: true}, "", 1)
	g := load(t, dir)
	if !g.Completed || g.CurrentRun != "" || len(g.Runs) != 0 {
		t.Fatalf("%#v", g)
	}
	routine := rd.RoutineRef{Source: "example-scheduler", Routine: "routine-nightly", Invocation: "invocation-0001"}
	update(t, dir, func(g *Graph) (bool, error) {
		return g.Sync(Boundary{Gate: enabled, Operation: "resume", At: at, NewRunID: id("run_", 1), Routine: routine})
	})
	g = load(t, dir)
	if g.Runs[0].Origin != rd.Reopened || g.Runs[0].Reopens != "" || routineRef(g.Runs[0].Routine) != routine {
		t.Fatalf("%#v", g.Runs)
	}
	n, status := g.Lookup(InvocationRef(routine))
	if status != Found || n.Type != InvocationNode || len(n.Edges) != 2 || n.Edges[0].To != id("run_", 1) || n.Edges[1].Type != EdgeRoutine || n.Edges[1].To != RoutineNodeRef(routine) {
		t.Fatalf("%s %#v", status, n)
	}
	// A malformed supplied identity is refused and nothing is written.
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	bad := rd.RoutineRef{Source: "example-scheduler", Routine: "has space"}
	err := Update(t.TempDir(), at, func(g *Graph) (bool, error) {
		return g.Sync(Boundary{Gate: enabled, Operation: "start", At: at, NewRunID: id("run_", 1), Routine: bad})
	})
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !errors.Is(err, rd.ErrInvalid) || !bytes.Equal(before, after) {
		t.Fatal(err)
	}
}

func TestRunReferenceSurvivesReconstructionAndAttemptsStayDistinct(t *testing.T) {
	dir := t.TempDir()
	sync(t, dir, enabled, "start", 1)
	first := Attempt{ID: id("attempt_", 1), Target: "primary", Action: "wake", Mode: "auto", GoalID: goal, GoalRevision: 2, RecordedAt: at}
	update(t, dir, func(g *Graph) (bool, error) { return true, g.Begin(first) })
	// Everything below reads only the directory, as a new process would. The
	// first intent has no recorded return: it stays unconfirmed.
	g := load(t, dir)
	if g.CurrentRun != id("run_", 1) || len(g.Attempts) != 1 || g.Attempts[0].Outcome != rd.Intent || !g.Attempts[0].ReturnedAt.IsZero() || g.Attempts[0].Run != id("run_", 1) {
		t.Fatalf("%#v", g)
	}
	sync(t, dir, enabled, "", 2) // the restarted supervisor's first check
	if err := Update(dir, at, func(g *Graph) (bool, error) { return true, g.Begin(first) }); !errors.Is(err, rd.ErrInvalid) {
		t.Fatal("a retained attempt ID was reused after reconstruction")
	}
	second := first
	second.ID = id("attempt_", 2)
	update(t, dir, func(g *Graph) (bool, error) { return true, g.Begin(second) })
	update(t, dir, func(g *Graph) (bool, error) { return g.Finish(second.ID, rd.AdapterReturned, at.Add(time.Second)) })
	update(t, dir, func(g *Graph) (bool, error) { return g.Finish(second.ID, rd.AdapterReturned, at.Add(time.Hour)) })
	g = load(t, dir)
	if g.CurrentRun != id("run_", 1) || len(g.Runs) != 1 || len(g.Attempts) != 2 || g.Attempts[0].Outcome != rd.Intent ||
		g.Attempts[1].Outcome != rd.AdapterReturned || !g.Attempts[1].ReturnedAt.Equal(at.Add(time.Second)) || g.Attempts[1].Run != g.Attempts[0].Run {
		t.Fatalf("%#v", g)
	}
	for name, fn := range map[string]func(*Graph) (bool, error){
		"conflicting return": func(g *Graph) (bool, error) { return g.Finish(second.ID, rd.AdapterError, at) },
		"unknown attempt":    func(g *Graph) (bool, error) { return g.Finish(id("attempt_", 9), rd.AdapterReturned, at) },
		"unsupported action": func(g *Graph) (bool, error) {
			return true, g.Begin(Attempt{ID: id("attempt_", 3), Target: "primary", Action: "observe", Mode: "auto", RecordedAt: at})
		},
		"missing target": func(g *Graph) (bool, error) {
			return true, g.Begin(Attempt{ID: id("attempt_", 3), Action: "wake", Mode: "auto", RecordedAt: at})
		},
	} {
		if err := Update(dir, at, fn); !errors.Is(err, rd.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A send intent needs an open run.
	sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 2)
	third := first
	third.ID = id("attempt_", 3)
	if err := Update(dir, at, func(g *Graph) (bool, error) { return true, g.Begin(third) }); !errors.Is(err, rd.ErrInvalid) {
		t.Fatal("an intent was recorded for a closed run")
	}
	// A late return for the closed run is still recorded and reopens nothing.
	update(t, dir, func(g *Graph) (bool, error) { return g.Finish(first.ID, rd.AdapterError, at) })
	if g = load(t, dir); !g.Completed || g.Attempts[0].Outcome != rd.AdapterError || len(g.Runs) != 1 {
		t.Fatalf("%#v", g)
	}
}

func TestLookupResolvesRecordedReferencesDirectly(t *testing.T) {
	dir := t.TempDir()
	sync(t, dir, enabled, "start", 1)
	attempt := Attempt{ID: id("attempt_", 1), Target: "primary", Action: "wake", Mode: "auto", GoalID: goal, GoalRevision: 1, RecordedAt: at}
	update(t, dir, func(g *Graph) (bool, error) {
		for _, l := range []Link{
			session("primary", "bb", "thr_example_a", SourceConfigured),
			session("example-fallback", "claude", "11111111-2222-3333-4444-555555555555", SourceRunnerFile),
			session("primary", "bb", "thr_example_a", SourceConfigured), // repeated checks add nothing
			{Kind: rd.GoalLink, GoalID: goal, GoalRevision: 1, Source: SourceGoal, RecordedAt: at},
			{Kind: rd.GoalLink, GoalID: goal, GoalRevision: 2, Source: SourceGoal, RecordedAt: at.Add(time.Minute)},
		} {
			if _, err := g.Relate(l); err != nil {
				return false, err
			}
		}
		if _, err := g.Register(registration, SourceRegistered, at); err != nil {
			return false, err
		}
		return true, g.Begin(attempt)
	})
	sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 2)
	sync(t, dir, enabled, "resume", 2)
	update(t, dir, func(g *Graph) (bool, error) {
		return g.Relate(session("primary", "bb", "thr_example_a", SourceConfigured))
	})

	g := load(t, dir)
	refs := func(nodes []Node) string {
		var out []string
		for _, n := range nodes {
			out = append(out, n.Ref)
		}
		return strings.Join(out, " ")
	}
	chatA, chatB := SessionRef("bb", "thr_example_a"), SessionRef("claude", "11111111-2222-3333-4444-555555555555")
	// loop → current run → chat/session
	loop, status := g.Lookup(g.LoopID)
	if status != Found || loop.Type != LoopNode {
		t.Fatal(status)
	}
	if got := refs(g.Follow(loop, EdgeCurrentRun)); got != id("run_", 2) {
		t.Fatalf("current run: %q", got)
	}
	current, status := g.Current()
	if status != Found || current.Run == nil || current.Run.ID != id("run_", 2) || !current.Run.ClosedAt.IsZero() || current.Source != SourceControl || !current.RecordedAt.Equal(at) {
		t.Fatalf("%s %#v", status, current)
	}
	if got := refs(g.Follow(current, EdgeSession)); got != chatA {
		t.Fatalf("current run sessions: %q", got)
	}
	// One run recorded two chats; the later run reuses the first. Each edge
	// keeps its target alias and source, and nothing links chat to chat.
	earlier, _ := g.Lookup(id("run_", 1))
	if got := refs(g.Follow(earlier, EdgeSession)); got != chatA+" "+chatB {
		t.Fatalf("earlier run sessions: %q", got)
	}
	shared, _ := g.Lookup(chatA)
	if got := refs(g.Follow(shared, EdgeRun)); got != id("run_", 1)+" "+id("run_", 2) || shared.Type != SessionNode {
		t.Fatalf("runs sharing a chat: %q", got)
	}
	for _, e := range shared.Edges {
		if e.Type != EdgeRun || e.Target != "primary" || e.Source != SourceConfigured || e.RecordedAt.IsZero() {
			t.Fatalf("%#v", e)
		}
	}
	other, _ := g.Lookup(chatB)
	if other.Source != SourceRunnerFile || other.Edges[0].Target != "example-fallback" {
		t.Fatalf("%#v", other)
	}
	// run → goal revision, run → attempt → run, run → predecessor, registration → loop
	if got := refs(g.Follow(earlier, EdgeGoalRevision)); got != GoalRevisionRef(goal, 1)+" "+GoalRevisionRef(goal, 2) {
		t.Fatalf("goal revisions: %q", got)
	}
	if got := refs(g.Follow(current, EdgeGoalRevision)); got != "" {
		t.Fatalf("a goal revision was carried into the reopened run: %q", got)
	}
	sent, status := g.Lookup(attempt.ID)
	if status != Found || sent.Attempt.Outcome != rd.Intent || refs(g.Follow(sent, EdgeRun)) != id("run_", 1) || sent.Edges[1].To != GoalRevisionRef(goal, 1) {
		t.Fatalf("%s %#v", status, sent)
	}
	if got := refs(g.Follow(current, EdgeReopens)); got != id("run_", 1) {
		t.Fatalf("predecessor: %q", got)
	}
	registered, status := g.Lookup(registration)
	if status != Found || registered.Registration.Source != SourceRegistered || refs(g.Follow(registered, EdgeLoop)) != g.LoopID {
		t.Fatalf("%s %#v", status, registered)
	}
	// A reference that was never recorded stays explicit.
	for _, ref := range []string{id("run_", 9), SessionRef("bb", "thr_example_z"), "loop_88888888888888888888888888888888", ""} {
		if n, status := g.Lookup(ref); status != NotRecorded || n.Ref != "" {
			t.Fatalf("%q: %s", ref, status)
		}
	}
}

func TestLinksAreValidatedAgainstTheCurrentRun(t *testing.T) {
	dir := t.TempDir()
	for name, l := range map[string]Link{
		"no run yet": session("primary", "bb", "thr_example_a", SourceConfigured),
	} {
		if err := Update(dir, at, func(g *Graph) (bool, error) { return g.Relate(l) }); !errors.Is(err, rd.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	sync(t, dir, enabled, "start", 1)
	for name, l := range map[string]Link{
		"URL as session":      session("primary", "bb", "https://example.com/chat/1", SourceConfigured),
		"missing provider":    session("primary", "", "thr_example_a", SourceConfigured),
		"unknown source":      session("primary", "bb", "thr_example_a", "transcript"),
		"overlong target":     session(strings.Repeat("a", 129), "bb", "thr_example_a", SourceConfigured),
		"target on goal link": {Kind: rd.GoalLink, Target: "primary", GoalID: goal, GoalRevision: 1, Source: SourceGoal, RecordedAt: at},
		"goal revision bound": {Kind: rd.GoalLink, GoalID: goal, GoalRevision: 129, Source: SourceGoal, RecordedAt: at},
		"unknown kind":        {Kind: "parent", Source: SourceExternal, RecordedAt: at},
	} {
		if err := Update(dir, at, func(g *Graph) (bool, error) { return g.Relate(l) }); !errors.Is(err, rd.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	invocation := Link{Kind: rd.InvocationLink, Routine: &Routine{Source: "example-scheduler", Routine: "routine-nightly", Invocation: "invocation-0002"}, Source: SourceExternal, RecordedAt: at}
	update(t, dir, func(g *Graph) (bool, error) { return g.Relate(invocation) })
	sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 2)
	later := invocation
	later.Routine = &Routine{Source: "example-scheduler", Routine: "routine-nightly", Invocation: "invocation-0003"}
	if err := Update(dir, at, func(g *Graph) (bool, error) { return g.Relate(later) }); !errors.Is(err, rd.ErrInvalid) {
		t.Fatal("a trigger after completion was related to the closed run")
	}
	if g := load(t, dir); len(g.Links) != 1 || len(g.Runs) != 1 {
		t.Fatalf("%#v", g)
	}
	for name, ref := range map[string]string{"durable ID": id("supervision_", 1), "malformed": "loop_bad"} {
		if err := Update(dir, at, func(g *Graph) (bool, error) { return g.Register(ref, SourceRegistered, at) }); !errors.Is(err, rd.ErrInvalid) {
			t.Fatalf("%s accepted as a registry reference", name)
		}
	}
}

func TestRetentionIsBoundedAndExpiryStaysExplicit(t *testing.T) {
	dir := t.TempDir()
	sync(t, dir, enabled, "start", 0)
	for n := 1; n <= MaxRuns+2; n++ {
		update(t, dir, func(g *Graph) (bool, error) {
			if _, err := g.Relate(session("primary", "bb", fmt.Sprintf("thr_example_%d", n), SourceConfigured)); err != nil {
				return false, err
			}
			return true, g.Begin(Attempt{ID: id("attempt_", n), Target: "primary", Action: "wake", Mode: "auto", RecordedAt: at})
		})
		sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 0)
		sync(t, dir, enabled, "resume", n)
	}
	g := load(t, dir)
	if len(g.Runs) != MaxRuns || g.Dropped != (Dropped{Runs: 3, Attempts: 3, Links: 3, Unrecorded: 0}) || g.Runs[0].ID != id("run_", 3) || g.CurrentRun != id("run_", MaxRuns+2) {
		t.Fatalf("runs=%d dropped=%#v first=%s", len(g.Runs), g.Dropped, g.Runs[0].ID)
	}
	// Dropped records are not recreated, and their absence is not "never recorded".
	for _, ref := range []string{id("run_", 0), id("attempt_", 1), SessionRef("bb", "thr_example_1"), id("run_", 99)} {
		if _, status := g.Lookup(ref); status != NotRecordedOrExpired {
			t.Fatalf("%s: %s", ref, status)
		}
	}
	// The oldest retained run still names its dropped predecessor, which does not resolve.
	oldest, _ := g.Lookup(id("run_", 3))
	if oldest.Run.Reopens != id("run_", 2) || len(g.Follow(oldest, EdgeReopens)) != 0 {
		t.Fatalf("%#v", oldest.Run)
	}
	// One long run keeps its newest attempts and links only.
	for n := 0; n < MaxAttempts+MaxLinks; n++ {
		update(t, dir, func(g *Graph) (bool, error) {
			if _, err := g.Relate(session("primary", "bb", fmt.Sprintf("thr_long_%d", n), SourceConfigured)); err != nil {
				return false, err
			}
			return true, g.Begin(Attempt{ID: id("attempt_", 1000+n), Target: "primary", Action: "nudge", Mode: "queue", RecordedAt: at})
		})
	}
	g = load(t, dir)
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil || info.Size() > MaxBytes || len(g.Attempts) != MaxAttempts || len(g.Links) != MaxLinks || g.CurrentRun != id("run_", MaxRuns+2) {
		t.Fatalf("size=%v attempts=%d links=%d", info, len(g.Attempts), len(g.Links))
	}
	if g.Attempts[len(g.Attempts)-1].ID != id("attempt_", 1000+MaxAttempts+MaxLinks-1) {
		t.Fatal("the newest attempt was dropped")
	}
	for n := 0; n < MaxRegistrations+3; n++ {
		update(t, dir, func(g *Graph) (bool, error) { return g.Register(id("loop_", n), SourceRegistered, at) })
	}
	if g = load(t, dir); len(g.Registrations) != MaxRegistrations || g.Registrations[0].ID != id("loop_", 3) || g.Dropped.Registrations != 3 {
		t.Fatalf("%d registrations, dropped %d", len(g.Registrations), g.Dropped.Registrations)
	}
}

func TestStoredRecordIsPrivateVersionedAndReplaceableUnderAnOpenReader(t *testing.T) {
	dir := t.TempDir()
	sync(t, dir, enabled, "start", 1)
	path := filepath.Join(dir, FileName)
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("%v %v", info, err)
	}
	b, _ := os.ReadFile(path)
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil || raw["schemaVersion"] != float64(1) || raw["attempts"] == nil || raw["links"] == nil || raw["registrations"] == nil {
		t.Fatalf("%s", b)
	}
	// A reader holding the previous snapshot does not block replacement.
	reader, err := control.OpenPrivateSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 2)
	if g := load(t, dir); !g.Completed {
		t.Fatal("replacement under an open reader was lost")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestUnusableRecordsStayExplicitAndAreNeverRewritten(t *testing.T) {
	dir := t.TempDir()
	sync(t, dir, enabled, "start", 1)
	update(t, dir, func(g *Graph) (bool, error) {
		return true, g.Begin(Attempt{ID: id("attempt_", 1), Target: "primary", Action: "wake", Mode: "auto", RecordedAt: at})
	})
	good, _ := os.ReadFile(filepath.Join(dir, FileName))
	edit := func(fn func(map[string]any)) []byte {
		var v map[string]any
		if err := json.Unmarshal(good, &v); err != nil {
			t.Fatal(err)
		}
		fn(v)
		b, _ := json.Marshal(v)
		return b
	}
	first := func(v map[string]any, key string) map[string]any { return v[key].([]any)[0].(map[string]any) }
	second := map[string]any{"id": id("run_", 2), "origin": "explicit", "recordedAt": at, "source": "control_command"}
	cases := map[string]struct {
		body []byte
		want Availability
	}{
		"newer schema":            {edit(func(v map[string]any) { v["schemaVersion"] = 2; v["future"] = true }), Unsupported},
		"schema zero":             {edit(func(v map[string]any) { v["schemaVersion"] = 0 }), Corrupt},
		"not JSON":                {[]byte("{"), Corrupt},
		"empty":                   {nil, Corrupt},
		"trailing data":           {append(append([]byte{}, good...), []byte("{}")...), Corrupt},
		"unknown field":           {edit(func(v map[string]any) { v["extra"] = 1 }), Corrupt},
		"oversized":               {bytes.Repeat([]byte(" "), MaxBytes+1), Corrupt},
		"registry ID as loop":     {edit(func(v map[string]any) { v["loopId"] = registration }), Corrupt},
		"missing update time":     {edit(func(v map[string]any) { delete(v, "updatedAt") }), Corrupt},
		"current run not stored":  {edit(func(v map[string]any) { v["currentRun"] = id("run_", 9) }), Corrupt},
		"two open runs":           {edit(func(v map[string]any) { v["runs"] = append(v["runs"].([]any), second) }), Corrupt},
		"duplicate run":           {edit(func(v map[string]any) { v["runs"] = append(v["runs"].([]any), v["runs"].([]any)[0]) }), Corrupt},
		"inferred origin":         {edit(func(v map[string]any) { first(v, "runs")["origin"] = "inferred" }), Corrupt},
		"transcript source":       {edit(func(v map[string]any) { first(v, "runs")["source"] = "transcript" }), Corrupt},
		"closed without source":   {edit(func(v map[string]any) { first(v, "runs")["closedAt"] = at; v["completed"] = true }), Corrupt},
		"completed but run open":  {edit(func(v map[string]any) { v["completed"] = true }), Corrupt},
		"attempt for unknown run": {edit(func(v map[string]any) { first(v, "attempts")["run"] = id("run_", 9) }), Corrupt},
		"delivered outcome":       {edit(func(v map[string]any) { first(v, "attempts")["outcome"] = "delivered" }), Corrupt},
		"duplicate attempt":       {edit(func(v map[string]any) { v["attempts"] = append(v["attempts"].([]any), v["attempts"].([]any)[0]) }), Corrupt},
		"link for unknown run": {edit(func(v map[string]any) {
			v["links"] = []any{map[string]any{"run": id("run_", 9), "kind": "session", "provider": "bb", "session": "thr_example_a", "source": "configured_target", "recordedAt": at}}
		}), Corrupt},
		"link with two payloads": {edit(func(v map[string]any) {
			v["links"] = []any{map[string]any{"run": id("run_", 1), "kind": "session", "provider": "bb", "session": "thr_example_a", "goalId": goal, "goalRevision": 1, "source": "configured_target", "recordedAt": at}}
		}), Corrupt},
		"skip for unknown run": {edit(func(v map[string]any) {
			v["unrecorded"] = []any{map[string]any{"run": id("run_", 9), "target": "primary", "reason": ReasonSessionID, "recordedAt": at}}
		}), Corrupt},
		"skip with unknown reason": {edit(func(v map[string]any) {
			v["unrecorded"] = []any{map[string]any{"run": id("run_", 1), "target": "primary", "reason": "guessed", "recordedAt": at}}
		}), Corrupt},
		"attempt ID reused as run": {edit(func(v map[string]any) { first(v, "attempts")["run"] = id("attempt_", 1) }), Corrupt},
		"more triggers than the bound": {edit(func(v map[string]any) {
			var links []any
			for n := 0; n <= MaxTriggersPerRun; n++ {
				links = append(links, map[string]any{"run": id("run_", 1), "kind": "invocation", "routine": map[string]any{"source": "example", "routine": "nightly", "invocation": fmt.Sprintf("inv-%d", n)}, "source": "external", "recordedAt": at})
			}
			v["links"] = links
		}), Corrupt},
		"registration with an unrelated source": {edit(func(v map[string]any) {
			v["registrations"] = []any{map[string]any{"id": registration, "recordedAt": at, "source": "goal_record"}}
		}), Corrupt},
		"malformed registration": {edit(func(v map[string]any) {
			v["registrations"] = []any{map[string]any{"id": "loop_bad", "recordedAt": at, "source": "registered_control"}}
		}), Corrupt},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			if err := os.WriteFile(path, tt.body, 0o600); err != nil {
				t.Fatal(err)
			}
			g, availability := Load(dir)
			if availability != tt.want || g.LoopID != "" || len(g.Runs) != 0 {
				t.Fatalf("%s %#v", availability, g)
			}
			err := Update(dir, at, func(g *Graph) (bool, error) {
				return g.Sync(Boundary{Gate: enabled, Operation: "start", At: at, NewRunID: id("run_", 5)})
			})
			after, _ := os.ReadFile(path)
			if !errors.Is(err, ErrUnavailable) || !bytes.Equal(after, tt.body) {
				t.Fatalf("an unusable record was rewritten or accepted: %v", err)
			}
		})
	}
	// A directory or symlink in the record's place is never followed or replaced.
	aliased := t.TempDir()
	if err := os.Mkdir(filepath.Join(aliased, FileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, availability := Load(aliased); availability != Corrupt {
		t.Fatal(availability)
	}
	if err := Update(aliased, at, func(g *Graph) (bool, error) { return true, nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		linked := t.TempDir()
		if err := os.Symlink(filepath.Join(dir, FileName), filepath.Join(linked, FileName)); err != nil {
			t.Fatal(err)
		}
		if _, availability := Load(linked); availability != Corrupt {
			t.Fatal(availability)
		}
	}
}

func TestConflictWithSavedControlStaysExplicitUntilRecorded(t *testing.T) {
	dir := t.TempDir()
	var none Graph
	if none.Conflict(Gate{Completed: true}) != "" {
		t.Fatal("a missing graph cannot conflict")
	}
	sync(t, dir, enabled, "start", 1)
	g := load(t, dir)
	if g.Conflict(enabled) != "" || g.Conflict(Gate{Enabled: true, Paused: true}) != "" {
		t.Fatal("a hold is not a conflict")
	}
	// The completion was saved but its graph write was lost.
	if got := g.Conflict(Gate{Enabled: true, Completed: true}); got != ConflictCompletionNotRecorded {
		t.Fatal(got)
	}
	sync(t, dir, Gate{Enabled: true, Completed: true}, "", 2)
	g = load(t, dir)
	if g.Conflict(Gate{Enabled: true, Completed: true}) != "" || g.Conflict(enabled) != ConflictReopenNotRecorded {
		t.Fatal("conflict was not cleared by recording, or the lost reopen was not reported")
	}
}

func TestNewIDAndGate(t *testing.T) {
	a, err := NewID("run_")
	b, _ := NewID("run_")
	if err != nil || a == b || len(a) != 36 {
		t.Fatal(a, b, err)
	}
	f := control.File{Enabled: true, Paused: true, Stop: true, AwaitingUser: true, Completed: true, PendingTransfer: &control.PendingBBTransfer{}}
	if GateOf(f) != (Gate{true, true, true, true, true, true}) || GateOf(control.File{}) != (Gate{}) {
		t.Fatal("gate")
	}
}

// A dropped registry reference must not read as one that was never used.
func TestDroppedRegistryReferenceIsExpiredNotUnrecorded(t *testing.T) {
	dir := t.TempDir()
	update(t, dir, func(g *Graph) (bool, error) { return g.Register(id("loop_", 0), SourceRegistered, at) })
	g := load(t, dir)
	if _, status := g.Lookup(id("loop_", 99)); status != NotRecorded {
		t.Fatal(status)
	}
	for n := 1; n <= MaxRegistrations; n++ {
		update(t, dir, func(g *Graph) (bool, error) { return g.Register(id("loop_", n), SourceRegistered, at) })
	}
	g = load(t, dir)
	if _, status := g.Lookup(id("loop_", 0)); status != NotRecordedOrExpired || g.Dropped.Registrations != 1 {
		t.Fatalf("%s %#v", status, g.Dropped)
	}
	if _, status := g.Lookup(id("loop_", 1)); status != Found {
		t.Fatal(status)
	}
}

// Tokens may contain ":", so references join their parts with "/", which no
// token can contain.
func TestReferencesOfDifferentIdentitiesNeverCollide(t *testing.T) {
	if SessionRef("example:a", "b") == SessionRef("example", "a:b") {
		t.Fatal("session references collide")
	}
	a := rd.RoutineRef{Source: "example", Routine: "nightly:eu", Invocation: "1"}
	b := rd.RoutineRef{Source: "example", Routine: "nightly", Invocation: "eu:1"}
	if InvocationRef(a) == InvocationRef(b) {
		t.Fatal("invocation references collide")
	}
	dir := t.TempDir()
	sync(t, dir, enabled, "start", 1)
	update(t, dir, func(g *Graph) (bool, error) {
		for _, r := range []rd.RoutineRef{a, b} {
			if _, err := g.Relate(Link{Kind: rd.InvocationLink, Routine: routineOf(r), Source: SourceExternal, RecordedAt: at}); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	g := load(t, dir)
	for _, r := range []rd.RoutineRef{a, b} {
		if n, status := g.Lookup(InvocationRef(r)); status != Found || len(n.Edges) != 2 || n.Edges[1].To != RoutineNodeRef(r) {
			t.Fatalf("%s %#v", status, n)
		}
	}
}

func TestSkipsAreKeptOncePerRunAndBounded(t *testing.T) {
	dir := t.TempDir()
	if err := Update(dir, at, func(g *Graph) (bool, error) { return g.Skip("primary", ReasonSessionID, at) }); !errors.Is(err, rd.ErrInvalid) {
		t.Fatal("a skip was recorded without a run")
	}
	sync(t, dir, enabled, "start", 1)
	for name, fn := range map[string]func(*Graph) (bool, error){
		"unknown reason": func(g *Graph) (bool, error) { return g.Skip("primary", "transcript_said_so", at) },
		"missing target": func(g *Graph) (bool, error) { return g.Skip("", ReasonSessionID, at) },
		"missing time":   func(g *Graph) (bool, error) { return g.Skip("primary", ReasonSessionID, time.Time{}) },
	} {
		if err := Update(dir, at, fn); !errors.Is(err, rd.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for n := 0; n < MaxUnrecorded+2; n++ {
		for repeat := 0; repeat < 2; repeat++ {
			update(t, dir, func(g *Graph) (bool, error) { return g.Skip(fmt.Sprintf("target-%d", n), ReasonSessionID, at) })
		}
	}
	g := load(t, dir)
	run, _ := g.Current()
	if len(g.Unrecorded) != MaxUnrecorded || g.Dropped.Unrecorded != 2 || len(run.Unrecorded) != MaxUnrecorded || run.Unrecorded[0].Target != "target-2" {
		t.Fatalf("%d kept, %d dropped", len(g.Unrecorded), g.Dropped.Unrecorded)
	}
	// A skip leaves with its run.
	sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 0)
	for n := 2; n <= MaxRuns+1; n++ {
		sync(t, dir, enabled, "resume", n)
		sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 0)
	}
	if g = load(t, dir); len(g.Unrecorded) != 0 || g.Runs[0].ID == id("run_", 1) {
		t.Fatalf("%d skips outlived their run", len(g.Unrecorded))
	}
}

// Routine → runs → chats: one scheduler entry opens a run, triggers it again,
// and after completion opens the next. Triggers never open or close a run.
func TestRoutineResolvesToItsInvocationsAndRuns(t *testing.T) {
	dir := t.TempDir()
	nightly := func(invocation string) rd.RoutineRef {
		return rd.RoutineRef{Source: "example-scheduler", Routine: "nightly", Invocation: invocation}
	}
	trigger := func(r rd.RoutineRef) {
		t.Helper()
		update(t, dir, func(g *Graph) (bool, error) { return g.Trigger(r, at) })
	}
	open := func(operation string, run int, r rd.RoutineRef) {
		t.Helper()
		update(t, dir, func(g *Graph) (bool, error) {
			return g.Sync(Boundary{Gate: enabled, Operation: operation, At: at, NewRunID: id("run_", run), Routine: r})
		})
	}
	// A trigger with no run records nothing and creates no graph.
	trigger(nightly("inv-0"))
	if _, availability := Load(dir); availability != Missing {
		t.Fatal("a trigger alone created a graph")
	}
	open("enable", 1, nightly("inv-1"))
	trigger(nightly("inv-1")) // the opening invocation is already on the run
	trigger(nightly("inv-2"))
	trigger(nightly("inv-2")) // a repeated trigger is one link
	trigger(rd.RoutineRef{Source: "example-scheduler", Routine: "hourly"})
	trigger(rd.RoutineRef{Source: "example-scheduler", Routine: "hourly"}) // no invocation ID: indistinguishable
	update(t, dir, func(g *Graph) (bool, error) {
		return g.Relate(session("primary", "bb", "thr_example_a", SourceConfigured))
	})
	g := load(t, dir)
	if len(g.Runs) != 1 || len(g.Links) != 3 || routineRef(g.Runs[0].Routine) != nightly("inv-1") {
		t.Fatalf("runs=%d links=%d", len(g.Runs), len(g.Links))
	}
	sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 0)
	trigger(nightly("inv-3")) // after completion: related to no run, reopens nothing
	if g = load(t, dir); len(g.Runs) != 1 || len(g.Links) != 3 || !g.Completed {
		t.Fatal("a trigger after completion changed the graph")
	}
	open("resume", 2, nightly("inv-4"))
	g = load(t, dir)

	refs := func(n Node, edgeType string) string {
		var out []string
		for _, e := range n.Edges {
			if e.Type == edgeType {
				out = append(out, e.To)
			}
		}
		return strings.Join(out, " ")
	}
	routine, status := g.Lookup(RoutineNodeRef(nightly("")))
	if status != Found || routine.Type != RoutineNode || routine.Source != SourceControl {
		t.Fatalf("%s %#v", status, routine)
	}
	// routine → runs, each once, and → its invocations
	if got := refs(routine, EdgeRun); got != id("run_", 1)+" "+id("run_", 2) {
		t.Fatalf("routine runs: %q", got)
	}
	if got := refs(routine, EdgeInvocation); got != InvocationRef(nightly("inv-1"))+" "+InvocationRef(nightly("inv-4"))+" "+InvocationRef(nightly("inv-2")) { // opening invocations, then later triggers
		t.Fatalf("routine invocations: %q", got)
	}
	// run → the invocation that opened it and the triggers it saw → back to the routine
	first, _ := g.Lookup(id("run_", 1))
	if got := refs(first, EdgeInvocation); got != InvocationRef(nightly("inv-1"))+" "+InvocationRef(nightly("inv-2"))+" routine:example-scheduler/hourly/" {
		t.Fatalf("run invocations: %q", got)
	}
	invocation, _ := g.Lookup(InvocationRef(nightly("inv-2")))
	if invocation.Source != SourceExternal || refs(invocation, EdgeRun) != id("run_", 1) || refs(invocation, EdgeRoutine) != RoutineNodeRef(nightly("")) {
		t.Fatalf("%#v", invocation)
	}
	// routine → run → chat
	if chats := g.Follow(g.Follow(routine, EdgeRun)[0], EdgeSession); len(chats) != 1 || chats[0].Ref != SessionRef("bb", "thr_example_a") {
		t.Fatalf("%#v", chats)
	}
	// A routine and an invocation never share a reference, and unknown ones stay explicit.
	if hourly, status := g.Lookup("routine:example-scheduler/hourly"); status != Found || hourly.Type != RoutineNode || refs(hourly, EdgeInvocation) != "routine:example-scheduler/hourly/" {
		t.Fatalf("%s %#v", status, hourly)
	}
	for _, ref := range []string{InvocationRef(nightly("inv-0")), InvocationRef(nightly("inv-3")), "routine:example-scheduler/weekly"} {
		if _, status := g.Lookup(ref); status != NotRecorded {
			t.Fatalf("%s: %s", ref, status)
		}
	}
	// A malformed identity is refused and nothing is written.
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	if err := Update(dir, at, func(g *Graph) (bool, error) {
		return g.Trigger(rd.RoutineRef{Source: "example-scheduler", Routine: "has space"}, at)
	}); !errors.Is(err, rd.ErrInvalid) {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, FileName)); !bytes.Equal(before, after) {
		t.Fatal("a refused trigger changed the graph")
	}
}

// A scheduler that sends a new invocation ID on every check must not push
// out an earlier run, a chat link or a goal link.
func TestTriggersAreBoundedOnTheirOwnAndEvictNothingElse(t *testing.T) {
	dir := t.TempDir()
	trigger := func(n int) {
		t.Helper()
		update(t, dir, func(g *Graph) (bool, error) {
			return g.Trigger(rd.RoutineRef{Source: "example-scheduler", Routine: "nightly", Invocation: fmt.Sprintf("inv-%d", n)}, at)
		})
	}
	sync(t, dir, enabled, "start", 0)
	for n := 1; n <= 5; n++ {
		update(t, dir, func(g *Graph) (bool, error) {
			g.Relate(session("primary", "bb", "thr_example_a", SourceConfigured))
			g.Relate(Link{Kind: rd.GoalLink, GoalID: goal, GoalRevision: 1, Source: SourceGoal, RecordedAt: at})
			return true, g.Begin(Attempt{ID: id("attempt_", n), Target: "primary", Action: "wake", Mode: "auto", RecordedAt: at})
		})
		trigger(1000 + n)
		sync(t, dir, Gate{Enabled: true, Completed: true}, "complete", 0)
		sync(t, dir, enabled, "resume", n)
	}
	update(t, dir, func(g *Graph) (bool, error) {
		g.Relate(session("primary", "bb", "thr_example_a", SourceConfigured))
		return g.Relate(Link{Kind: rd.GoalLink, GoalID: goal, GoalRevision: 1, Source: SourceGoal, RecordedAt: at})
	})
	const checks = 400 // more than every link bound
	for n := 0; n < checks; n++ {
		trigger(n)
	}
	g := load(t, dir)
	current, _ := g.Current()
	var kept []string
	for _, e := range current.Edges {
		if e.Type == EdgeInvocation {
			kept = append(kept, strings.TrimPrefix(e.To, "routine:example-scheduler/nightly/"))
		}
	}
	// The first trigger and the newest seven stay; the rest are counted.
	if strings.Join(kept, " ") != "inv-0 inv-393 inv-394 inv-395 inv-396 inv-397 inv-398 inv-399" || g.Dropped.Triggers != checks-MaxTriggersPerRun {
		t.Fatalf("kept %v, dropped %+v", kept, g.Dropped)
	}
	// Nothing else moved: every run, send, chat link and goal link is still there.
	if len(g.Runs) != 6 || len(g.Attempts) != 5 || g.Dropped.Runs != 0 || g.Dropped.Links != 0 || g.Dropped.Attempts != 0 {
		t.Fatalf("runs=%d attempts=%d dropped=%+v", len(g.Runs), len(g.Attempts), g.Dropped)
	}
	for _, r := range g.Runs {
		n, _ := g.Lookup(r.ID)
		if len(g.Follow(n, EdgeSession)) != 1 || len(g.Follow(n, EdgeGoalRevision)) != 1 {
			t.Fatalf("%s lost its chat or goal link", r.ID)
		}
	}
	// A dropped trigger is expired, not never recorded.
	if _, status := g.Lookup("routine:example-scheduler/nightly/inv-200"); status != NotRecordedOrExpired {
		t.Fatal(status)
	}
	// When the shared link bound is reached by chat links, triggers go first
	// and the chat links stay.
	for n := 0; n < MaxLinks; n++ {
		update(t, dir, func(g *Graph) (bool, error) {
			return g.Relate(session("primary", "bb", fmt.Sprintf("thr_fill_%d", n), SourceConfigured))
		})
	}
	g = load(t, dir)
	triggers, fill := 0, 0
	for _, l := range g.Links {
		if l.Kind == rd.InvocationLink {
			triggers++
		}
		if strings.HasPrefix(l.Session, "thr_fill_") {
			fill++
		}
	}
	if triggers != 0 || len(g.Links) != MaxLinks || fill == 0 {
		t.Fatalf("triggers=%d links=%d fill=%d", triggers, len(g.Links), fill)
	}
}

func TestRegistryAddRecordsNeverEvictControlRecordsAndAreUpgraded(t *testing.T) {
	dir := t.TempDir()
	sync(t, dir, enabled, "start", 1)
	register := func(n int, source Source) {
		t.Helper()
		update(t, dir, func(g *Graph) (bool, error) { return g.Register(id("loop_", n), source, at) })
	}
	register(1, SourceRegistered)
	register(2, SourceRegistered)
	// Repeated remove/add: many add-only references.
	for n := 100; n < 100+3*MaxRegistrations; n++ {
		register(n, SourceRegistryAdd)
	}
	g := load(t, dir)
	if len(g.Registrations) != MaxRegistrations || g.Registrations[0].ID != id("loop_", 1) || g.Registrations[1].ID != id("loop_", 2) ||
		g.Registrations[0].Source != SourceRegistered || g.Dropped.Registrations != uint64(2*MaxRegistrations+2) {
		t.Fatalf("%d kept, first %s, dropped %d", len(g.Registrations), g.Registrations[0].ID, g.Dropped.Registrations)
	}
	// A control operation through an add-only reference upgrades it; a later
	// add of a control-recorded reference changes nothing.
	last := 100 + 3*MaxRegistrations - 1
	register(last, SourceRegistered)
	register(1, SourceRegistryAdd)
	g = load(t, dir)
	n, _ := g.Lookup(id("loop_", last))
	first, _ := g.Lookup(id("loop_", 1))
	if n.Registration.Source != SourceRegistered || first.Registration.Source != SourceRegistered {
		t.Fatalf("%s %s", n.Registration.Source, first.Registration.Source)
	}
	if err := Update(dir, at, func(g *Graph) (bool, error) { return g.Register(id("loop_", 9), SourceGoal, at) }); !errors.Is(err, rd.ErrInvalid) {
		t.Fatal("a registry reference was recorded with an unrelated source")
	}
}

func TestUpdateExistingNeverCreatesARuntimeFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-started")
	record := func() error {
		return UpdateExisting(dir, at, func(g *Graph) (bool, error) { return g.Register(registration, SourceRegistryAdd, at) })
	}
	if err := record(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("UpdateExisting created the directory")
	}
	sync(t, dir, enabled, "start", 1)
	if err := record(); err != nil || len(load(t, dir).Registrations) != 1 {
		t.Fatal(err)
	}
	// The lock file exists but the graph is gone or unusable: nothing is written.
	path := filepath.Join(dir, FileName)
	os.Remove(path)
	if err := record(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("UpdateExisting recreated a removed graph")
	}
	os.WriteFile(path, []byte("{"), 0o600)
	if err := record(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{" {
		t.Fatal("UpdateExisting rewrote an unusable graph")
	}
}
