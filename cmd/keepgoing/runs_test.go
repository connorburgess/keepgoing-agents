package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

var runsAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const (
	runsGoalID   = "goal_66666666666666666666666666666666"
	runsHeadless = "11111111-2222-3333-4444-555555555555"
)

func runsID(prefix string, n int) string { return fmt.Sprintf("%s%032x", prefix, n) }

func runsUpdate(t *testing.T, dir string, fn func(*rungraph.Graph) (bool, error)) {
	t.Helper()
	if err := rungraph.Update(dir, runsAt, fn); err != nil {
		t.Fatal(err)
	}
}

func runsSync(t *testing.T, dir string, completed bool, operation string, run int) {
	t.Helper()
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		return g.Sync(rungraph.Boundary{Gate: rungraph.Gate{Enabled: true, Completed: completed}, Operation: operation, At: runsAt, NewRunID: runsID("run_", run)})
	})
}

func rtSession(target, provider, id string, source rungraph.Source) rungraph.Link {
	return rungraph.Link{Kind: rd.SessionLink, Target: target, Provider: provider, Session: id, Source: source, RecordedAt: runsAt}
}

func runsSend(n int, target string) rungraph.Attempt {
	return rungraph.Attempt{ID: runsID("attempt_", n), Target: target, Action: "wake", Mode: "auto", GoalID: runsGoalID, GoalRevision: 1, RecordedAt: runsAt}
}

// runsFixture records a closed first run and an open second run that reuses
// the first run's chat and adds a headless fallback session.
// runsClock makes the reader's clock one minute after the fixture's records.
func runsClock(t *testing.T, at time.Time) {
	t.Helper()
	previous := runsNow
	runsNow = func() time.Time { return at }
	t.Cleanup(func() { runsNow = previous })
}

func runsFixture(t *testing.T) string {
	t.Helper()
	runsClock(t, runsAt.Add(time.Minute))
	dir := t.TempDir()
	if err := control.Save(dir, control.File{Enabled: true, Note: "PRIVATE-NOTE"}); err != nil {
		t.Fatal(err)
	}
	// As a check leaves it: a saved active target and the fallback runner's
	// saved resume ID.
	if err := state.Save(dir, state.File{}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Dir(filepath.Join(dir, "targets", "example-fallback")).SaveSession(runsHeadless); err != nil {
		t.Fatal(err)
	}
	runsSync(t, dir, false, "start", 1)
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		g.Relate(rtSession("primary", "bb", "thr_example_a", rungraph.SourceConfigured))
		g.Relate(rungraph.Link{Kind: rd.GoalLink, GoalID: runsGoalID, GoalRevision: 1, Source: rungraph.SourceGoal, RecordedAt: runsAt})
		return true, g.Begin(runsSend(1, "primary"))
	})
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		return g.Finish(runsID("attempt_", 1), rd.AdapterReturned, runsAt)
	})
	runsSync(t, dir, true, "complete", 0)
	runsSync(t, dir, false, "resume", 2)
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		g.Relate(rtSession("primary", "bb", "thr_example_a", rungraph.SourceConfigured))
		g.Relate(rtSession("example-fallback", "claude", runsHeadless, rungraph.SourceRunnerFile))
		g.Skip("odd-target", rungraph.ReasonSessionID, runsAt)
		if err := g.Begin(runsSend(2, "primary")); err != nil {
			return false, err
		}
		return true, g.Begin(runsSend(3, "example-fallback"))
	})
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) { return g.Finish(runsID("attempt_", 2), rd.AdapterError, runsAt) })
	return dir
}

func runsExec(t *testing.T, args ...string) (int, runsReport, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := run(context.Background(), append([]string{"runs", "--json"}, args...), &out, &errs)
	var r runsReport
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("output is not the report: %v\n%s", err, out.String())
		}
	}
	return code, r, out.String() + errs.String()
}

// snapshotTree lists every file under dir with its bytes and modification
// time, so a read that creates, rewrites or touches anything is caught.
func snapshotTree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		fmt.Fprintf(&b, "%s %v %d", path, info.Mode(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, _ := os.ReadFile(path)
			fmt.Fprintf(&b, " %x", data)
		}
		b.WriteString("\n")
		return nil
	})
	return b.String()
}

func TestRunsLoopViewResolvesCurrentWorkAndItsChat(t *testing.T) {
	dir := runsFixture(t)
	code, r, raw := runsExec(t, "--state-dir", dir)
	if code != exitOK || r.SchemaVersion != 2 || r.View != "loop" || len(r.Loops) != 1 || r.Node != nil || len(r.Limitations) == 0 {
		t.Fatalf("%d %s", code, raw)
	}
	l := r.Loops[0]
	if l.Graph.Availability != rungraph.Available || !strings.HasPrefix(l.Graph.LoopID, "supervision_") || l.Graph.UpdatedAt == nil || l.Control.State != "enabled" ||
		l.ActiveTarget != (runsTarget{Availability: "recorded", Alias: "primary"}) || l.Conflict != "" || l.Blocker.Availability != blockerUnknown || l.Blocker.Reason != "no_child_evidence_recorded" || l.Blocker.Lock.Availability != recordUnavailable {
		t.Fatalf("%+v", l)
	}
	cur := l.Current
	if cur == nil || cur.ID != runsID("run_", 2) || cur.State != "open" || cur.Origin != rd.Reopened || cur.Reopens != runsID("run_", 1) || cur.ClosedAt != nil {
		t.Fatalf("%+v", cur)
	}
	// The open run's chat for the saved active target is configured evidence.
	// The fallback's saved session is not the active target, so it is history.
	if len(cur.Sessions) != 2 || cur.SessionEvidence != evidenceConfigured ||
		cur.Sessions[0].Ref != "session:bb/thr_example_a" || cur.Sessions[0].Provider != "bb" || cur.Sessions[0].ID != "thr_example_a" || cur.Sessions[0].Evidence != evidenceConfigured ||
		cur.Sessions[0].Link.Availability != "unavailable" || cur.Sessions[1].ID != runsHeadless || cur.Sessions[1].Evidence != evidenceHistorical || cur.Sessions[1].Target != "example-fallback" {
		t.Fatalf("%+v", cur.Sessions)
	}
	if len(cur.Unrecorded) != 1 || cur.Unrecorded[0] != (runsUnrecorded{Target: "odd-target", Reason: rungraph.ReasonSessionID}) {
		t.Fatalf("%+v", cur.Unrecorded)
	}
	// Sends are newest first; the one with no recorded return is unconfirmed.
	a := cur.Attempts
	if a.Total != 2 || a.Unconfirmed != 1 || len(a.Latest) != 2 || a.Latest[0].ID != runsID("attempt_", 3) || a.Latest[0].Outcome != rd.Intent || a.Latest[0].ReturnedAt != nil ||
		a.Latest[0].Session != (runsAttemptSession{Availability: "recorded", Ref: "session:claude/" + runsHeadless}) ||
		a.Latest[1].Outcome != rd.AdapterError || a.Latest[1].Session.Ref != "session:bb/thr_example_a" || a.Latest[1].GoalRef != runsGoalID+"@1" {
		t.Fatalf("%+v", a)
	}
	// The earlier run is closed: its chat is historical even though the same
	// chat is the current run's configured one.
	if len(l.Recent) != 1 || l.OmittedRuns != 0 {
		t.Fatalf("%+v", l.Recent)
	}
	old := l.Recent[0]
	if old.ID != runsID("run_", 1) || old.State != "completion_declared" || old.ClosedAt == nil || old.SessionEvidence != evidenceHistorical ||
		old.Sessions[0].Ref != cur.Sessions[0].Ref || len(old.GoalRevisions) != 1 || old.GoalRevisions[0].Ref != runsGoalID+"@1" || old.Attempts.Total != 1 {
		t.Fatalf("%+v", old)
	}
	// After a recorded move to the fallback, its saved session is the observed
	// one and the primary chat becomes history.
	if err := state.Save(dir, state.File{ActiveTarget: "example-fallback"}); err != nil {
		t.Fatal(err)
	}
	_, r, _ = runsExec(t, "--state-dir", dir)
	cur = r.Loops[0].Current
	if cur.SessionEvidence != evidenceObserved || cur.Sessions[0].Evidence != evidenceHistorical || cur.Sessions[1].Evidence != evidenceObserved {
		t.Fatalf("%+v", cur.Sessions)
	}
	for _, private := range []string{"PRIVATE-NOTE", dir} {
		if strings.Contains(raw, private) {
			t.Fatalf("private value %q in output", private)
		}
	}
}

func TestRunsFollowsExactReferencesInBothDirections(t *testing.T) {
	dir := runsFixture(t)
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		return g.Register(runsID("loop_", 7), rungraph.SourceRegistered, runsAt)
	})
	g, _ := rungraph.Load(dir)
	node := func(ref string) *runsNode {
		t.Helper()
		code, r, raw := runsExec(t, "--state-dir", dir, "--id", ref)
		if code != exitOK || r.View != "node" || r.Node == nil || r.Node.Ref != ref {
			t.Fatalf("%d %s", code, raw)
		}
		return r.Node
	}
	edges := func(n *runsNode, typ string) string {
		var out []string
		for _, e := range n.Edges {
			if e.Type == typ {
				out = append(out, e.To)
			}
		}
		return strings.Join(out, " ")
	}
	chat := "session:bb/thr_example_a"
	// registry reference → loop → runs → attempt → chat
	reference := node(runsID("loop_", 7))
	if reference.Status != rungraph.Found || reference.Type != rungraph.RegistrationNode || edges(reference, rungraph.EdgeLoop) != g.LoopID {
		t.Fatalf("%+v", reference)
	}
	loop := node(g.LoopID)
	if edges(loop, rungraph.EdgeCurrentRun) != runsID("run_", 2) || edges(loop, rungraph.EdgeRun) != runsID("run_", 1) {
		t.Fatalf("%+v", loop.Edges)
	}
	current := node(runsID("run_", 2))
	if current.Run == nil || current.Run.State != "open" || current.Run.Sessions[0].Evidence != evidenceConfigured ||
		edges(current, rungraph.EdgeAttempt) != runsID("attempt_", 2)+" "+runsID("attempt_", 3) || edges(current, rungraph.EdgeReopens) != runsID("run_", 1) {
		t.Fatalf("%+v", current)
	}
	sent := node(runsID("attempt_", 2))
	if sent.Attempt == nil || sent.Attempt.Session.Ref != chat || edges(sent, rungraph.EdgeRun) != runsID("run_", 2) || sent.Source != rungraph.SourceAdapter || sent.RecordedAt == nil {
		t.Fatalf("%+v", sent)
	}
	// chat → both runs that recorded it, and back to the sends made to it.
	shared := node(chat)
	if shared.Type != rungraph.SessionNode || edges(shared, rungraph.EdgeRun) != runsID("run_", 1)+" "+runsID("run_", 2) ||
		len(shared.Attempts) != 2 || shared.Attempts[0].ID != runsID("attempt_", 2) || shared.Attempts[1].ID != runsID("attempt_", 1) {
		t.Fatalf("%+v", shared)
	}
	for _, e := range shared.Edges {
		if !e.Resolves || e.Target != "primary" || e.Source != rungraph.SourceConfigured {
			t.Fatalf("%+v", e)
		}
	}
	if goal := node(runsGoalID + "@1"); goal.Type != rungraph.GoalRevisionNode || edges(goal, rungraph.EdgeRun) != runsID("run_", 1) {
		t.Fatalf("%+v", goal)
	}
	// Unknown references say so; nothing is searched for or guessed.
	for _, ref := range []string{runsID("run_", 9), "session:bb/thr_example_z", "thr_example_a", "example-loop"} {
		if n := node(ref); n.Status != rungraph.NotRecorded || n.Type != "" || len(n.Edges) != 0 || n.Run != nil {
			t.Fatalf("%s: %+v", ref, n)
		}
	}
	// A target alias that named two chats in one run leaves the send's chat
	// ambiguous instead of picking one.
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		return g.Relate(rtSession("primary", "bb", "thr_example_b", rungraph.SourceConfigured))
	})
	if sent = node(runsID("attempt_", 2)); sent.Attempt.Session != (runsAttemptSession{Availability: "ambiguous"}) {
		t.Fatalf("%+v", sent.Attempt.Session)
	}
	if first := node(runsID("attempt_", 1)); first.Attempt.Session.Ref != chat {
		t.Fatal("the earlier run's send lost its chat")
	}
}

func TestRunsOutputIsBoundedAndExpiryStaysExplicit(t *testing.T) {
	dir := t.TempDir()
	runsSync(t, dir, false, "start", 0)
	for n := 1; n <= rungraph.MaxRuns+1; n++ {
		runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) { return true, g.Begin(runsSend(n, "primary")) })
		runsSync(t, dir, true, "complete", 0)
		runsSync(t, dir, false, "resume", n)
	}
	for n := 100; n < 110; n++ {
		runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) { return true, g.Begin(runsSend(n, "primary")) })
	}
	_, r, _ := runsExec(t, "--state-dir", dir)
	l := r.Loops[0]
	if len(l.Recent) != runsDefaultLimit || l.OmittedRuns != rungraph.MaxRuns-1-runsDefaultLimit || l.Recent[0].ID != runsID("run_", rungraph.MaxRuns) ||
		l.Graph.Dropped.Runs != 2 || l.Current.Attempts.Total != 10 || len(l.Current.Attempts.Latest) != runsLatestSends {
		t.Fatalf("recent=%d omitted=%d dropped=%+v", len(l.Recent), l.OmittedRuns, l.Graph.Dropped)
	}
	_, r, _ = runsExec(t, "--state-dir", dir, "--limit", "2")
	if len(r.Loops[0].Recent) != 2 || r.Loops[0].OmittedRuns != rungraph.MaxRuns-3 {
		t.Fatalf("%d %d", len(r.Loops[0].Recent), r.Loops[0].OmittedRuns)
	}
	_, r, _ = runsExec(t, "--state-dir", dir, "--limit", "4", "--id", runsID("run_", rungraph.MaxRuns+1))
	if len(r.Node.Edges) != 4 || r.Node.OmittedEdges != 8 {
		t.Fatalf("edges=%d omitted=%d", len(r.Node.Edges), r.Node.OmittedEdges)
	}
	// A dropped run is not reported as never recorded, and the oldest
	// retained run's predecessor edge says it no longer resolves.
	_, r, _ = runsExec(t, "--state-dir", dir, "--id", runsID("run_", 0))
	if r.Node.Status != rungraph.NotRecordedOrExpired {
		t.Fatal(r.Node.Status)
	}
	_, r, _ = runsExec(t, "--state-dir", dir, "--id", runsID("run_", 2))
	for _, e := range r.Node.Edges {
		if e.Type == rungraph.EdgeReopens && (e.To != runsID("run_", 1) || e.Resolves) {
			t.Fatalf("%+v", e)
		}
	}
}

// Reads must work on a held or finished loop, while another process holds
// the tick and graph locks, and must leave every byte and timestamp alone.
func TestRunsReadsNeverChangeRecordsOrWaitForLocks(t *testing.T) {
	for name, hold := range map[string]control.File{
		"paused":    {Enabled: true, Paused: true, Note: "fictional"},
		"stopped":   {Enabled: true, Stop: true},
		"completed": {Enabled: true, Completed: true},
		"disabled":  {},
		"awaiting":  {Enabled: true, AwaitingUser: true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := runsFixture(t)
			if err := control.Save(dir, hold); err != nil {
				t.Fatal(err)
			}
			for _, lockName := range []string{lock.Tick, lock.Control, "graph"} {
				held, err := lock.AcquireNamed(dir, lockName)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Release()
			}
			before := snapshotTree(t, dir)
			started := time.Now()
			code, r, raw := runsExec(t, "--state-dir", dir)
			if code != exitOK || r.Loops[0].Current == nil || r.Loops[0].Control.State != hold.Summary() {
				t.Fatalf("%d %s", code, raw)
			}
			wantConflict := ""
			if hold.Completed {
				wantConflict = rungraph.ConflictCompletionNotRecorded
			}
			if r.Loops[0].Conflict != wantConflict {
				t.Fatalf("conflict %q", r.Loops[0].Conflict)
			}
			for _, args := range [][]string{{"--id", runsID("run_", 2)}, {"--id", "session:bb/thr_example_a"}, {"--id", "unknown"}, {"--limit", "1"}} {
				if code, _, raw := runsExec(t, append([]string{"--state-dir", dir}, args...)...); code != exitOK {
					t.Fatalf("%v: %d %s", args, code, raw)
				}
			}
			var text, errs bytes.Buffer
			if code := run(context.Background(), []string{"runs", "--state-dir", dir}, &text, &errs); code != exitOK || !strings.Contains(text.String(), runsID("run_", 2)) {
				t.Fatalf("%d %s %s", code, text.String(), errs.String())
			}
			if time.Since(started) > 5*time.Second {
				t.Fatal("a read waited on a held lock")
			}
			if after := snapshotTree(t, dir); after != before {
				t.Fatalf("a read changed the state directory:\n%s\n---\n%s", before, after)
			}
		})
	}
	// A directory that does not exist is a loop with nothing recorded, and it
	// is not created.
	absent := filepath.Join(t.TempDir(), "never-started")
	code, r, raw := runsExec(t, "--state-dir", absent)
	l := r.Loops[0]
	if code != exitOK || l.Graph.Availability != rungraph.Missing || l.Current != nil || len(l.Recent) != 0 || l.Graph.LoopID != "" ||
		l.Control != (runsControl{Availability: recordNotRecorded}) || l.ActiveTarget != (runsTarget{Availability: recordNotRecorded}) {
		t.Fatalf("absent records were reported as defaults: %d %s", code, raw)
	}
	for _, invented := range []string{"primary", "disabled", "enabled"} {
		if strings.Contains(raw, `"`+invented+`"`) {
			t.Fatalf("a default %q was printed for a record that does not exist", invented)
		}
	}
	if code, r, _ := runsExec(t, "--state-dir", absent, "--id", runsID("run_", 1)); code != exitOK || r.Node.Status != rungraph.NotRecorded {
		t.Fatal(code, r.Node)
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatal("a read created the state directory")
	}
}

func TestRunsUnusableRecordsArePartialResults(t *testing.T) {
	for name, tt := range map[string]struct {
		body string
		want rungraph.Availability
	}{
		"corrupt":      {"{", rungraph.Corrupt},
		"newer schema": {`{"schemaVersion":99}`, rungraph.Unsupported},
	} {
		t.Run(name, func(t *testing.T) {
			dir := runsFixture(t)
			path := filepath.Join(dir, rungraph.FileName)
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			code, r, raw := runsExec(t, "--state-dir", dir, "--id", runsID("run_", 2))
			l := r.Loops[0]
			if code != exitFailed || l.Graph.Availability != tt.want || l.Current != nil || l.Control.State != "enabled" || l.Conflict != "" || r.Node.Status != recordUnavailable || len(r.Node.Edges) != 0 {
				t.Fatalf("%d %s", code, raw)
			}
			if b, _ := os.ReadFile(path); string(b) != tt.body {
				t.Fatal("an unusable graph was rewritten by a read")
			}
		})
	}
	// Unreadable control is reported; the graph is still shown.
	dir := runsFixture(t)
	if err := os.WriteFile(filepath.Join(dir, control.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, r, raw := runsExec(t, "--state-dir", dir)
	if code != exitFailed || r.Loops[0].Control != (runsControl{Availability: "unavailable"}) || r.Loops[0].Current == nil {
		t.Fatalf("%d %s", code, raw)
	}
}

// Two registered loops share a name. Their registry references keep them
// apart, and each shows only its own run.
func TestRunsRegistryViewSeparatesLoopsWithTheSameName(t *testing.T) {
	regDir := filepath.Join(t.TempDir(), "registry")
	var entries []registry.Entry
	for i := 0; i < 2; i++ {
		if code, out, errs := registryRun(t, regDir, "add", "-c", registryConfig(t)); code != 0 {
			t.Fatal(out, errs)
		}
	}
	f, err := registry.Load(regDir)
	if err != nil || len(f.Entries) != 2 || f.Entries[0].Loop != f.Entries[1].Loop {
		t.Fatalf("%+v %v", f, err)
	}
	entries = f.Entries
	// The first loop has a run and has been controlled through its reference.
	// The second has never started.
	if err := control.Save(entries[0].StateDir, control.File{Enabled: true, Paused: true}); err != nil {
		t.Fatal(err)
	}
	runsSync(t, entries[0].StateDir, false, "start", 1)
	runsUpdate(t, entries[0].StateDir, func(g *rungraph.Graph) (bool, error) {
		g.Relate(rtSession("primary", "bb", "thr_example", rungraph.SourceConfigured))
		return g.Register(entries[0].ID, rungraph.SourceRegistered, runsAt)
	})
	before := snapshotTree(t, regDir) + snapshotTree(t, entries[0].StateDir)
	code, r, raw := runsExec(t, "--all", "--directory", regDir)
	if code != exitOK || r.View != "registry" || len(r.Loops) != 2 || r.Omitted != 0 {
		t.Fatalf("%d %s", code, raw)
	}
	allOutput := raw
	first, second := r.Loops[0], r.Loops[1]
	if first.RegistrationID != entries[0].ID || first.Registration != "recorded" || first.Loop != "example-loop" || first.Current == nil || first.Current.ID != runsID("run_", 1) ||
		first.Control.State != "paused" || first.Current.Sessions[0].Ref != "session:bb/thr_example" || len(first.Recent) != 0 || first.Binding != recordAvailable {
		t.Fatalf("%+v", first)
	}
	if second.RegistrationID != entries[1].ID || second.Registration != "not_recorded" || second.Loop != first.Loop || second.Current != nil || second.Graph.Availability != rungraph.Missing {
		t.Fatalf("%+v", second)
	}
	// Selecting by reference, then following the chat from there.
	code, r, raw = runsExec(t, "--registry-id", entries[0].ID, "--directory", regDir, "--id", "session:bb/thr_example")
	if code != exitOK || r.View != "node" || len(r.Loops) != 1 || r.Loops[0].RegistrationID != entries[0].ID || r.Node.Status != rungraph.Found || r.Node.Edges[0].To != runsID("run_", 1) {
		t.Fatalf("%d %s", code, raw)
	}
	// The same chat reference is not found through the other loop's reference.
	_, r, _ = runsExec(t, "--registry-id", entries[1].ID, "--directory", regDir, "--id", "session:bb/thr_example")
	if r.Node.Status != rungraph.NotRecorded || r.Loops[0].Registration != "not_recorded" {
		t.Fatalf("%+v", r.Node)
	}
	if code, r, _ := runsExec(t, "--all", "--directory", regDir, "--limit", "1"); code != exitOK || len(r.Loops) != 1 || r.Omitted != 1 {
		t.Fatal(code, len(r.Loops), r.Omitted)
	}
	if after := snapshotTree(t, regDir) + snapshotTree(t, entries[0].StateDir); after != before {
		t.Fatal("a registry read changed the registry or a state directory")
	}
	if _, err := os.Stat(entries[1].StateDir); !os.IsNotExist(err) {
		t.Fatal("a registry read created a runtime directory")
	}
	for _, private := range []string{entries[0].StateDir, entries[0].ConfigPath, entries[1].StateDir, entries[1].ConfigPath, filepath.Dir(entries[0].ConfigPath), "private-missing-task", "nonexistent-provider-fixture"} {
		if strings.Contains(raw, private) || strings.Contains(allOutput, private) {
			t.Fatalf("private value %q in output", private)
		}
	}
	if code, _, raw := runsExec(t, "--registry-id", runsID("loop_", 9), "--directory", regDir); code != exitFailed || strings.Contains(raw, regDir) {
		t.Fatalf("unknown registration: %d %s", code, raw)
	}
}

func TestRunsRejectsAmbiguousOrOutOfRangeArguments(t *testing.T) {
	dir := runsFixture(t)
	for _, args := range [][]string{
		{"--state-dir", dir, "-c", "keepgoing.yaml"},
		{"--state-dir", dir, "--all"},
		{"--state-dir", ""},
		{"--state-dir", dir, "--limit", "0"},
		{"--state-dir", dir, "--limit", "257"},
		{"--state-dir", dir, "--id", ""},
		{"--state-dir", dir, "--id", strings.Repeat("a", 513)},
		{"--state-dir", dir, "--id", "run_1\nforged: line"},
		{"--state-dir", dir, "--id", "run_1\x1b[2Jcleared"},
		{"--state-dir", dir, "--id", "two words"},
		{"--registry-id", "example-loop"},
		{"--all", "--id", runsID("run_", 1)},
		{"--state-dir", dir, "extra"},
		{"--unknown"},
		{"-c", filepath.Join(dir, "absent.yaml")},
	} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"runs"}, args...), &out, &errs); code != exitUsage || out.Len() != 0 || strings.Contains(errs.String(), dir) {
			t.Fatalf("%v: %d %s %s", args, code, out.String(), errs.String())
		}
	}
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"runs", "--help"}, &out, &errs); code != exitOK || !strings.Contains(out.String(), "registry-id") {
		t.Fatal(code, out.String())
	}
}

// A corrupt, newer-schema or unreadable record must never answer a lookup
// with not_recorded: the reference may be in it.
func TestRunsLookupInAnUnreadableGraphIsUnavailableNotUnrecorded(t *testing.T) {
	for name, body := range map[string]string{"truncated": `{"schemaVersion":1,"loopId":"superv`, "newer schema": `{"schemaVersion":99}`, "empty": ""} {
		dir := runsFixture(t)
		if err := os.WriteFile(filepath.Join(dir, rungraph.FileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		code, r, raw := runsExec(t, "--state-dir", dir, "--id", runsID("run_", 2))
		if code != exitFailed || r.Node.Status != recordUnavailable || r.Loops[0].Blocker.Reason != "graph_unavailable" {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
		var text, errs bytes.Buffer
		run(context.Background(), []string{"runs", "--state-dir", dir, "--id", runsID("run_", 2)}, &text, &errs)
		if !strings.Contains(text.String(), "reference "+runsID("run_", 2)+": unavailable") || strings.Contains(text.String(), ": not_recorded") {
			t.Fatalf("%s: %s", name, text.String())
		}
	}
	// With no graph at all, not_recorded is the true answer.
	if _, r, _ := runsExec(t, "--state-dir", t.TempDir(), "--id", runsID("run_", 2)); r.Node.Status != rungraph.NotRecorded {
		t.Fatal(r.Node.Status)
	}
}

// With no saved active target there is nothing to compare a chat with, so no
// chat is labelled configured or observed.
func TestRunsWithoutSavedTargetLabelsNoChatAsCurrent(t *testing.T) {
	dir := runsFixture(t)
	if err := os.Remove(filepath.Join(dir, state.FileName)); err != nil {
		t.Fatal(err)
	}
	_, r, raw := runsExec(t, "--state-dir", dir)
	cur := r.Loops[0].Current
	if r.Loops[0].ActiveTarget != (runsTarget{Availability: recordNotRecorded}) || cur.SessionEvidence != evidenceHistorical || strings.Contains(raw, `"evidence": "configured"`) || strings.Contains(raw, `"evidence": "observed"`) {
		t.Fatalf("%s", raw)
	}
}

func runsConfig(t *testing.T, stateDir, thread string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keepgoing.yaml")
	body := fmt.Sprintf("version: 1\nloop:\n  name: example-loop\n  state_dir: %q\n  adapter: bb\n  provider: example\n  bb: {thread: %s, cli: nonexistent-provider-fixture}\n  prompt: Continue the fictional loop.\n", stateDir, thread)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The configured chat was edited during the open run, so the alias has two
// links. Only the one configuration names now is configured; the sends for
// that alias cannot be attributed and are counted on both chats.
func TestRunsSupersededChatIsHistoricalAndItsSendsAreCounted(t *testing.T) {
	dir := runsFixture(t)
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		return g.Relate(rtSession("primary", "bb", "thr_example_b", rungraph.SourceConfigured))
	})
	evidence := func(r runsReport) string {
		var out []string
		for _, s := range r.Loops[0].Current.Sessions {
			out = append(out, s.ID+"="+s.Evidence)
		}
		return strings.Join(out, " ")
	}
	for thread, want := range map[string]string{
		"thr_example_b": "thr_example_a=historical " + runsHeadless + "=historical thr_example_b=configured",
		"thr_example_a": "thr_example_a=configured " + runsHeadless + "=historical thr_example_b=historical",
		// Configuration changed again and no check has recorded it yet.
		"thr_example_c": "thr_example_a=historical " + runsHeadless + "=historical thr_example_b=historical",
	} {
		_, r, raw := runsExec(t, "-c", runsConfig(t, dir, thread))
		if got := evidence(r); got != want || r.Loops[0].Loop != "example-loop" {
			t.Fatalf("configured %s: %s\n%s", thread, got, raw)
		}
	}
	// Without configuration nothing says which of the two is in use.
	_, r, _ := runsExec(t, "--state-dir", dir)
	if got := evidence(r); strings.Contains(got, evidenceConfigured) || r.Loops[0].Current.SessionEvidence != evidenceHistorical {
		t.Fatal(got)
	}
	for _, chat := range []string{"session:bb/thr_example_a", "session:bb/thr_example_b"} {
		_, r, raw := runsExec(t, "--state-dir", dir, "--id", chat)
		if r.Node.AmbiguousAttempts != 1 {
			t.Fatalf("%s: unattributed sends not counted: %s", chat, raw)
		}
		var text, errs bytes.Buffer
		run(context.Background(), []string{"runs", "--state-dir", dir, "--id", chat}, &text, &errs)
		if !strings.Contains(text.String(), "1 sends not attributed") {
			t.Fatalf("%s", text.String())
		}
	}
	// The earlier run recorded one chat for the alias, so its send still
	// belongs to that chat.
	_, r, _ = runsExec(t, "--state-dir", dir, "--id", "session:bb/thr_example_a")
	if len(r.Node.Attempts) != 1 || r.Node.Attempts[0].ID != runsID("attempt_", 1) {
		t.Fatalf("%+v", r.Node.Attempts)
	}
	// A headless link stops being observed when the runner's saved ID changes.
	if err := state.Save(dir, state.File{ActiveTarget: "example-fallback"}); err != nil {
		t.Fatal(err)
	}
	if _, r, _ = runsExec(t, "--state-dir", dir); r.Loops[0].Current.SessionEvidence != evidenceObserved {
		t.Fatal(evidence(r))
	}
	if err := os.WriteFile(filepath.Join(dir, "targets", "example-fallback", "session"), []byte("99999999-2222-3333-4444-555555555555\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, r, _ = runsExec(t, "--state-dir", dir); strings.Contains(evidence(r), evidenceObserved) {
		t.Fatal(evidence(r))
	}
}

// A registration whose configuration now names another runtime directory or
// loop must not print the old directory's runs under the registered name.
func TestRunsRegistrySelectorsRefuseAStaleBinding(t *testing.T) {
	regDir, entry := clientFixture(t)
	if err := control.Save(entry.StateDir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	runsSync(t, entry.StateDir, false, "start", 1)
	if code, r, _ := runsExec(t, "--registry-id", entry.ID, "--directory", regDir); code != exitOK || r.Loops[0].Binding != recordAvailable || r.Loops[0].Current == nil {
		t.Fatal(code, r.Loops)
	}
	original, _ := os.ReadFile(entry.ConfigPath)
	for name, edit := range map[string]func() error{
		"state_dir changed": func() error {
			return os.WriteFile(entry.ConfigPath, []byte(strings.Replace(string(original), "state_dir: state", "state_dir: elsewhere", 1)), 0o600)
		},
		"loop renamed": func() error {
			return os.WriteFile(entry.ConfigPath, []byte(strings.Replace(string(original), "name: example-loop", "name: other-loop", 1)), 0o600)
		},
		"config removed": func() error { return os.Remove(entry.ConfigPath) },
	} {
		if err := edit(); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"--registry-id", entry.ID}, {"--all"}, {"--registry-id", entry.ID, "--id", runsID("run_", 1)}} {
			code, r, raw := runsExec(t, append(args, "--directory", regDir)...)
			l := r.Loops[0]
			if code != exitFailed || l.Binding != recordUnavailable || l.Current != nil || l.Graph.LoopID != "" || l.Graph.Availability != recordUnavailable ||
				strings.Contains(raw, runsID("run_", 1)) && r.Node == nil || (r.Node != nil && r.Node.Status != recordUnavailable) {
				t.Fatalf("%s %v: %d %s", name, args, code, raw)
			}
		}
		var text, errs bytes.Buffer
		run(context.Background(), []string{"runs", "--registry-id", entry.ID, "--directory", regDir}, &text, &errs)
		if !strings.Contains(text.String(), "binding: unavailable") || strings.Contains(text.String(), "current run") {
			t.Fatalf("%s: %s", name, text.String())
		}
	}
}

// Text mode must be enough to walk every documented path: each step uses only
// a reference printed by the step before.
func TestRunsTextModeWalksEveryDocumentedPath(t *testing.T) {
	dir := runsFixture(t)
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		return g.Register(runsID("loop_", 7), rungraph.SourceRegistered, runsAt)
	})
	text := func(args ...string) string {
		t.Helper()
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"runs", "--state-dir", dir}, args...), &out, &errs); code != exitOK {
			t.Fatalf("%v: %d %s", args, code, errs.String())
		}
		return out.String()
	}
	// step follows ref and returns the reference printed after marker.
	step := func(ref, marker string) string {
		t.Helper()
		out := text("--id", ref)
		_, rest, found := strings.Cut(out[strings.Index(out, "\nreference "):], marker)
		if !found {
			t.Fatalf("%s does not print %q:\n%s", ref, marker, out)
		}
		return strings.Fields(rest)[0]
	}
	// registry reference → durable loop → current run → send → chat
	loop := step(runsID("loop_", 7), "loop -> ")
	if !strings.HasPrefix(loop, "supervision_") {
		t.Fatal(loop)
	}
	current := step(loop, "current_run -> ")
	earlier := step(loop, "\n  run -> ")
	send := step(current, "attempt -> ")
	if current != runsID("run_", 2) || earlier != runsID("run_", 1) || send != runsID("attempt_", 2) {
		t.Fatal(current, earlier, send)
	}
	chat := step(send, "chat/session ")
	if chat != "session:bb/thr_example_a" || step(send, "run -> ") != current || step(send, "goal_revision -> ") != runsGoalID+"@1" {
		t.Fatal(chat)
	}
	// chat → the runs that recorded it → and its sends, each naming its run
	back := text("--id", chat)
	for _, want := range []string{"run -> " + earlier, "run -> " + current, runsID("attempt_", 1), send, "run " + earlier, "run " + current} {
		if !strings.Contains(back, want) {
			t.Fatalf("chat view lacks %q:\n%s", want, back)
		}
	}
	// run → chat, goal revision, predecessor; goal revision → run
	if step(current, "session -> ") != chat || step(current, "reopens -> ") != earlier || step(earlier, "goal_revision -> ") != runsGoalID+"@1" || step(runsGoalID+"@1", "run -> ") != earlier {
		t.Fatal("a run link is missing in text mode")
	}
	// The loop view itself names the current run, its chat and each send's chat.
	view := text()
	for _, want := range []string{"current run: " + current, "chat/session: " + chat, send, "chat/session " + chat, "chat/session session:claude/" + runsHeadless} {
		if !strings.Contains(view, want) {
			t.Fatalf("loop view lacks %q:\n%s", want, view)
		}
	}
	// An ambiguous send says so.
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		return g.Relate(rtSession("primary", "bb", "thr_example_b", rungraph.SourceConfigured))
	})
	if out := text("--id", send); !strings.Contains(out, "chat/session ambiguous") {
		t.Fatal(out)
	}
}

func TestRunsLimitBoundsEveryListInARun(t *testing.T) {
	dir := t.TempDir()
	runsSync(t, dir, false, "start", 1)
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) {
		for n := 0; n < 12; n++ {
			g.Relate(rtSession(fmt.Sprintf("target-%d", n), "bb", fmt.Sprintf("thr_example_%d", n), rungraph.SourceConfigured))
			g.Relate(rungraph.Link{Kind: rd.GoalLink, GoalID: runsGoalID, GoalRevision: uint64(n + 1), Source: rungraph.SourceGoal, RecordedAt: runsAt})
		}
		return true, nil
	})
	_, r, _ := runsExec(t, "--state-dir", dir, "--limit", "5")
	cur := r.Loops[0].Current
	if len(cur.Sessions) != 5 || cur.OmittedSessions != 7 || len(cur.GoalRevisions) != 5 || cur.OmittedGoalRevisions != 7 {
		t.Fatalf("sessions=%d+%d goals=%d+%d", len(cur.Sessions), cur.OmittedSessions, len(cur.GoalRevisions), cur.OmittedGoalRevisions)
	}
	var text, errs bytes.Buffer
	run(context.Background(), []string{"runs", "--state-dir", dir, "--limit", "5"}, &text, &errs)
	if !strings.Contains(text.String(), "7 more chat/session references not shown") || !strings.Contains(text.String(), "7 more goal revisions not shown") {
		t.Fatal(text.String())
	}
}

// A hand-edited record cannot forge output lines through a target alias.
func TestRunsTextOutputNeutralisesControlCharacters(t *testing.T) {
	dir := runsFixture(t)
	if err := state.Save(dir, state.File{ActiveTarget: "evil\nforged: line\x1b[2J"}); err != nil {
		t.Fatal(err)
	}
	var text, errs bytes.Buffer
	run(context.Background(), []string{"runs", "--state-dir", dir}, &text, &errs)
	if strings.Contains(text.String(), "\nforged") || strings.Contains(text.String(), "\x1b") || !strings.Contains(text.String(), "evil?forged:?line?[2J") {
		t.Fatal(text.String())
	}
}

// `registry add` maps a reference only when the loop already has a graph, and
// never creates a runtime file. Repeating it after the first run records it.
func TestRunsRegistryAddRecordsTheReferenceOnceAGraphExists(t *testing.T) {
	regDir, entry := clientFixture(t)
	registration := func() string {
		t.Helper()
		code, r, raw := runsExec(t, "--registry-id", entry.ID, "--directory", regDir)
		if code != exitOK {
			t.Fatal(code, raw)
		}
		return r.Loops[0].Registration
	}
	if _, err := os.Stat(entry.StateDir); !os.IsNotExist(err) || registration() != recordNotRecorded {
		t.Fatal("registration before the first run created runtime files or was reported as recorded")
	}
	// Runtime files without a graph: a repeated add still writes nothing.
	if err := control.Save(entry.StateDir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, entry.StateDir)
	if code, out, errs := registryRun(t, regDir, "add", "-c", entry.ConfigPath); code != 0 {
		t.Fatal(out, errs)
	}
	if snapshotTree(t, entry.StateDir) != before || registration() != recordNotRecorded {
		t.Fatal("registry add wrote into a runtime directory that has no graph")
	}
	// After the first run the same command records the same reference.
	runsSync(t, entry.StateDir, false, "start", 1)
	if registration() != recordNotRecorded {
		t.Fatal("the reference was recorded without a command that used it")
	}
	code, out, errs := registryRun(t, regDir, "add", "-c", entry.ConfigPath)
	if code != 0 || !strings.Contains(out, entry.ID) {
		t.Fatal(out, errs)
	}
	if registration() != recordRecorded {
		t.Fatal("repeating registry add did not record the reference")
	}
	g, _ := rungraph.Load(entry.StateDir)
	if len(g.Registrations) != 1 || g.Registrations[0].ID != entry.ID || g.Registrations[0].Source != rungraph.SourceRegistryAdd || len(g.Runs) != 1 {
		t.Fatalf("%#v", g.Registrations)
	}
	// Removing and adding again creates a new reference. It is recorded as
	// its own entry with its own time; the old one stays as history.
	if code, out, errs := registryRun(t, regDir, "remove", "--id", entry.ID); code != 0 {
		t.Fatal(out, errs)
	}
	if code, out, errs := registryRun(t, regDir, "add", "-c", entry.ConfigPath); code != 0 {
		t.Fatal(out, errs)
	}
	f, _ := registry.Load(regDir)
	g, _ = rungraph.Load(entry.StateDir)
	if len(f.Entries) != 1 || f.Entries[0].ID == entry.ID || len(g.Registrations) != 2 || g.Registrations[1].ID != f.Entries[0].ID {
		t.Fatalf("%#v %#v", f.Entries, g.Registrations)
	}
	// A control operation through the reference upgrades its label; a later
	// add does not downgrade it.
	current := f.Entries[0]
	if r, code := applyClientHold(context.Background(), regDir, current.ID, "pause"); code != 0 {
		t.Fatal(code, r)
	}
	registryRun(t, regDir, "add", "-c", entry.ConfigPath)
	if g, _ = rungraph.Load(entry.StateDir); g.Registrations[0].Source != rungraph.SourceRegistryAdd || g.Registrations[1].Source != rungraph.SourceRegistered {
		t.Fatalf("%#v", g.Registrations)
	}
	// An unusable graph is left alone, and a graph removed after its lock
	// was created is not recreated.
	path := filepath.Join(entry.StateDir, rungraph.FileName)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := registryRun(t, regDir, "add", "-c", entry.ConfigPath); code != 0 {
		t.Fatal(out, errs)
	}
	if b, _ := os.ReadFile(path); string(b) != "{" {
		t.Fatal("registry add rewrote an unusable graph")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	registryRun(t, regDir, "add", "-c", entry.ConfigPath)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("registry add created a graph")
	}
}

func TestRunsShowsRoutineInvocationsAndTriggers(t *testing.T) {
	dir := t.TempDir()
	nightly := func(invocation string) rd.RoutineRef {
		return rd.RoutineRef{Source: "example-scheduler", Routine: "nightly", Invocation: invocation}
	}
	if err := control.Save(dir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	recordRunBoundary(dir, "enable", nightly("inv-1"))
	recordRunBoundary(dir, "enable", nightly("inv-2")) // a later scheduled enable is a trigger of the open run
	for n := 3; n < 9; n++ {
		runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) { return g.Trigger(nightly(fmt.Sprintf("inv-%d", n)), runsAt) })
	}
	_, r, raw := runsExec(t, "--state-dir", dir, "--limit", "3")
	cur := r.Loops[0].Current
	if cur == nil || cur.Origin != rd.Explicit || cur.Invocation != "routine:example-scheduler/nightly/inv-1" || len(cur.Triggers) != 3 || cur.OmittedTriggers != 4 ||
		cur.Triggers[0].Ref != "routine:example-scheduler/nightly/inv-2" || cur.Triggers[0].Source != rungraph.SourceExternal || len(r.Loops[0].Recent) != 0 {
		t.Fatalf("%s", raw)
	}
	// routine → its runs, and an invocation → its run and routine, in text.
	var text, errs bytes.Buffer
	run(context.Background(), []string{"runs", "--state-dir", dir, "--id", "routine:example-scheduler/nightly", "--limit", "20"}, &text, &errs)
	for _, want := range []string{"(routine,", "run -> " + cur.ID, "invocation -> routine:example-scheduler/nightly/inv-1", "invocation -> routine:example-scheduler/nightly/inv-8",
		"opened by invocation: routine:example-scheduler/nightly/inv-1", "trigger while open: routine:example-scheduler/nightly/inv-2"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("lacks %q:\n%s", want, text.String())
		}
	}
	_, r, _ = runsExec(t, "--state-dir", dir, "--id", "routine:example-scheduler/nightly/inv-5")
	if r.Node.Status != rungraph.Found || r.Node.Type != rungraph.InvocationNode || len(r.Node.Edges) != 2 || r.Node.Edges[0].To != cur.ID || r.Node.Edges[1].To != "routine:example-scheduler/nightly" {
		t.Fatalf("%+v", r.Node)
	}
	if _, r, _ = runsExec(t, "--state-dir", dir, "--id", "routine:example-scheduler/weekly"); r.Node.Status != rungraph.NotRecorded {
		t.Fatal(r.Node.Status)
	}
}

func childKey(thread string) string { return childKeyOf(thread) }

// runsWait records a check at runsAt that observed the given children, and
// makes it the last recorded check.
func runsWait(t *testing.T, dir, listing string, children ...rungraph.Child) {
	t.Helper()
	runsClock(t, runsAt.Add(time.Minute))
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) { return g.Wait("primary", listing, children, runsAt) })
	if err := state.Save(dir, state.File{LastCheck: &state.Check{At: runsAt, Target: "primary", ObservedState: "active", Action: "none", Reason: "child work running", Outcome: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRunsBlockerComesOnlyFromACurrentCompleteChildListing(t *testing.T) {
	dir := runsFixture(t)
	working, stuck, failed := childKey("thr_child_working"), childKey("thr_child_stuck"), childKey("thr_child_failed")
	blocker := func() runsBlocker {
		t.Helper()
		code, r, raw := runsExec(t, "--state-dir", dir)
		if code != exitOK {
			t.Fatal(code, raw)
		}
		for _, id := range []string{"thr_child_working", "thr_child_stuck", "thr_child_failed"} {
			if strings.Contains(raw, id) {
				t.Fatalf("child thread ID %s in output", id)
			}
		}
		return r.Loops[0].Blocker
	}
	runsWait(t, dir, rungraph.ListingComplete, rungraph.Child{Key: working, Status: rungraph.ChildActive}, rungraph.Child{Key: stuck, Status: rungraph.ChildAwaiting}, rungraph.Child{Key: failed, Status: rungraph.ChildErrored})
	b := blocker()
	if b.Availability != blockerBlocked || b.Reason != "" || b.CheckedAt == nil || !b.CheckedAt.Equal(runsAt) || b.Target != "primary" || len(b.Children) != 3 {
		t.Fatalf("%+v", b)
	}
	for i, want := range []runsChild{
		{Ref: "child:" + working, Status: "active", Holds: true}, {Ref: "child:" + stuck, Status: "awaiting_input", Holds: true}, {Ref: "child:" + failed, Status: "error", Holds: false},
	} {
		got := b.Children[i]
		if got.Ref != want.Ref || got.Status != want.Status || got.Holds != want.Holds || got.Supervised != (runsSupervised{Availability: "registry_not_consulted"}) {
			t.Fatalf("%+v", got)
		}
	}
	// The run links to the children that hold it, and each links back.
	_, r, _ := runsExec(t, "--state-dir", dir, "--id", runsID("run_", 2))
	var blockedBy, listed []string
	for _, e := range r.Node.Edges {
		switch e.Type {
		case rungraph.EdgeBlockedBy:
			blockedBy = append(blockedBy, e.To)
		case rungraph.EdgeChild:
			listed = append(listed, e.To)
		}
	}
	if strings.Join(blockedBy, " ") != "child:"+working+" child:"+stuck || strings.Join(listed, " ") != "child:"+failed {
		t.Fatalf("%v %v", blockedBy, listed)
	}
	_, r, _ = runsExec(t, "--state-dir", dir, "--id", "child:"+stuck)
	if r.Node.Status != rungraph.Found || r.Node.Type != rungraph.ChildNode || len(r.Node.Edges) != 1 || r.Node.Edges[0].Type != rungraph.EdgeBlocks || r.Node.Edges[0].To != runsID("run_", 2) || r.Node.Source != rungraph.SourceChildren {
		t.Fatalf("%+v", r.Node)
	}
	var text, errs bytes.Buffer
	run(context.Background(), []string{"runs", "--state-dir", dir}, &text, &errs)
	for _, want := range []string{"waiting on: blocked", "child child:" + working + "  active  held the run at that check", "child child:" + failed + "  error  does not hold the run", "lock holder: unavailable"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("lacks %q:\n%s", want, text.String())
		}
	}

	// Only errored children, or none: every child was verified and none holds the run.
	runsWait(t, dir, rungraph.ListingComplete, rungraph.Child{Key: failed, Status: rungraph.ChildErrored})
	if b = blocker(); b.Availability != blockerNone || len(b.Children) != 1 || b.Children[0].Holds {
		t.Fatalf("%+v", b)
	}
	runsWait(t, dir, rungraph.ListingComplete)
	if b = blocker(); b.Availability != blockerNone || len(b.Children) != 0 {
		t.Fatalf("%+v", b)
	}
	// An incomplete listing is unknown and names no child, even a verified one.
	runsWait(t, dir, rungraph.ListingIncomplete, rungraph.Child{Key: working, Status: rungraph.ChildActive})
	if b = blocker(); b.Availability != blockerUnknown || b.Reason != "child_listing_incomplete" || len(b.Children) != 0 {
		t.Fatalf("%+v", b)
	}
	// A later check that did not refresh the record leaves it stale: unknown.
	runsWait(t, dir, rungraph.ListingComplete, rungraph.Child{Key: working, Status: rungraph.ChildActive})
	for name, later := range map[string]state.File{
		"a later check":      {LastCheck: &state.Check{At: runsAt.Add(10 * time.Minute)}},
		"an earlier check":   {LastCheck: &state.Check{At: runsAt.Add(-time.Second)}},
		"another target":     {ActiveTarget: "example-fallback", LastCheck: &state.Check{At: runsAt}},
		"no check on record": {},
	} {
		if err := state.Save(dir, later); err != nil {
			t.Fatal(err)
		}
		want := "not_refreshed_by_the_last_check"
		if later.LastCheck == nil {
			want = "last_check_not_recorded"
		}
		if b = blocker(); b.Availability != blockerUnknown || b.Reason != want || len(b.Children) != 0 {
			t.Fatalf("%s: %+v", name, b)
		}
	}
	// A closed run waits on nothing that is recorded.
	runsSync(t, dir, true, "complete", 0)
	if b = blocker(); b.Availability != blockerUnknown || b.Reason != "no_open_run" || b.CheckedAt != nil {
		t.Fatalf("%+v", b)
	}
	if g, _ := rungraph.Load(dir); g.Waiting != nil {
		t.Fatal("the waiting record outlived its run")
	}
}

func runsLoopConfig(t *testing.T, name, thread string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keepgoing.yaml")
	body := fmt.Sprintf("version: 1\nloop:\n  name: %s\n  state_dir: state\n  adapter: bb\n  provider: example\n  bb: {thread: %s, cli: nonexistent-provider-fixture}\n  prompt: Continue the fictional loop.\n", name, thread)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Two supervised loops run at once: the parent waits on a child conversation
// that is itself a registered loop. The reader names that loop's run and chat,
// and reading it changes nothing in either loop.
func TestRunsBlockerResolvesToTheBlockingLoopsRunAndChat(t *testing.T) {
	regDir := filepath.Join(t.TempDir(), "registry")
	add := func(name, thread string) registry.Entry {
		t.Helper()
		cfg := runsLoopConfig(t, name, thread)
		if code, out, errs := registryRun(t, regDir, "add", "-c", cfg); code != 0 {
			t.Fatal(out, errs)
		}
		f, _ := registry.Load(regDir)
		return f.Entries[len(f.Entries)-1]
	}
	start := func(entry registry.Entry, thread string, run int) {
		t.Helper()
		if err := control.Save(entry.StateDir, control.File{Enabled: true}); err != nil {
			t.Fatal(err)
		}
		runsSync(t, entry.StateDir, false, "start", run)
		runsUpdate(t, entry.StateDir, func(g *rungraph.Graph) (bool, error) {
			return g.Relate(rtSession("primary", "bb", thread, rungraph.SourceConfigured))
		})
	}
	parent, child := add("example-parent", "thr_example_parent"), add("example-child", "thr_example_child")
	// A loop that shares the child's name but supervises another conversation
	// must never be taken for it.
	decoy := add("example-child", "thr_example_other")
	start(parent, "thr_example_parent", 1)
	start(child, "thr_example_child", 2)
	start(decoy, "thr_example_other", 3)
	key := childKey("thr_example_child")
	runsWait(t, parent.StateDir, rungraph.ListingComplete, rungraph.Child{Key: key, Status: rungraph.ChildActive}, rungraph.Child{Key: childKey("thr_example_unsupervised"), Status: rungraph.ChildAwaiting})
	before := snapshotTree(t, regDir) + snapshotTree(t, parent.StateDir) + snapshotTree(t, child.StateDir) + snapshotTree(t, decoy.StateDir)

	for name, args := range map[string][]string{
		"registry selector":       {"--registry-id", parent.ID, "--directory", regDir},
		"state dir with registry": {"--state-dir", parent.StateDir, "--directory", regDir},
	} {
		code, r, raw := runsExec(t, args...)
		b := r.Loops[0].Blocker
		if code != exitOK || b.Availability != blockerBlocked || len(b.Children) != 2 {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
		blocking := b.Children[0]
		if blocking.Ref != "child:"+key {
			blocking = b.Children[1]
		}
		want := runsSupervised{Availability: recordRecorded, RegistrationID: child.ID, Loop: "example-child", Run: runsID("run_", 2)}
		if blocking.Supervised != want {
			t.Fatalf("%s: %+v", name, blocking.Supervised)
		}
		for _, c := range b.Children {
			if c.Ref != "child:"+key && c.Supervised != (runsSupervised{Availability: "not_registered"}) {
				t.Fatalf("%s: an unsupervised child was resolved: %+v", name, c)
			}
		}
		if strings.Contains(raw, "thr_example_child") || strings.Contains(raw, "thr_example_unsupervised") || strings.Contains(raw, decoy.ID) || strings.Contains(raw, parent.StateDir) || strings.Contains(raw, child.StateDir) {
			t.Fatalf("%s: %s", name, raw)
		}
	}
	// Following the link: the blocking loop's run and its chat resolve through its own reference.
	code, r, _ := runsExec(t, "--registry-id", child.ID, "--directory", regDir, "--id", runsID("run_", 2))
	if code != exitOK || r.Node.Status != rungraph.Found || r.Node.Run.Sessions[0].Ref != "session:bb/thr_example_child" {
		t.Fatal(code, r.Node)
	}
	// Without a registry the child is still shown, but no loop is named.
	_, r, _ = runsExec(t, "--state-dir", parent.StateDir)
	if got := r.Loops[0].Blocker.Children[0].Supervised; got != (runsSupervised{Availability: "registry_not_consulted"}) {
		t.Fatalf("%+v", got)
	}
	var text, errs bytes.Buffer
	run(context.Background(), []string{"runs", "--registry-id", parent.ID, "--directory", regDir}, &text, &errs)
	for _, want := range []string{"a registered loop: " + child.ID + " (example-child)  run " + runsID("run_", 2) + "\n", "follow it with: keepgoing runs --registry-id " + child.ID, "its loop: unavailable (not_registered)"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("lacks %q:\n%s", want, text.String())
		}
	}
	if after := snapshotTree(t, regDir) + snapshotTree(t, parent.StateDir) + snapshotTree(t, child.StateDir) + snapshotTree(t, decoy.StateDir); after != before {
		t.Fatal("reading a blocker changed a registry or a loop")
	}

	// The blocking loop has no open run for that conversation: the loop is
	// named, the run is not.
	runsSync(t, child.StateDir, true, "complete", 0)
	_, r, _ = runsExec(t, "--registry-id", parent.ID, "--directory", regDir)
	for _, c := range r.Loops[0].Blocker.Children {
		if c.Ref == "child:"+key && (c.Supervised.Availability != recordRecorded || c.Supervised.Run != "" || c.Supervised.RunReason == "" || c.Supervised.RegistrationID != child.ID) {
			t.Fatalf("%+v", c.Supervised)
		}
	}
	// Two registered loops name the same conversation: ambiguous, none picked.
	add("example-duplicate", "thr_example_child")
	_, r, _ = runsExec(t, "--registry-id", parent.ID, "--directory", regDir)
	for _, c := range r.Loops[0].Blocker.Children {
		if c.Ref == "child:"+key && c.Supervised != (runsSupervised{Availability: "ambiguous"}) {
			t.Fatalf("%+v", c.Supervised)
		}
	}
	// A blocking loop whose binding no longer validates is not a match.
	f, _ := registry.Load(regDir)
	for _, e := range f.Entries {
		if e.Loop == "example-duplicate" || e.ID == child.ID {
			os.Remove(e.ConfigPath)
		}
	}
	_, r, _ = runsExec(t, "--registry-id", parent.ID, "--directory", regDir)
	for _, c := range r.Loops[0].Blocker.Children {
		if c.Ref == "child:"+key && c.Supervised != (runsSupervised{Availability: "not_registered"}) {
			t.Fatalf("%+v", c.Supervised)
		}
	}
}

// The owner is read from the validated claim in control.json. A command run
// from another conversation is told it is refused; nothing is claimed.
func TestRunsShowsTheOwningConversationWithoutGrantingControl(t *testing.T) {
	dir := runsFixture(t)
	save := func(f control.File) {
		t.Helper()
		f.Enabled = true
		if err := control.Save(dir, f); err != nil {
			t.Fatal(err)
		}
	}
	owner := func(args ...string) (runsOwner, string) {
		t.Helper()
		code, r, raw := runsExec(t, append([]string{"--state-dir", dir}, args...)...)
		if code != exitOK {
			t.Fatal(code, raw)
		}
		return r.Loops[0].Blocker.Owner, raw
	}
	t.Setenv("BB_THREAD_ID", "")
	if o, _ := owner(); o.Availability != recordNotRecorded || o.BlocksCaller || o.Ref != "" {
		t.Fatalf("%+v", o)
	}
	save(control.File{Ownership: &control.Ownership{ThreadID: "thr_example_owner", Generation: 3}})
	before := snapshotTree(t, dir)
	o, _ := owner()
	if o.Availability != recordRecorded || o.Ref != "session:bb/thr_example_owner" || o.Generation != 3 || o.BlocksCaller || o.Supervised.Availability != "registry_not_consulted" {
		t.Fatalf("%+v", o)
	}
	// Run from another conversation: refused, and the caller is not printed.
	t.Setenv("BB_THREAD_ID", "thr_example_caller")
	o, raw := owner()
	if !o.BlocksCaller || strings.Contains(raw, "thr_example_caller") {
		t.Fatalf("%+v %s", o, raw)
	}
	var text, errs bytes.Buffer
	run(context.Background(), []string{"runs", "--state-dir", dir}, &text, &errs)
	if !strings.Contains(text.String(), "owner: session:bb/thr_example_owner (generation 3)  this conversation is not the owner") || strings.Contains(text.String(), "thr_example_caller") {
		t.Fatal(text.String())
	}
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	if o, _ = owner(); o.BlocksCaller {
		t.Fatal("the owner was told it is refused")
	}
	// Reading as a non-owner claims nothing and changes nothing.
	t.Setenv("BB_THREAD_ID", "thr_example_caller")
	owner("--id", "session:bb/thr_example_a")
	ctl, _ := control.Load(dir)
	if snapshotTree(t, dir) != before || ctl.Ownership.ThreadID != "thr_example_owner" || ctl.Ownership.Generation != 3 {
		t.Fatal("a read changed ownership or runtime records")
	}
	// Evidence that does not validate is unknown, never a guess.
	for name, f := range map[string]control.File{
		"generation zero":  {Ownership: &control.Ownership{ThreadID: "thr_example_owner"}},
		"malformed owner":  {Ownership: &control.Ownership{ThreadID: "not a thread", Generation: 1}},
		"transfer pending": {Ownership: &control.Ownership{ThreadID: "thr_example_owner", Generation: 3}, PendingTransfer: &control.PendingBBTransfer{}},
	} {
		save(f)
		if o, raw := owner(); o.Availability != blockerUnknown || o.Reason == "" || o.Ref != "" || o.BlocksCaller || strings.Contains(raw, "not a thread") {
			t.Fatalf("%s: %+v", name, o)
		}
	}
}

// Registered views withhold the owning thread, so the registry selectors show
// it by key. When the owner is itself a registered loop, that loop is named.
func TestRunsRegistryViewWithholdsTheOwnerButNamesItsLoop(t *testing.T) {
	regDir := filepath.Join(t.TempDir(), "registry")
	var entries []registry.Entry
	for _, l := range [][2]string{{"example-owned", "thr_example_owned"}, {"example-owner", "thr_example_owner"}} {
		if code, out, errs := registryRun(t, regDir, "add", "-c", runsLoopConfig(t, l[0], l[1])); code != 0 {
			t.Fatal(out, errs)
		}
		f, _ := registry.Load(regDir)
		entries = append(entries, f.Entries[len(f.Entries)-1])
	}
	owned, owner := entries[0], entries[1]
	if err := control.Save(owned.StateDir, control.File{Enabled: true, Ownership: &control.Ownership{ThreadID: "thr_example_owner", Generation: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := control.Save(owner.StateDir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	runsSync(t, owner.StateDir, false, "start", 7)
	runsUpdate(t, owner.StateDir, func(g *rungraph.Graph) (bool, error) {
		return g.Relate(rtSession("primary", "bb", "thr_example_owner", rungraph.SourceConfigured))
	})
	t.Setenv("BB_THREAD_ID", "thr_example_caller")
	for _, args := range [][]string{{"--registry-id", owned.ID}, {"--all"}} {
		code, r, raw := runsExec(t, append(args, "--directory", regDir)...)
		o := r.Loops[0].Blocker.Owner
		want := runsSupervised{Availability: recordRecorded, RegistrationID: owner.ID, Loop: "example-owner", Run: runsID("run_", 7)}
		if code != exitOK || o.Ref != "owner:"+childKey("thr_example_owner") || o.Generation != 2 || !o.BlocksCaller || o.Supervised != want {
			t.Fatalf("%v: %d %+v", args, code, o)
		}
		// The caller is never printed. Selecting only the owned loop must not
		// print the owning thread either; --all also lists the owner's own
		// loop, whose chat reference is its own to show.
		if strings.Contains(raw, "thr_example_caller") || (args[0] == "--registry-id" && strings.Contains(raw, "thr_example_owner")) {
			t.Fatalf("%v: a withheld thread was printed: %s", args, raw)
		}
	}
	// With a configuration selector the owner is shown, as status shows it.
	_, r, _ := runsExec(t, "--state-dir", owned.StateDir, "--directory", regDir)
	if o := r.Loops[0].Blocker.Owner; o.Ref != "session:bb/thr_example_owner" || o.Supervised.RegistrationID != owner.ID || o.Supervised.Run != runsID("run_", 7) {
		t.Fatalf("%+v", o)
	}
}

// A listing the loop view does not show as current must not be followable by
// exact ID either, in any view.
func TestRunsStaleChildListingIsNotFollowableByID(t *testing.T) {
	dir := runsFixture(t)
	key := childKey("thr_child_working")
	runsWait(t, dir, rungraph.ListingComplete, rungraph.Child{Key: key, Status: rungraph.ChildActive})
	blockedEdges := func() (child *runsNode, runEdges int) {
		t.Helper()
		_, r, _ := runsExec(t, "--state-dir", dir, "--id", "child:"+key)
		child = r.Node
		_, r, _ = runsExec(t, "--state-dir", dir, "--id", runsID("run_", 2))
		for _, e := range r.Node.Edges {
			if e.Type == rungraph.EdgeBlockedBy || e.Type == rungraph.EdgeChild || e.Type == rungraph.EdgeBlocks {
				runEdges++
			}
		}
		return child, runEdges
	}
	if child, edges := blockedEdges(); child.Status != rungraph.Found || edges != 1 {
		t.Fatalf("%+v %d", child, edges)
	}
	// A held check came later and observed nothing.
	if err := state.Save(dir, state.File{LastCheck: &state.Check{At: runsAt.Add(30 * time.Second), Reason: "paused"}}); err != nil {
		t.Fatal(err)
	}
	child, edges := blockedEdges()
	if child.Status != recordUnavailable || child.Reason != "not_refreshed_by_the_last_check" || len(child.Edges) != 0 || edges != 0 {
		t.Fatalf("%+v %d", child, edges)
	}
	var text, errs bytes.Buffer
	run(context.Background(), []string{"runs", "--state-dir", dir, "--id", "child:" + key}, &text, &errs)
	if strings.Contains(text.String(), "blocks ->") || !strings.Contains(text.String(), "reference child:"+key+": unavailable (not_refreshed_by_the_last_check") {
		t.Fatal(text.String())
	}
	text.Reset()
	run(context.Background(), []string{"runs", "--state-dir", dir, "--id", runsID("run_", 2)}, &text, &errs)
	if strings.Contains(text.String(), "blocked_by") || strings.Contains(text.String(), "child:"+key) {
		t.Fatal(text.String())
	}
}

// Equal check times are not enough: if checks stop, the listing ages. An old
// listing is unknown, with its age shown.
func TestRunsChildListingOlderThanStaleAfterIsUnknown(t *testing.T) {
	dir := runsFixture(t)
	key := childKey("thr_child_working")
	runsWait(t, dir, rungraph.ListingComplete, rungraph.Child{Key: key, Status: rungraph.ChildActive})
	read := func(args ...string) runsBlocker {
		t.Helper()
		_, r, _ := runsExec(t, append([]string{"--state-dir", dir}, args...)...)
		return r.Loops[0].Blocker
	}
	if b := read(); b.Availability != blockerBlocked || b.Stale || *b.AgeSeconds != 60 {
		t.Fatalf("%+v", b)
	}
	runsClock(t, runsAt.Add(2*time.Hour)) // checks stopped two hours ago
	b := read()
	if b.Availability != blockerUnknown || b.Reason != "last_check_older_than_stale_after" || !b.Stale || *b.AgeSeconds != 7200 || len(b.Children) != 0 {
		t.Fatalf("%+v", b)
	}
	if _, r, _ := runsExec(t, "--state-dir", dir, "--id", "child:"+key); r.Node.Status != recordUnavailable {
		t.Fatalf("an aged listing was followable: %+v", r.Node)
	}
	var text, errs bytes.Buffer
	run(context.Background(), []string{"runs", "--state-dir", dir}, &text, &errs)
	if !strings.Contains(text.String(), "waiting on: unknown (last_check_older_than_stale_after)") || !strings.Contains(text.String(), "2h0m0s ago (stale)") {
		t.Fatal(text.String())
	}
	// A longer threshold, for a slower scheduler, accepts it.
	if b = read("--stale-after", "3h"); b.Availability != blockerBlocked || b.Stale {
		t.Fatalf("%+v", b)
	}
	var out bytes.Buffer
	if code := run(context.Background(), []string{"runs", "--state-dir", dir, "--stale-after", "0s"}, &out, &errs); code != exitUsage {
		t.Fatal(code)
	}
}

// A child is said to hold the run only when child work is what the last check
// recorded. With a stop requested, the same children are listed, not blocking.
func TestRunsChildrenHoldTheRunOnlyWhenTheCheckSaidSo(t *testing.T) {
	dir := runsFixture(t)
	key := childKey("thr_child_working")
	runsWait(t, dir, rungraph.ListingComplete, rungraph.Child{Key: key, Status: rungraph.ChildActive})
	for reason, want := range map[string]struct{ availability, cause string }{
		"child work running": {blockerBlocked, "child_busy"},
		"children waiting on a person (pending interaction): thr_child_working": {blockerBlocked, "child_input"},
		"stop requested; session not running":                                   {blockerListed, "stop"},
		"children in error while other children run":                            {blockerListed, "child_error"},
		"idle":                              {blockerListed, "idle"},
		"a reason this build does not know": {blockerListed, "other"},
	} {
		if err := state.Save(dir, state.File{LastCheck: &state.Check{At: runsAt, Reason: reason}}); err != nil {
			t.Fatal(err)
		}
		code, r, raw := runsExec(t, "--state-dir", dir)
		b := r.Loops[0].Blocker
		holds := want.availability == blockerBlocked
		if code != exitOK || b.Availability != want.availability || b.LastCheckCause != want.cause || len(b.Children) != 1 || b.Children[0].Holds != holds || strings.Contains(raw, "thr_child_working") || strings.Contains(raw, "a reason this build") {
			t.Fatalf("%q: %s", reason, raw)
		}
		var text, errs bytes.Buffer
		run(context.Background(), []string{"runs", "--state-dir", dir, "--id", runsID("run_", 2)}, &text, &errs)
		if strings.Contains(text.String(), "blocked_by") != holds || strings.Contains(text.String(), "held the run at that check") != holds {
			t.Fatalf("%q: %s", reason, text.String())
		}
		if !holds && (b.Reason != "last_check_recorded_another_reason" || !strings.Contains(text.String(), "does not hold the run") || !strings.Contains(text.String(), "child -> child:"+key)) {
			t.Fatalf("%q: %s", reason, text.String())
		}
	}
}

// A stuck child puts the loop on an awaiting-user hold, and held checks stop
// observing. The last verified listing stays visible, labelled with the hold
// and its age, instead of vanishing after one check.
func TestRunsKeepsTheStuckChildVisibleDuringItsHold(t *testing.T) {
	dir := runsFixture(t)
	stuck, working := childKey("thr_child_stuck"), childKey("thr_child_working")
	runsWait(t, dir, rungraph.ListingComplete, rungraph.Child{Key: stuck, Status: rungraph.ChildAwaiting}, rungraph.Child{Key: working, Status: rungraph.ChildActive})
	held := func() {
		t.Helper()
		if err := control.Save(dir, control.File{Enabled: true, AwaitingUser: true}); err != nil {
			t.Fatal(err)
		}
		if err := state.Save(dir, state.File{LastCheck: &state.Check{At: runsAt.Add(20 * time.Minute), Reason: "awaiting user"}}); err != nil {
			t.Fatal(err)
		}
	}
	held()
	runsClock(t, runsAt.Add(26*time.Hour))
	_, r, _ := runsExec(t, "--state-dir", dir)
	b := r.Loops[0].Blocker
	if b.Availability != blockerHeld || !strings.HasPrefix(b.Reason, "awaiting_user_hold") || !b.Stale || *b.AgeSeconds != 26*3600 || len(b.Children) != 2 || !b.Children[0].Holds || b.LastCheckCause != "awaiting_user" {
		t.Fatalf("%+v", b)
	}
	if _, r, _ = runsExec(t, "--state-dir", dir, "--id", "child:"+stuck); r.Node.Status != rungraph.Found || r.Node.Edges[0].Type != rungraph.EdgeBlocks {
		t.Fatalf("%+v", r.Node)
	}
	var text, errs bytes.Buffer
	run(context.Background(), []string{"runs", "--state-dir", dir}, &text, &errs)
	if !strings.Contains(text.String(), "waiting on: held (awaiting_user_hold") || !strings.Contains(text.String(), "26h0m0s ago (stale)") {
		t.Fatal(text.String())
	}
	// Once the hold is cleared, or if no child was stuck, the old listing is
	// not presented: a later check has not refreshed it.
	if err := control.Save(dir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, r, _ = runsExec(t, "--state-dir", dir); r.Loops[0].Blocker.Availability != blockerUnknown || r.Loops[0].Blocker.Reason != "not_refreshed_by_the_last_check" {
		t.Fatalf("%+v", r.Loops[0].Blocker)
	}
	runsWait(t, dir, rungraph.ListingComplete, rungraph.Child{Key: working, Status: rungraph.ChildActive})
	held()
	if _, r, _ = runsExec(t, "--state-dir", dir); r.Loops[0].Blocker.Availability != blockerUnknown {
		t.Fatalf("an awaiting-user hold with no stuck child kept the listing: %+v", r.Loops[0].Blocker)
	}
}

// A loop is never reported as its own blocker, whichever way it is selected,
// and an unreadable registry is an error, not "nothing registered".
func TestRunsNeverNamesTheLoopItselfAndRejectsAnUnreadableRegistry(t *testing.T) {
	regDir, entry := clientFixture(t) // its configuration uses a relative state_dir
	if err := control.Save(entry.StateDir, control.File{Enabled: true, Ownership: &control.Ownership{ThreadID: "thr_example", Generation: 1}}); err != nil {
		t.Fatal(err)
	}
	runsSync(t, entry.StateDir, false, "start", 1)
	t.Setenv("BB_THREAD_ID", "")
	for name, args := range map[string][]string{
		"configuration": {"-c", entry.ConfigPath, "--directory", regDir},
		"state dir":     {"--state-dir", entry.StateDir, "--directory", regDir},
		"registry":      {"--registry-id", entry.ID, "--directory", regDir},
	} {
		code, r, raw := runsExec(t, args...)
		if code != exitOK || r.Loops[0].Blocker.Owner.Availability != recordRecorded || r.Loops[0].Blocker.Owner.Supervised != (runsSupervised{Availability: "not_registered"}) {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
	}
	// A registry file that cannot be read stops the command.
	if err := os.WriteFile(filepath.Join(regDir, registry.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--state-dir", entry.StateDir, "--directory", regDir}, {"--all", "--directory", regDir}} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"runs"}, args...), &out, &errs); code != exitFailed || out.Len() != 0 || strings.Contains(errs.String(), regDir) {
			t.Fatalf("%v: %d %s %s", args, code, out.String(), errs.String())
		}
	}
}
