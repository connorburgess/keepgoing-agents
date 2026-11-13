//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Through the real CLI and BB adapter against a fake bb: an idle parent whose
// children are all in error is told once, with the count only. No child is
// sent to, stopped or has its log read, and status and timeline show the count.
func TestEndToEndErroredChildrenAreAnnouncedOnceAndNeverTouched(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	child := func(id, status string) {
		write(t, filepath.Join(f.bbDir, id+".show"), fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":"thr_primary","status":%q,"title":"fictional-private-title"}}`, id, status), 0600)
	}
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child_a"},{"id":"thr_child_b"},{"id":"thr_child_c"}]`, 0600)
	child("thr_child_a", "error")
	child("thr_child_b", "error")
	child("thr_child_c", "active")

	// One child still running: the parent is woken once for the two that
	// failed, then left alone again as busy.
	if r := f.tick(); r["action"] != "wake" || r["reason"] != "children in error while other children run" || r["erroredChildren"] != float64(2) || r["childErrorNote"] != true {
		t.Fatalf("%v", r)
	}
	for i := 0; i < 2; i++ {
		if r := f.tick(); r["action"] != "none" || r["reason"] != "child work running" || r["childErrorNote"] == true {
			t.Fatalf("check %d: %v", i, r)
		}
	}
	// That message is the notice alone: no task prompt for a parent that is
	// waiting on a running child.
	if told := f.read("thr_primary.told"); strings.Count(told, "[keepgoing:child-errors count=2 running=1]") != 1 || strings.Contains(told, "Continue the fictional loop.") {
		t.Fatalf("told: %q", told)
	}
	// It fails too: the set changed, and the idle parent's ordinary wake
	// carries the notice once.
	child("thr_child_c", "error")
	for i, wantNote := range []bool{true, false, false} {
		r := f.tick()
		if r["action"] != "wake" || r["reason"] != "idle" || r["erroredChildren"] != float64(3) || (r["childErrorNote"] == true) != wantNote {
			t.Fatalf("check %d: %v", i, r)
		}
	}
	told := f.read("thr_primary.told")
	if strings.Count(told, "[keepgoing:child-errors count=3]") != 1 || strings.Count(told, "Continue the fictional loop.") != 3 ||
		strings.Contains(told, "thr_child") || strings.Contains(told, "fictional-private-title") {
		t.Fatalf("told: %q", told)
	}
	// One is retried by its owner and recovers: the set only shrank, so
	// nothing new is said. When it fails again, it is announced again.
	child("thr_child_b", "idle")
	if r := f.tick(); r["action"] != "wake" || r["childErrorNote"] == true || r["erroredChildren"] != float64(2) {
		t.Fatalf("%v", r)
	}
	child("thr_child_b", "error")
	if r := f.tick(); r["childErrorNote"] != true || r["erroredChildren"] != float64(3) {
		t.Fatalf("%v", r)
	}
	if told := f.read("thr_primary.told"); strings.Count(told, "[keepgoing:child-errors count=3]") != 2 || strings.Contains(told, "[keepgoing:child-errors count=2]") {
		t.Fatalf("told: %q", told)
	}

	for _, line := range strings.Split(f.read("calls"), "\n") {
		if !strings.Contains(line, "thr_child") {
			continue
		}
		if !strings.HasPrefix(line, "thread show thr_child") && !strings.HasPrefix(line, "thread interactions list thr_child") {
			t.Fatalf("a child was asked for more than status and pending interactions: %q", line)
		}
	}
	for _, id := range []string{"thr_child_a", "thr_child_b", "thr_child_c"} {
		if f.read(id+".told") != "" {
			t.Fatalf("%s was sent a message", id)
		}
	}

	code, out, errs := f.run("status")
	if code != exitOK || !strings.Contains(out, "children:  3 direct child thread(s) in error at that check (historical); keepgoing does not retry, message or stop them\n") || strings.Contains(out, "thr_child") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	var status statusOut
	_, out, _ = f.run("status", "--json")
	if err := json.Unmarshal([]byte(out), &status); err != nil || status.State.LastCheck == nil || status.State.LastCheck.ErroredChildren != 3 || strings.Contains(out, "thr_child") {
		t.Fatalf("%v %s", err, out)
	}
	code, out, errs = f.run("timeline")
	if code != exitOK || !strings.Contains(out, "3 direct child thread(s) in error") || !strings.Contains(out, "2 direct child thread(s) in error") || strings.Contains(out, "thr_child") {
		t.Fatalf("timeline: %d %s %s", code, out, errs)
	}
	if f.hooks.types() != "" {
		t.Fatalf("errored children sent a report or raised a hold: %s", f.hooks.types())
	}

	// A retarget forgets what was announced: children belong to the old target.
	if code, _, errs := f.run("retarget", "primary"); code != exitOK {
		t.Fatal(errs)
	}
	if st, _ := state.Load(filepath.Join(f.dir, "state")); len(st.ChildErrorsAnnounced) != 0 {
		t.Fatalf("retarget kept %v", st.ChildErrorsAnnounced)
	}

	// A listing that cannot be completed: the count is a lower bound, and both
	// status and timeline say so.
	write(t, filepath.Join(f.bbDir, "thr_child_c.show"), `fictional unreadable reply`, 0600)
	if r := f.tick(); r["action"] != "none" || r["reason"] != "child status unverified" || r["erroredChildrenPartial"] != true {
		t.Fatalf("%v", r)
	}
	for _, cmd := range []string{"status", "timeline"} {
		if _, out, _ := f.run(cmd); !strings.Contains(out, "at least 2 (child discovery was incomplete) direct child thread(s) in error") {
			t.Fatalf("%s: %s", cmd, out)
		}
	}
}

// A notice the adapter keeps rejecting is given up, status says so, and
// resume lets it be tried again.
func TestStatusShowsAGivenUpChildNoticeAndResumeRetriesIt(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	dir := filepath.Join(f.dir, "state")
	st, _ := state.Load(dir)
	st.ChildErrorNoticeFor, st.ChildErrorNoticeAttempts = "fictional-pending", 3
	st.LastCheck = &state.Check{Action: "none", Reason: "child work running", Outcome: "succeeded", ObservedState: "idle", ErroredChildren: 2, ChildErrorNoticeStopped: true}
	if err := state.Save(dir, st); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := f.run("status"); !strings.Contains(out, "child notice: not accepted after 3 attempts; keepgoing has stopped sending it until another child fails or resume\n") {
		t.Fatalf("status: %s", out)
	}
	if code, _, errs := f.run("resume"); code != exitOK {
		t.Fatal(errs)
	}
	if st, _ := state.Load(dir); st.ChildErrorNoticeAttempts != 0 {
		t.Fatalf("resume kept %d attempts", st.ChildErrorNoticeAttempts)
	}
}
