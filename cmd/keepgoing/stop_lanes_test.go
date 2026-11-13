//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
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

// keepgoing stop --lanes --now stops the supervised thread and its running
// lanes inside the command, through the check's stop path, and records it as
// a check. No message is sent to anyone.
func TestStopLanesNowStopsTheLanesInTheCommand(t *testing.T) {
	f := laneFixture(t)
	code, out, errs := f.run("stop", "--lanes", "--now")
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

// Without --now the stop is saved and the next check carries it out; plain
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

// While another check holds the loop's check lock, stop --now waits no more
// than a few seconds, says so, and leaves the stop to the checks.
func TestStopNowLeavesTheStopToARunningCheck(t *testing.T) {
	f := laneFixture(t)
	held, err := lock.Acquire(filepath.Join(f.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, out, _ := f.run("stop", "--lanes", "--now")
	held.Release()
	if took := time.Since(start); took > stopNowWait+3*time.Second {
		t.Fatalf("waited %s", took)
	}
	if code != exitOK || !strings.Contains(out, "the stop is left to the checks") || len(stopCalls(f)) != 0 {
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

// client stop takes --lanes and --now, and its JSON says what was done to the
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
	run(context.Background(), []string{"client", "stop", "--lanes", "--now", "--id", entry.ID, "--directory", dir, "--json"}, &out, &errs)
	r = clientHoldReport{}
	if json.Unmarshal(out.Bytes(), &r) != nil || r.CarriedOut != "now" || r.Lanes == nil || !r.Lanes.Requested || r.Lanes.Result == nil {
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
	run(context.Background(), []string{"client", "stop", "--now", "--id", entry.ID, "--directory", dir, "--json"}, &out, &errs)
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
