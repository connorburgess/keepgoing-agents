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
func runsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := control.Save(dir, control.File{Enabled: true, Note: "PRIVATE-NOTE"}); err != nil {
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
	if code != exitOK || r.SchemaVersion != 1 || r.View != "loop" || len(r.Loops) != 1 || r.Node != nil || len(r.Limitations) == 0 {
		t.Fatalf("%d %s", code, raw)
	}
	l := r.Loops[0]
	if l.Graph.Availability != rungraph.Available || !strings.HasPrefix(l.Graph.LoopID, "supervision_") || l.Graph.UpdatedAt == nil || l.Control.State != "enabled" ||
		l.ActiveTarget != (runsTarget{Availability: "recorded", Alias: "primary"}) || l.Conflict != "" || l.Blocker.Availability != "unavailable" {
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
	runsUpdate(t, dir, func(g *rungraph.Graph) (bool, error) { return g.Register(runsID("loop_", 7), runsAt) })
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
	if code != exitOK || l.Graph.Availability != rungraph.Missing || l.Current != nil || len(l.Recent) != 0 || l.Control.State != "disabled" || l.Graph.LoopID != "" {
		t.Fatalf("%d %s", code, raw)
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
			if code != exitFailed || l.Graph.Availability != tt.want || l.Current != nil || l.Control.State != "enabled" || l.Conflict != "" || r.Node.Status != rungraph.NotRecorded {
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
		return g.Register(entries[0].ID, runsAt)
	})
	before := snapshotTree(t, regDir) + snapshotTree(t, entries[0].StateDir)
	code, r, raw := runsExec(t, "--all", "--directory", regDir)
	if code != exitOK || r.View != "registry" || len(r.Loops) != 2 || r.Omitted != 0 {
		t.Fatalf("%d %s", code, raw)
	}
	first, second := r.Loops[0], r.Loops[1]
	if first.RegistrationID != entries[0].ID || first.Registration != "recorded" || first.Loop != "example-loop" || first.Current == nil || first.Current.ID != runsID("run_", 1) ||
		first.Control.State != "paused" || first.Current.Sessions[0].Ref != "session:bb/thr_example" || len(first.Recent) != 0 {
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
	for _, private := range []string{entries[0].StateDir, entries[0].ConfigPath, "private-missing-task"} {
		if strings.Contains(raw, private) {
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
		{"--state-dir", dir, "--directory", dir},
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
