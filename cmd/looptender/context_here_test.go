//go:build unix

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

	"github.com/looptender/looptender/internal/session"
)

// here runs context --here, which reads no -c, and returns its answer.
func (f *fixture) here(args ...string) (int, hereView, string, string) {
	f.t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{"context", "--here", "--json"}, args...), &out, &errb)
	var v hereView
	if code != exitUsage {
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			f.t.Fatalf("context --here --json: %d %v\n%s\n%s", code, err, out.String(), errb.String())
		}
	}
	var text bytes.Buffer
	run(context.Background(), append([]string{"context", "--here"}, args...), &text, &bytes.Buffer{})
	return code, v, text.String(), errb.String()
}

// unchanged fails the test when the command changed any file under the
// fixture, its session records included, or called bb.
func (f *fixture) unchanged(t *testing.T, before, calls string) {
	t.Helper()
	if after := snapshotTree(t, f.dir); after != before {
		t.Fatalf("context --here changed files:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if now := f.read("calls"); now != calls {
		t.Fatalf("context --here called bb: %s", now)
	}
}

// secondLoop writes another loop's configuration, with its own state
// directory, whose primary is thread.
func (f *fixture) secondLoop(t *testing.T, name, thread string) string {
	t.Helper()
	dir := filepath.Join(f.dir, name)
	os.MkdirAll(dir, 0o700)
	cfg := filepath.Join(dir, "looptender.yaml")
	write(t, cfg, fmt.Sprintf("version: 1\nloop:\n  name: %s\n  state_dir: state\n  prompt: Continue the example task.\n  adapter: bb\n  bb: {thread: %s, cli: %s/bin/bb}\n", name, thread, f.dir), 0o600)
	return cfg
}

// The session a loop targets finds that loop and its context; a lane of it,
// another thread, and a thread the loop has left find none. Nothing is
// written and bb is not called.
func TestContextHereFindsTheLoopThatTargetsTheCaller(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_owner1")
	if code, out, errs := f.run("enable"); code != exitOK {
		t.Fatalf("enable: %d %s %s", code, out, errs)
	}
	f.tick()
	before, calls := snapshotTree(t, f.dir), f.read("calls")

	t.Setenv("BB_THREAD_ID", "thr_primary")
	code, v, text, errs := f.here()
	if code != exitOK || errs != "" || v.Outcome != hereOne || len(v.Loops) != 1 || len(v.NotCounted) != 0 {
		t.Fatalf("%d %s %+v", code, errs, v)
	}
	l := v.Loops[0]
	if l.Loop != "example-loop" || l.Context.Target == nil || l.Context.Target.BBThread != "thr_primary" || l.Context.Owner.BBThread != "thr_owner1" {
		t.Fatalf("%+v", l)
	}
	if v.Caller.Source != "bb_thread_id" || v.Caller.BBThread != "thr_primary" || !strings.Contains(v.Caller.Text, "not with BB") {
		t.Fatalf("caller %+v", v.Caller)
	}
	for _, want := range []string{
		"found:     loop example-loop targets this session\n",
		"caller:    BB thread thr_primary, taken from BB_THREAD_ID, which is checked only for its form and not with BB",
		"basis:     " + hereNoParent + "\n",
		"\nexample-loop (", "context:   schema version 1\n", "authority:\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, hereProcessLimit) || strings.Contains(text, hereExactDir) {
		t.Fatal("a BB caller is told about working directories")
	}
	if !strings.Contains(text, "basis:     "+hereInherited+"\n") {
		t.Fatalf("a BB caller is not told that agents looptender starts inherit BB_THREAD_ID:\n%s", text)
	}

	// A lane of the supervised thread, and any other thread, is not
	// supervised by implication: the parent is never looked up.
	for _, thread := range []string{"thr_lane1", "thr_owner1", "thr_alt"} {
		t.Setenv("BB_THREAD_ID", thread)
		code, v, text, _ := f.here()
		if code != exitOK || v.Outcome != hereNone || v.Text != hereNoLoop || len(v.Loops) != 0 {
			t.Fatalf("%s: %d %+v", thread, code, v)
		}
		if !strings.Contains(text, "found:     no loop targets this session\n") || strings.Contains(text, "example-loop") {
			t.Fatalf("%s: %s", thread, text)
		}
	}
	f.unchanged(t, before, calls)

	// After a retarget the loop's active target is the fallback: the
	// primary thread, though recorded, is no longer targeted.
	t.Setenv("BB_THREAD_ID", "thr_owner1")
	if code, out, errs := f.run("retarget", "claude-other-account"); code != exitOK {
		t.Fatalf("retarget: %d %s %s", code, out, errs)
	}
	t.Setenv("BB_THREAD_ID", "thr_primary")
	code, v, text, _ = f.here()
	if code != exitOK || v.Outcome != hereNone || len(v.NotCounted) != 1 || !v.NotCounted[0].Verified || v.NotCounted[0].Count != 1 || v.NotCounted[0].Reason != "its active route targets another session" {
		t.Fatalf("%d %+v", code, v)
	}
	// A loop that does not target the caller is counted, never named.
	if v.NotCounted[0].Loop != "" || v.NotCounted[0].Config != "" || strings.Contains(text, "example-loop") || strings.Contains(text, f.cfg) {
		t.Fatalf("a loop that does not target the caller is named: %+v\n%s", v.NotCounted, text)
	}
	if !strings.Contains(text, "not counted: 1 recorded loop(s): its active route targets another session\n") {
		t.Fatalf("%s", text)
	}
}

// Two loops whose active target is the caller's session are both reported,
// as enable reports them.
func TestContextHereReportsMoreThanOneLoop(t *testing.T) {
	f := newFixture(t)
	if code, out, errs := f.run("enable"); code != exitOK {
		t.Fatalf("enable: %d %s %s", code, out, errs)
	}
	other := f.secondLoop(t, "second-loop", "thr_primary")
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"enable", "--allow-second-loop", "-c", other}, &out, &errb); code != exitOK {
		t.Fatalf("enable second: %d %s %s", code, out.String(), errb.String())
	}
	before, calls := snapshotTree(t, f.dir), f.read("calls")
	t.Setenv("BB_THREAD_ID", "thr_primary")
	code, v, text, _ := f.here()
	if code != exitOK || v.Outcome != hereSeveral || len(v.Loops) != 2 {
		t.Fatalf("%d %+v", code, v)
	}
	if !strings.HasPrefix(v.Text, "2 loops target this session: ") || !strings.Contains(text, "\nexample-loop (") || !strings.Contains(text, "\nsecond-loop (") {
		t.Fatalf("%s", text)
	}
	f.unchanged(t, before, calls)
}

// A recorded loop is checked against its current configuration and state.
// What cannot be checked makes the answer unknown, never "no loop".
func TestContextHereRevalidatesRecordedLoops(t *testing.T) {
	f := newFixture(t)
	if code, out, errs := f.run("enable"); code != exitOK {
		t.Fatalf("enable: %d %s %s", code, out, errs)
	}
	t.Setenv("BB_THREAD_ID", "thr_primary")

	write(t, filepath.Join(f.dir, "state", "state.json"), "{not json", 0o600)
	before, calls := snapshotTree(t, f.dir), f.read("calls")
	code, v, text, _ := f.here()
	if code != exitFailed || v.Outcome != hereUnknown || len(v.Loops) != 0 || len(v.NotCounted) != 1 || v.NotCounted[0].Verified || v.NotCounted[0].Loop != "example-loop" {
		t.Fatalf("%d %+v", code, v)
	}
	if strings.Contains(text, hereNoLoop) || !strings.Contains(text, "found:     no loop is known to target this session; whether 1 other recorded loop(s) do could not be checked\n") {
		t.Fatalf("%s", text)
	}
	f.unchanged(t, before, calls)
	os.Remove(filepath.Join(f.dir, "state", "state.json"))

	// The configuration now names another state directory: not this loop.
	cfg, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(cfg), "state_dir: state", "state_dir: state-moved", 1), 0o600)
	code, v, _, _ = f.here()
	if code != exitOK || v.Outcome != hereNone || len(v.NotCounted) != 1 || v.NotCounted[0].Reason != "its configuration now names another state directory" || v.NotCounted[0].Loop != "" {
		t.Fatalf("%d %+v", code, v)
	}

	// The configuration does not load: unknown.
	write(t, f.cfg, "version: [", 0o600)
	if code, v, _, _ = f.here(); code != exitFailed || v.Outcome != hereUnknown {
		t.Fatalf("%d %+v", code, v)
	}

	// The configuration is gone: not counted, and the answer is none.
	os.Remove(f.cfg)
	if code, v, _, _ = f.here(); code != exitOK || v.Outcome != hereNone || v.NotCounted[0].Reason != "its configuration file no longer exists" {
		t.Fatalf("%d %+v", code, v)
	}
}

// A caller without BB_THREAD_ID is identified by its working directory only,
// and is told so; a loop of a headless agent working there is found.
func TestContextHereByWorkingDirectory(t *testing.T) {
	f := newFixture(t)
	project := filepath.Join(f.dir, "project")
	os.MkdirAll(project, 0o700)
	os.MkdirAll(filepath.Join(f.dir, "headless-state"), 0o700)
	cfgPath := filepath.Join(f.dir, "headless.yaml")
	write(t, cfgPath, fmt.Sprintf("version: 1\nloop:\n  name: headless-loop\n  state_dir: headless-state\n  prompt: Continue the example task.\n  adapter: claude\n  claude: {workdir: %s, cli: %s/bin/claude}\n", project, f.dir), 0o600)
	dir, _, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	// What enable records for this loop, without starting an agent.
	if err := session.Note(dir, session.Identity{Kind: "claude", ID: session.CleanPath(project)}, session.Supervisor{Loop: "headless-loop", Config: cfgPath, StateDir: filepath.Join(f.dir, "headless-state"), SeenAt: time.Now()}, time.Second); err != nil {
		t.Fatal(err)
	}
	before, calls := snapshotTree(t, f.dir), f.read("calls")

	t.Chdir(project)
	code, v, text, _ := f.here()
	if code != exitOK || v.Outcome != hereOne || v.Loops[0].Loop != "headless-loop" || v.Caller.Source != "working_directory" || v.Caller.Workdir != session.CleanPath(project) {
		t.Fatalf("%d %+v", code, v)
	}
	for _, want := range []string{hereDirCaller, hereExactDir, hereProcessLimit} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q:\n%s", want, text)
		}
	}

	// A subdirectory or another directory finds none, and says which
	// directory it looked for; BB_THREAD_ID set makes the working directory
	// irrelevant.
	sub := filepath.Join(project, "sub")
	os.MkdirAll(sub, 0o700)
	before = snapshotTree(t, f.dir)
	for _, d := range []string{sub, f.dir} {
		t.Chdir(d)
		code, v, text, _ := f.here()
		want := "no loop targets a session working in " + session.CleanPath(d)
		if code != exitOK || v.Outcome != hereNone || v.Text != want || !strings.Contains(text, "found:     "+want+"\n") || strings.Contains(text, "headless-loop") {
			t.Fatalf("%s: %d %+v\n%s", d, code, v, text)
		}
	}
	t.Chdir(project)
	t.Setenv("BB_THREAD_ID", "thr_lane1")
	if code, v, _, _ := f.here(); code != exitOK || v.Outcome != hereNone || v.Caller.Workdir != "" {
		t.Fatalf("%d %+v", code, v)
	}
	f.unchanged(t, before, calls)
}

// The claim directory is neither created nor repaired, and one that cannot
// be trusted gives an unknown answer.
func TestContextHereLeavesTheClaimDirectoryAlone(t *testing.T) {
	f := newFixture(t)
	sessions := filepath.Join(f.dir, "sessions")
	t.Setenv("BB_THREAD_ID", "thr_primary")
	code, v, _, _ := f.here()
	if code != exitOK || v.Outcome != hereNone {
		t.Fatalf("%d %+v", code, v)
	}
	if _, err := os.Lstat(sessions); !os.IsNotExist(err) {
		t.Fatalf("context --here created the claim directory: %v", err)
	}
	os.MkdirAll(filepath.Join(f.dir, "elsewhere"), 0o700)
	if err := os.Symlink(filepath.Join(f.dir, "elsewhere"), sessions); err != nil {
		t.Fatal(err)
	}
	code, v, _, _ = f.here()
	if code != exitFailed || v.Outcome != hereUnknown || !strings.Contains(v.Text, "is a symbolic link") {
		t.Fatalf("%d %+v", code, v)
	}
	os.Remove(sessions)
	os.MkdirAll(sessions, 0o755)
	if code, v, _, _ = f.here(); code != exitOK || v.Outcome != hereNone {
		t.Fatalf("%d %+v", code, v)
	}
	if info, _ := os.Stat(sessions); info.Mode().Perm() != 0o755 {
		t.Fatalf("context --here repaired the claim directory: %v", info.Mode())
	}

	// An unreadable record is not taken for an empty one.
	id := session.Identity{Kind: "bb", ID: "thr_primary"}
	write(t, filepath.Join(sessions, id.Key()+".json"), "{not json", 0o600)
	if code, v, _, _ = f.here(); code != exitFailed || v.Outcome != hereUnknown || strings.Contains(v.Text, hereNoLoop) {
		t.Fatalf("%d %+v", code, v)
	}
}

func TestContextHereUsage(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		args   []string
		thread string
		want   string
	}{
		{[]string{"context", "--here", "-c", "x.yaml"}, "", "usage: keepgoing context --here"},
		{[]string{"context", "--here", "extra"}, "", "usage: keepgoing context --here"},
		{[]string{"status", "--here"}, "", "--here is only supported by context"},
		{[]string{"context", "--here"}, "not-a-thread", "BB_THREAD_ID is set but is not a BB thread ID"},
	} {
		t.Setenv("BB_THREAD_ID", c.thread)
		var out, errb bytes.Buffer
		if code := run(context.Background(), c.args, &out, &errb); code != exitUsage || !strings.Contains(errb.String(), c.want) {
			t.Fatalf("%v: %d %s", c.args, code, errb.String())
		}
	}
	_ = f
}
