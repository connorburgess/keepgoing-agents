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

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func laneFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child_a"},{"id":"thr_child_b"}]`, 0o600)
	write(t, filepath.Join(f.bbDir, "thr_child_b.children"), `[{"id":"thr_child_b1"}]`, 0o600)
	for id, status := range map[string]string{"thr_child_a": "active", "thr_child_b": "idle"} {
		write(t, filepath.Join(f.bbDir, id+".show"), fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":"thr_primary","status":%q,"title":"PRIVATE_TITLE"}}`, id, status), 0o600)
	}
	write(t, filepath.Join(f.bbDir, "thr_child_b1.show"), `{"thread":{"id":"thr_child_b1","parentThreadId":"thr_child_b","status":"active"}}`, 0o600)
	return f
}

func stopCalls(f *fixture) []string {
	var out []string
	for _, line := range strings.Split(f.read("calls"), "\n") {
		if strings.HasPrefix(line, "thread stop ") {
			out = append(out, strings.Fields(line)[2])
		}
	}
	return out
}

// keepgoing stop --lanes --immediately stops the supervised thread and its running
// lanes inside the command, through the check's stop path, and records it as
// a check. No message is sent to anyone.
func TestStopLanesNowStopsTheLanesInTheCommand(t *testing.T) {
	f := laneFixture(t)
	code, out, errs := f.run("stop", "--lanes", "--immediately")
	if code != exitOK || !strings.Contains(out, "stop carried out now: stop") || !strings.Contains(out, "lanes: 2 stopped, 1 already idle, 0 left to another loop, 0 failed, 0 not checked") {
		t.Fatalf("%d\n%s%s", code, out, errs)
	}
	if got := strings.Join(stopCalls(f), ","); got != "thr_primary,thr_child_a,thr_child_b1" {
		t.Fatalf("stopped %s", got)
	}
	if strings.Contains(f.read("calls"), "thread tell") || f.read("thr_child_a.told") != "" || strings.Contains(out+errs, "PRIVATE") {
		t.Fatal("a message was sent")
	}
	dir := filepath.Join(f.dir, "state")
	ctl, _ := control.Load(dir)
	st, err := state.Load(dir)
	if err != nil || !ctl.Stop || ctl.StopLanesAt == nil || st.LaneStop == nil || st.LaneStop.Outcome != "done" || !st.LaneStop.For.Equal(*ctl.StopLanesAt) || st.LastCheck == nil || st.LastCheck.Action != "stop" {
		t.Fatalf("%+v %+v %v", ctl, st.LaneStop, err)
	}
	// One stop report, sent by the command.
	if got := f.hooks.types(); got != "keepgoing.stop" {
		t.Fatalf("reports %q", got)
	}
	if code, out, _ := f.run("status"); code != exitOK || !strings.Contains(out, "lane stop: at the check of ") || !strings.Contains(out, "lanes: 2 stopped, 1 already idle") {
		t.Fatalf("status\n%s", out)
	}
	// A later check stops nothing more.
	f.tick()
	if len(stopCalls(f)) != 3 || f.hooks.types() != "keepgoing.stop" {
		t.Fatalf("%v %s", stopCalls(f), f.hooks.types())
	}
	// A resume ends the stop and its lane request.
	f.run("resume")
	if ctl, _ := control.Load(dir); ctl.Stop || ctl.StopLanesAt != nil {
		t.Fatalf("%+v", ctl)
	}
}

// Without --immediately the stop is saved and the next check carries it out; plain
// stop does not look at lanes.
func TestStopLanesWithoutNowIsLeftToTheNextCheck(t *testing.T) {
	f := laneFixture(t)
	code, out, _ := f.run("stop", "--lanes")
	if code != exitOK || !strings.Contains(out, "the next check carries out the stop, and stops the lanes") || len(stopCalls(f)) != 0 {
		t.Fatalf("%d %s %v", code, out, stopCalls(f))
	}
	if _, out, _ := f.run("status"); !strings.Contains(out, "lane stop: requested at ") {
		t.Fatalf("status\n%s", out)
	}
	r := f.tick()
	if r["action"] != "stop" || strings.Join(stopCalls(f), ",") != "thr_primary,thr_child_a,thr_child_b1" {
		t.Fatalf("%v %v", r, stopCalls(f))
	}
	g := laneFixture(t)
	if code, out, _ := g.run("stop"); code != exitOK || strings.Contains(out, "lanes") {
		t.Fatalf("plain stop: %s", out)
	}
	g.tick()
	if strings.Join(stopCalls(g), ",") != "thr_primary" || strings.Contains(g.read("calls"), "--parent-thread thr_child") {
		t.Fatalf("plain stop looked at lanes: %v", stopCalls(g))
	}
}

// While another check holds the loop's check lock, stop --immediately waits no more
// than a few seconds, says so, and leaves the stop to the checks.
func TestStopNowLeavesTheStopToARunningCheck(t *testing.T) {
	f := laneFixture(t)
	held, err := lock.Acquire(filepath.Join(f.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, out, _ := f.run("stop", "--lanes", "--immediately")
	held.Release()
	if took := time.Since(start); took > stopNowWait+3*time.Second {
		t.Fatalf("waited %s", took)
	}
	if code != exitOK || !strings.Contains(out, "stop saved and left to the loop's checks: a check of example-loop was running") || len(stopCalls(f)) != 0 {
		t.Fatalf("%d\n%s %v", code, out, stopCalls(f))
	}
	f.tick()
	if len(stopCalls(f)) != 3 {
		t.Fatalf("%v", stopCalls(f))
	}
}

func TestLanesAndNowAreStopOnly(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"pause", "--lanes"}, {"resume", "--lanes"}, {"tick", "--lanes"}} {
		if code, _, errs := f.run(args...); code != exitUsage || !strings.Contains(errs, "only supported by stop") {
			t.Errorf("%v: %d %s", args, code, errs)
		}
	}
}

// client stop takes --lanes and --immediately, and its JSON says what was done to the
// lanes and whether the stop ran in the command or was left to a check.
func TestClientStopLanesNowJSON(t *testing.T) {
	dir, entry := clientFixture(t)
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"client", "stop", "--id", entry.ID, "--directory", dir, "--json"}, &out, &errs); code != exitOK {
		t.Fatal(code, out.String(), errs.String())
	}
	var r clientHoldReport
	if json.Unmarshal(out.Bytes(), &r) != nil || r.CarriedOut != "left_to_check" || r.Lanes != nil {
		t.Fatal(out.String())
	}
	if ctl, _ := control.Load(entry.StateDir); ctl.StopLanesAt != nil {
		t.Fatal("plain client stop asked for the lanes")
	}
	// The registered loop's agent does not exist, so the check cannot reach
	// it; the stop is still carried out in the command and recorded.
	out.Reset()
	errs.Reset()
	code := run(context.Background(), []string{"client", "stop", "--lanes", "--immediately", "--id", entry.ID, "--directory", dir, "--json"}, &out, &errs)
	r = clientHoldReport{}
	if code != exitOK || json.Unmarshal(out.Bytes(), &r) != nil || r.CarriedOut != "now" || r.Lanes == nil || !r.Lanes.Requested || r.Lanes.Result == nil || len(r.Issues) != 0 {
		t.Fatal(out.String(), errs.String())
	}
	if ctl, _ := control.Load(entry.StateDir); ctl.StopLanesAt == nil || !ctl.Stop {
		t.Fatalf("%+v", ctl)
	}
	// With the check lock held it is left to a check.
	held, err := lock.Acquire(entry.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	out.Reset()
	run(context.Background(), []string{"client", "stop", "--immediately", "--id", entry.ID, "--directory", dir, "--json"}, &out, &errs)
	r = clientHoldReport{}
	if json.Unmarshal(out.Bytes(), &r) != nil || r.CarriedOut != "left_to_check" || r.LeftBecause != "check_running" {
		t.Fatal(out.String())
	}
	// pause has neither option.
	out.Reset()
	if code := run(context.Background(), []string{"client", "pause", "--lanes", "--id", entry.ID, "--directory", dir}, &out, &errs); code != exitUsage {
		t.Fatal(code)
	}
}

// S2. A resume that lands between the stop's save and the check lock leaves
// the stop-only check nothing to do: it must not run a full check (an idle
// session would be woken), and it needs no owner.
func TestStopNowAfterAResumeDoesNothing(t *testing.T) {
	f := laneFixture(t)
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"idle"}}`, 0o600)
	f.run("stop", "--lanes")
	f.run("resume")
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BB_THREAD_ID", "thr_not_the_owner")
	r, err := stopNow(context.Background(), cfg, stopNowWait, "")
	if err != nil || r.CarriedOut != "now" || r.Action != "none" || r.Reason != "stop no longer requested; nothing done" {
		t.Fatalf("%+v %v", r, err)
	}
	if calls := f.read("calls"); strings.Contains(calls, "thread tell") || strings.Contains(calls, "thread stop") || strings.Contains(calls, "thread show") {
		t.Fatalf("the check did more than nothing:\n%s", calls)
	}
}

// Another loop's live check holds the session: the stop is left to the
// checks, said in words, and the command exits 0, as a running check does.
// An observation failure is carried out and recorded, and exits 0 too.
func TestStopNowOutcomesAndExitCodes(t *testing.T) {
	f := laneFixture(t)
	claim, err := session.Take(os.Getenv(session.DirEnv), session.Identity{Kind: "bb", ID: "thr_primary"}, session.Holder{Loop: "other-loop", PID: 1, Since: time.Now(), Until: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	code, out, errs := f.run("stop", "--lanes", "--immediately")
	claim.Release()
	if code != exitOK || !strings.Contains(out, "stop saved and left to the loop's checks: another loop's check is acting on this session") || strings.Contains(out, "carried out now") || len(stopCalls(f)) != 0 {
		t.Fatalf("%d\n%s%s", code, out, errs)
	}
	g := laneFixture(t)
	write(t, filepath.Join(g.bbDir, "thr_primary.show"), `not json`, 0o600)
	code, out, errs = g.run("stop", "--lanes", "--immediately")
	if code != exitOK || !strings.Contains(out, "stop carried out now:") || !strings.Contains(out, "lanes: 2 stopped") {
		t.Fatalf("%d\n%s%s", code, out, errs)
	}
	// From inside a BB thread the command says, before anything is stopped,
	// that a thread of its own is stopped last.
	h := laneFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_child_a")
	if _, out, _ := h.run("stop", "--lanes", "--immediately"); !strings.Contains(out, "this command runs in BB thread thr_child_a; if the stop covers it, it is stopped last") {
		t.Fatalf("%s", out)
	}
	if got := strings.Join(stopCalls(h), ","); got != "thr_primary,thr_child_b1,thr_child_a" {
		t.Fatalf("stopped %s", got)
	}
}

// --immediately is an ordinary flag: a note before it is a note, and the
// clock flag --now <time> keeps its meaning and its value.
func TestImmediatelyIsAnOrdinaryFlag(t *testing.T) {
	f := laneFixture(t)
	if code, _, errs := f.run("stop", "--note", "reviewing", "--immediately"); code != exitOK {
		t.Fatalf("%d %s", code, errs)
	}
	ctl, _ := control.Load(filepath.Join(f.dir, "state"))
	if ctl.Note != "reviewing" || !ctl.Stop {
		t.Fatalf("%+v", ctl)
	}
	g := laneFixture(t)
	if code, _, errs := g.run("stop", "--now", "2030-03-04T12:00:00Z"); code != exitOK || len(stopCalls(g)) != 0 {
		t.Fatalf("%d %s %v", code, errs, stopCalls(g))
	}
	if code, _, _ := g.run("pause", "--immediately"); code != exitUsage {
		t.Fatal(code)
	}
}

// laneRegistry registers the lane fixture's loop, as the menu and the page
// find loops.
func laneRegistry(t *testing.T, f *fixture) (string, string) {
	t.Helper()
	reg := filepath.Join(t.TempDir(), "registry")
	if code, out, errs := registryRun(t, reg, "add", "-c", f.cfg); code != exitOK {
		t.Fatal(out, errs)
	}
	r, err := registry.Load(reg)
	if err != nil || len(r.Entries) != 1 {
		t.Fatal(err)
	}
	return reg, dockKey(r.Entries[0].ID)
}

// The menu's review of a stop offers the stop's options in the CLI's words,
// with the lanes counted from a fresh read, and client apply carries out
// the one chosen. Run with the panel server's PATH: the loop's own
// configuration names bb, so it is found the way the loop's checks find it.
func TestClientReviewAndApplyStopWithLanes(t *testing.T) {
	f := laneFixture(t)
	reg, key := laneRegistry(t, f)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	code, reply, errs := runClientReview(t, "review", "stop", "--loop", key, "--directory", reg, "--json")
	p := reply.Preview
	if code != exitOK || p == nil || p.Stop == nil {
		t.Fatalf("%d %+v %s", code, reply, errs)
	}
	got, _ := json.Marshal(p.Stop)
	if string(got) != `{"lanes":2,"immediately":true,"options":[{"label":"Stop the Conversation","lanes":false},{"label":"Stop It and Its 2 Lanes","lanes":true}]}` {
		t.Fatalf("%s", got)
	}
	for _, l := range p.Lines {
		if len(l) > clientReviewMenuCap {
			t.Fatalf("a line the menu would cut: %q", l)
		}
	}
	if strings.Contains(f.read("calls"), "thread stop") {
		t.Fatal("the review stopped something")
	}
	code, reply, errs = runClientReview(t, "apply", "stop", "--loop", key, "--expect", p.Expect, "--lanes", "--immediately", "--directory", reg, "--json")
	if code != exitOK || reply.Result == nil || reply.Result.Outcome != "done" || !strings.HasPrefix(reply.Result.Text, "Stopped now, as a check would stop it. Lanes: 2 stopped") {
		t.Fatalf("%d %+v %s", code, reply.Result, errs)
	}
	if got := strings.Join(stopCalls(f), ","); got != "thr_primary,thr_child_a,thr_child_b1" {
		t.Fatalf("stopped %s", got)
	}
	// Options belong to stop only.
	if code, _, _ := runClientReview(t, "apply", "pause", "--loop", key, "--expect", p.Expect, "--lanes", "--directory", reg, "--json"); code != exitUsage {
		t.Fatal(code)
	}
	if code, _, _ := runClientReview(t, "review", "stop", "--loop", key, "--immediately", "--directory", reg, "--json"); code != exitUsage {
		t.Fatal(code)
	}
}

// The page's stop review offers the same options, and its confirmation
// carries out only an option the review offered.
func TestPanelStopReviewOffersTheStopsOptions(t *testing.T) {
	f := laneFixture(t)
	reg, key := laneRegistry(t, f)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	reader := &dockReader{ctx: context.Background(), directory: reg, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon}
	c, err := newPanelChanges(context.Background(), reg, reader, time.Now(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	p := c.prepare("session-a", key, "stop")
	if p.Preview == nil || p.Preview.Stop == nil || len(p.Preview.Stop.Options) != 2 || p.Preview.Stop.Options[1].Label != "Stop It and Its 2 Lanes" {
		t.Fatalf("%+v", p)
	}
	r := c.confirm("session-a", key, "stop", p.Preview.Form, holdOptions{Lanes: true, Now: true})
	if r.Result == nil || r.Result.Outcome != "done" || !strings.Contains(r.Result.Text, "Lanes: 2 stopped") {
		t.Fatalf("%+v", r.Result)
	}
	if got := strings.Join(stopCalls(f), ","); got != "thr_primary,thr_child_a,thr_child_b1" {
		t.Fatalf("stopped %s", got)
	}
	// With no lanes running the review offers no lane option, and a
	// confirmation that asks for one is refused before anything is done.
	g := laneFixture(t)
	write(t, filepath.Join(g.bbDir, "thr_child_a.show"), `{"thread":{"id":"thr_child_a","parentThreadId":"thr_primary","status":"idle"}}`, 0o600)
	write(t, filepath.Join(g.bbDir, "thr_child_b1.show"), `{"thread":{"id":"thr_child_b1","parentThreadId":"thr_child_b","status":"idle"}}`, 0o600)
	reg, key = laneRegistry(t, g)
	reader = &dockReader{ctx: context.Background(), directory: reg, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon}
	c, _ = newPanelChanges(context.Background(), reg, reader, time.Now(), time.Now)
	p = c.prepare("session-a", key, "stop")
	if p.Preview == nil || p.Preview.Stop == nil || len(p.Preview.Stop.Options) != 1 || *p.Preview.Stop.Lanes != 0 {
		t.Fatalf("%+v", p.Preview)
	}
	if r := c.confirm("session-a", key, "stop", p.Preview.Form, holdOptions{Lanes: true, Now: true}); r.Result == nil || r.Result.Outcome != "review_again" {
		t.Fatalf("%+v", r.Result)
	}
	if ctl, _ := control.Load(filepath.Join(g.dir, "state")); ctl.Stop {
		t.Fatal("a refused confirmation saved the stop")
	}
}

// The page's change request is one object with these keys at most, each
// once, spelled exactly; the stop's options only with a stop.
func TestPanelChangeRequestTakesTheStopsOptionsOnly(t *testing.T) {
	for body, ok := range map[string]bool{
		`{"action":"stop"}`: true,
		`{"action":"stop","lanes":true,"immediately":true}`: true,
		`{"immediately":true,"action":"stop"}`:              true,
		`{"action":"pause","lanes":true}`:                   false,
		`{"action":"stop","lanes":"yes"}`:                   false,
		`{"action":"stop","Lanes":true}`:                    false,
		`{"action":"stop","lanes":true,"lanes":false}`:      false,
		`{"lanes":true}`:                                    false,
		`{"action":"stop","other":1}`:                       false,
		`{"action":"stop"}{}`:                               false,
	} {
		if _, got := panelReadAction(strings.NewReader(body)); got != ok {
			t.Errorf("%s: %v", body, got)
		}
	}
}
