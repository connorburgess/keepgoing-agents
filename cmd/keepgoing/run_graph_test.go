//go:build unix

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
)

func TestRunGraphProcessHelper(t *testing.T) {
	if os.Getenv("KEEPGOING_GRAPH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	args := strings.Split(os.Getenv("KEEPGOING_GRAPH_ARGS"), "\x1f")
	os.Exit(run(context.Background(), args, os.Stdout, os.Stderr))
}

// graphProcess prepares the real command entrypoint in a separate OS process.
// Nothing but the state directory carries over between these processes.
func graphProcess(f *fixture, args ...string) *exec.Cmd {
	c := exec.Command(os.Args[0], "-test.run=^TestRunGraphProcessHelper$")
	c.Env = append(os.Environ(), "KEEPGOING_GRAPH_HELPER=1", "KEEPGOING_GRAPH_ARGS="+strings.Join(append(args, "-c", f.cfg), "\x1f"))
	return c
}

func graphOf(t *testing.T, f *fixture) rungraph.Graph {
	t.Helper()
	g, availability := rungraph.Load(filepath.Join(f.dir, "state"))
	if availability != rungraph.Available {
		t.Fatalf("graph is %s", availability)
	}
	return g
}

// The D1 restart criterion: a run reference recorded by one process is the
// current run for every later process that reads the same state directory,
// including a daemon started after the previous daemon was killed.
func TestRunReferenceSurvivesSeparateProcessesAndDaemonRestart(t *testing.T) {
	f := newFixture(t)
	stateDir := filepath.Join(f.dir, "state")
	command := func(args ...string) {
		t.Helper()
		if out, err := graphProcess(f, args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	command("enable")
	g := graphOf(t, f)
	loop, run := g.LoopID, g.CurrentRun
	if run == "" || g.Runs[0].Origin != rd.Explicit || g.Runs[0].Source != rungraph.SourceControl || len(g.Attempts) != 0 {
		t.Fatalf("%#v", g)
	}
	if info, err := os.Stat(filepath.Join(stateDir, rungraph.FileName)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	command("tick")
	g = graphOf(t, f)
	first := g.Attempts[0]
	if len(g.Attempts) != 1 || first.Run != run || first.Action != "wake" || first.Outcome != rd.AdapterReturned {
		t.Fatalf("%#v", g.Attempts)
	}
	current, _ := g.Current()
	if sessions := g.Follow(current, rungraph.EdgeSession); len(sessions) != 1 || sessions[0].Ref != rungraph.SessionRef("bb", "thr_primary") {
		t.Fatalf("%#v", sessions)
	}

	// Two daemons in turn. The first is killed without a chance to clean up.
	for restart := 0; restart < 2; restart++ {
		before := graphOf(t, f).UpdatedAt
		var out safeBuffer
		daemon := graphProcess(f, "daemon")
		daemon.Stdout = &out
		if err := daemon.Start(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return strings.Contains(out.String(), "\n") }, "daemon tick")
		if err := daemon.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		daemon.Wait()
		g = graphOf(t, f)
		if g.LoopID != loop || g.CurrentRun != run || len(g.Runs) != 1 || g.Completed {
			t.Fatalf("daemon %d changed the run reference: %#v", restart, g)
		}
		if g.UpdatedAt.Before(before) {
			t.Fatal("graph went backwards")
		}
	}
	// Every send across those processes is its own attempt in the one run,
	// and the first process's attempt is still the first record.
	seen := map[string]bool{}
	for _, a := range g.Attempts {
		if seen[a.ID] || a.Run != run {
			t.Fatalf("%#v", g.Attempts)
		}
		seen[a.ID] = true
	}
	if g.Attempts[0] != first {
		t.Fatalf("the earlier attempt changed: %#v", g.Attempts[0])
	}

	// Holds keep the run; completion closes it; only resume opens the next.
	for _, cmd := range []string{"pause", "stop", "disable", "enable", "resume", "tick"} {
		command(cmd)
		if g = graphOf(t, f); g.CurrentRun != run || len(g.Runs) != 1 || g.Completed {
			t.Fatalf("%s changed the run: %#v", cmd, g)
		}
	}
	command("complete")
	command("tick")
	command("enable")
	g = graphOf(t, f)
	if !g.Completed || g.CurrentRun != run || g.Runs[0].ClosedAt.IsZero() || g.Runs[0].ClosedSource != rungraph.SourceControl || len(g.Runs) != 1 {
		t.Fatalf("%#v", g)
	}
	command("resume")
	command("tick")
	g = graphOf(t, f)
	next, _ := g.Current()
	if g.LoopID != loop || next.Ref == run || next.Run.Origin != rd.Reopened || next.Run.Reopens != run || len(g.Runs) != 2 {
		t.Fatalf("%#v", g.Runs)
	}
	// The reopened run reuses the same chat. Neither run is the other's parent
	// beyond the recorded predecessor.
	chat, _ := g.Lookup(rungraph.SessionRef("bb", "thr_primary"))
	if runs := g.Follow(chat, rungraph.EdgeRun); len(runs) != 2 || runs[0].Ref != run || runs[1].Ref != next.Ref {
		t.Fatalf("%#v", runs)
	}
}

// A completion saved while the graph could not be written is reported as a
// conflict by a reader and recorded by the next check, never guessed.
func TestLostRunBoundaryIsReportedThenRecordedFromControl(t *testing.T) {
	f := newFixture(t)
	stateDir := filepath.Join(f.dir, "state")
	f.run("enable")
	run := graphOf(t, f).CurrentRun
	path := filepath.Join(stateDir, rungraph.FileName)
	saved, _ := os.ReadFile(path)
	// Make the record unwritable for one command: a directory in its place.
	os.Remove(path)
	os.Mkdir(path, 0o700)
	if code, out, errs := f.run("complete"); code != 0 || !strings.Contains(out, "completed") {
		t.Fatal(code, out, errs)
	}
	os.Remove(path)
	write(t, path, string(saved), 0o600)
	ctl, _ := control.Load(stateDir)
	g := graphOf(t, f)
	if !ctl.Completed || g.Completed || g.Conflict(rungraph.GateOf(ctl)) != rungraph.ConflictCompletionNotRecorded {
		t.Fatalf("control=%+v graph=%#v", ctl, g)
	}
	f.tick()
	g = graphOf(t, f)
	if !g.Completed || g.CurrentRun != run || g.Runs[0].ClosedSource != rungraph.SourceReconciled || g.Conflict(rungraph.GateOf(ctl)) != "" {
		t.Fatalf("%#v", g)
	}
}

// A command that records after a later command has saved must not apply its
// own older view of control over the newer one.
func TestRunBoundaryIsRecordedFromCurrentControlNotTheCallersSnapshot(t *testing.T) {
	f := newFixture(t)
	stateDir := filepath.Join(f.dir, "state")
	f.run("enable")
	run := graphOf(t, f).CurrentRun
	// A "complete" whose save was already undone by a resume records late.
	recordRunBoundary(stateDir, "complete")
	if g := graphOf(t, f); g.Completed || g.CurrentRun != run || len(g.Runs) != 1 {
		t.Fatalf("%#v", g)
	}
}

// From real commands to the reader: the run a tick recorded is what `runs`
// shows, and reading it never calls the provider or changes a hold.
func TestRunsCommandShowsWhatTicksRecordedWithoutProviderCalls(t *testing.T) {
	f := newFixture(t)
	stateDir := filepath.Join(f.dir, "state")
	f.run("enable")
	f.tick()
	g := graphOf(t, f)
	for _, hold := range []string{"", "pause", "stop", "complete"} {
		if hold != "" {
			if code, _, errs := f.run(hold); code != 0 {
				t.Fatal(hold, errs)
			}
		}
		calls, before := f.read("calls"), snapshotTree(t, stateDir)
		code, out, errs := f.run("runs", "--json")
		var r runsReport
		if code != exitOK || json.Unmarshal([]byte(out), &r) != nil {
			t.Fatalf("%s: %d %s %s", hold, code, out, errs)
		}
		l := r.Loops[0]
		if l.Loop != "example-loop" || l.Graph.LoopID != g.LoopID || l.Current == nil || l.Current.ID != g.CurrentRun || len(l.Current.Sessions) != 1 ||
			l.Current.Sessions[0].Ref != "session:bb/thr_primary" || l.Current.Attempts.Total != 1 || l.Current.Attempts.Latest[0].Session.Ref != "session:bb/thr_primary" {
			t.Fatalf("%s: %s", hold, out)
		}
		wantEvidence, wantState := evidenceConfigured, "open"
		if hold == "complete" {
			wantEvidence, wantState = evidenceHistorical, "completion_declared"
		}
		if l.Current.Sessions[0].Evidence != wantEvidence || l.Current.State != wantState || l.Conflict != "" {
			t.Fatalf("%s: %s", hold, out)
		}
		if code, out, _ := f.run("runs", "--id", "session:bb/thr_primary"); code != exitOK || !strings.Contains(out, g.CurrentRun) {
			t.Fatal(code, out)
		}
		if f.read("calls") != calls {
			t.Fatalf("%s: runs called the provider", hold)
		}
		if after := snapshotTree(t, stateDir); after != before {
			t.Fatalf("%s: runs changed the state directory", hold)
		}
	}
}

// A scheduler supplies its identity with --routine. It is recorded with the
// run it opens or triggers; it never opens, reopens or closes one by itself,
// and it changes nothing a check decides.
func TestRoutineFlagRecordsSchedulerIdentityWithoutChangingSupervision(t *testing.T) {
	f := newFixture(t)
	stateDir := filepath.Join(f.dir, "state")
	ok := func(args ...string) string {
		t.Helper()
		code, out, errs := f.run(args...)
		if code != exitOK {
			t.Fatalf("%v: %d %s %s", args, code, out, errs)
		}
		return out
	}
	// A scheduled check before anything is enabled records nothing.
	ok("tick", "--routine", "example-scheduler/nightly/inv-0")
	if _, availability := rungraph.Load(stateDir); availability != rungraph.Missing {
		t.Fatal("a trigger on a disabled loop created a graph")
	}
	ok("enable", "--routine", "example-scheduler/nightly/inv-1")
	g := graphOf(t, f)
	run := g.CurrentRun
	if g.Runs[0].Origin != rd.Explicit || g.Runs[0].Routine == nil || g.Runs[0].Routine.Invocation != "inv-1" || len(g.Links) != 0 {
		t.Fatalf("%#v", g.Runs)
	}
	// The check's result is the same with or without the identity.
	plain := f.tick()
	var withRoutine map[string]any
	if err := json.Unmarshal([]byte(ok("tick", "--routine", "example-scheduler/nightly/inv-2")), &withRoutine); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"control", "target", "warnings"} {
		if fmt.Sprint(plain[key]) != fmt.Sprint(withRoutine[key]) {
			t.Fatalf("%s differs: %v vs %v", key, plain[key], withRoutine[key])
		}
	}
	ok("tick", "--routine", "example-scheduler/nightly/inv-2")              // repeated trigger: one link
	ok("tick", "--dry-run", "--routine", "example-scheduler/nightly/inv-9") // dry runs record nothing
	ok("enable", "--routine", "example-scheduler/nightly/inv-3")            // a scheduled enable of an open run is a trigger
	g = graphOf(t, f)
	var triggers []string
	for _, l := range g.Links {
		if l.Kind == rd.InvocationLink {
			triggers = append(triggers, l.Routine.Invocation)
		}
	}
	if g.CurrentRun != run || len(g.Runs) != 1 || strings.Join(triggers, " ") != "inv-2 inv-3" {
		t.Fatalf("runs=%d triggers=%v", len(g.Runs), triggers)
	}
	// After completion a trigger and a scheduled enable relate to no run and
	// reopen nothing. Only resume opens the next run, and carries its invocation.
	ok("complete")
	ok("tick", "--routine", "example-scheduler/nightly/inv-4")
	ok("enable", "--routine", "example-scheduler/nightly/inv-5")
	if g = graphOf(t, f); len(g.Runs) != 1 || !g.Completed || len(g.Links) != len(triggers)+1 {
		t.Fatalf("a trigger after completion changed the graph: %#v", g.Links)
	}
	ok("resume", "--routine", "example-scheduler/nightly/inv-6")
	g = graphOf(t, f)
	if len(g.Runs) != 2 || g.Runs[1].Origin != rd.Reopened || g.Runs[1].Reopens != run || g.Runs[1].Routine.Invocation != "inv-6" {
		t.Fatalf("%#v", g.Runs)
	}
	// routine → both runs, through the reader.
	out := ok("runs", "--id", "routine:example-scheduler/nightly")
	if !strings.Contains(out, "run -> "+run) || !strings.Contains(out, "run -> "+g.CurrentRun) || !strings.Contains(out, "opened by invocation: routine:example-scheduler/nightly/inv-6") {
		t.Fatal(out)
	}
	// A malformed or misplaced identity is a usage error: nothing changes and
	// the supplied value is not echoed.
	before := snapshotTree(t, stateDir)
	for _, args := range [][]string{
		{"tick", "--routine", "PRIVATE value"}, {"enable", "--routine", "nightly"}, {"resume", "--routine", "a/b/c/d"},
		{"tick", "--routine", "example-scheduler/nightly/"}, {"pause", "--routine", "example-scheduler/nightly"}, {"status", "--routine", "example-scheduler/nightly"},
	} {
		code, out, errs := f.run(args...)
		if code != exitUsage || strings.Contains(out+errs, "PRIVATE") || strings.Contains(out+errs, "a/b/c/d") {
			t.Fatalf("%v: %d %s %s", args, code, out, errs)
		}
	}
	if snapshotTree(t, stateDir) != before {
		t.Fatal("a rejected --routine changed runtime records")
	}
}
