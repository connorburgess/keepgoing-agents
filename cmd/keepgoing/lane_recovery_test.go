//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Through the real CLI and BB adapter against a fake bb, with
// bb.children.retry_errored on: a lane seen working that then fails with a
// connection error has its failed turn retried by reference, once. A
// rate-limited lane and a lane never seen working are left alone. No lane is
// messaged or stopped, nothing a lane said is kept, and status and timeline
// show what was done.
func TestEndToEndFailedLaneIsRetriedByReferenceWhenOptedIn(t *testing.T) {
	const private = "PRIVATE_LANE_TEXT"
	lanes := []string{"thr_child_a", "thr_child_b", "thr_child_c"}
	setup := func(optIn bool) *fixture {
		f := newFixture(t)
		if optIn {
			b, err := os.ReadFile(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			cfg := strings.Replace(string(b), "bb: {thread: thr_primary, cli:", "bb: {thread: thr_primary, children: {retry_errored: true}, cli:", 1)
			if cfg == string(b) {
				t.Fatal("fixture config changed")
			}
			write(t, f.cfg, cfg, 0o600)
		}
		f.run("enable")
		write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child_a"},{"id":"thr_child_b"},{"id":"thr_child_c"}]`, 0o600)
		return f
	}
	child := func(f *fixture, id, status string) {
		write(t, filepath.Join(f.bbDir, id+".show"), fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":"thr_primary","status":%q,"title":%q}}`, id, status, private), 0o600)
	}
	failure := func(f *fixture, id, category, code string) {
		write(t, filepath.Join(f.bbDir, id+".log"), fmt.Sprintf(`[{"seq":1,"type":"turn/started","data":{"providerThreadId":"provider_private1","title":%[1]q}},`+
			`{"seq":2,"type":"item/agentMessage/delta","data":{"delta":%[1]q}},`+
			`{"seq":3,"type":"provider/error","data":{"message":%[1]q,"detail":%[1]q,"willRetry":false,"errorInfo":{"category":%[2]q,"httpStatusCode":null,"providerCode":%[3]q,"message":%[1]q}}},`+
			`{"seq":4,"type":"turn/completed","data":{"providerThreadId":"provider_private1","status":"failed"}}]`, private, category, code), 0o600)
	}
	outage := func(f *fixture) {
		// a and b were seen working; c is first seen already in error.
		child(f, "thr_child_a", "active")
		child(f, "thr_child_b", "active")
		child(f, "thr_child_c", "idle")
		if r := f.tick(); r["action"] != "none" || r["lanes"] != nil {
			t.Fatalf("%v", r)
		}
		for _, id := range lanes {
			child(f, id, "error")
		}
		failure(f, "thr_child_a", "network", "econnreset")
		failure(f, "thr_child_b", "rate-limit", "rate_limit_error")
		failure(f, "thr_child_c", "network", "econnreset")
	}
	laneCalls := func(f *fixture, verb string) int {
		n := 0
		for _, line := range strings.Split(f.read("calls"), "\n") {
			if strings.HasPrefix(line, "thread "+verb+" thr_child_") {
				n++
			}
		}
		return n
	}

	f := setup(true)
	outage(f)
	r := f.tick()
	work, _ := r["lanes"].(map[string]any)
	if r["action"] != "wake" || r["erroredChildren"] != float64(3) || work == nil || work["retried"] != float64(1) {
		t.Fatalf("%v", r)
	}
	if left, _ := work["notRetried"].(map[string]any); left["rate_limit"] != float64(1) || len(left) != 1 {
		t.Fatalf("%v", work)
	}
	calls := f.read("calls")
	if strings.Count(calls, "thread retry thr_child_a --reason keepgoing: recover the failed turn --json") != 1 || laneCalls(f, "retry") != 1 {
		t.Fatalf("calls:\n%s", calls)
	}
	for _, verb := range []string{"tell", "stop", "update", "spawn", "archive", "delete"} {
		if laneCalls(f, verb) != 0 {
			t.Fatalf("a lane received %s:\n%s", verb, calls)
		}
	}
	// The lane never seen working was not inspected: its log was never read.
	if strings.Contains(calls, "thread log thr_child_c") {
		t.Fatalf("calls:\n%s", calls)
	}
	told := f.read("thr_primary.told")
	if strings.Count(told, "[keepgoing:child-errors count=3 recovering=1]") != 1 || !strings.Contains(told, "keepgoing is recovering 1 of them itself") || !strings.Contains(told, "The other 2 are yours") || strings.Contains(told, "thr_child") || strings.Contains(told, private) {
		t.Fatalf("told: %q", told)
	}
	// Inside the spacing nothing more is asked.
	if r := f.tick(); r["action"] != "wake" || laneCalls(f, "retry") != 1 {
		t.Fatalf("%v", r)
	}
	// Nothing a lane said, no lane ID and no provider reference is kept.
	state_ := filepath.Join(f.dir, "state")
	entries, err := os.ReadDir(state_)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(state_, e.Name()))
		for _, secret := range []string{private, "thr_child_", "provider_private1"} {
			// The stand-in bb gives each lane a title holding the private text;
			// lane-titles.json is the one file meant to keep a title, for the
			// panel's identifier view. It still holds no lane ID or reference.
			if secret == private && e.Name() == state.LaneTitlesName {
				continue
			}
			if strings.Contains(string(b), secret) {
				t.Errorf("%s holds %q", e.Name(), secret)
			}
		}
	}
	code, out, errs := f.run("status")
	if code != exitOK || !strings.Contains(out, "lane recovery: on; last lane work at the check of ") || !strings.Contains(out, "asked BB to retry 1 lane(s), gave up on 0") ||
		!strings.Contains(out, "left alone: 1 (rate_limit)") || !strings.Contains(out, "1 of at most 20 retries used for this failure") ||
		!strings.Contains(out, "keepgoing does not message them, and stops them only for stop --lanes; lane recovery is on") || strings.Contains(out+errs, private) || strings.Contains(out+errs, "thr_child") {
		t.Fatalf("status %d\n%s%s", code, out, errs)
	}
	code, out, errs = f.run("status", "--json")
	if code != exitOK || !strings.Contains(out, `"laneRecovery": {`) || !strings.Contains(out, `"enabled": true`) || !strings.Contains(out, `"retried": 1`) {
		t.Fatalf("status json %d\n%s%s", code, out, errs)
	}
	// The saved record holds a hash per lane, keyed with the loop's
	// lanes.key. status shows the counts and never the record or the key.
	saved, err := state.Load(state_)
	key, kerr := os.ReadFile(filepath.Join(state_, state.LaneKeyName))
	if err != nil || kerr != nil || len(strings.TrimSpace(string(key))) != 64 || saved.Lanes == nil || len(saved.Lanes.Retries) != 2 {
		t.Fatalf("%+v %v", saved.Lanes, err)
	}
	// The lane record is keyed: it does not use the unkeyed hash that the
	// child-error records and the run graph already hold for the same child.
	unkeyed := map[string]bool{}
	for _, id := range lanes {
		unkeyed[decide.ChildHashes([]string{id})[0]] = true
	}
	for h := range saved.Lanes.Retries {
		if strings.Contains(out, h) || unkeyed[h] {
			t.Fatalf("lane hash %s is unkeyed or shown by status:\n%s", h, out)
		}
	}
	if lanesFile, _ := os.ReadFile(filepath.Join(state_, "targets", "primary", "bb-lanes.json")); len(lanesFile) == 0 {
		t.Fatal("no lane summary file")
	} else {
		for h := range unkeyed {
			if strings.Contains(string(lanesFile), h) {
				t.Fatalf("bb-lanes.json holds an unkeyed lane hash")
			}
		}
	}
	if strings.Contains(out, strings.TrimSpace(string(key))) || strings.Contains(out, `"seenActive"`) {
		t.Fatalf("status shows the lane record:\n%s", out)
	}
	code, out, errs = f.run("timeline", "--kind", "child_retry")
	if code != exitOK || errs != "" || !strings.Contains(out, "LANES: asked BB to retry the failed turn of 1 lane(s) by reference") ||
		!strings.Contains(out, "last recorded lane work (failed child turns re-run by reference; not proof they succeeded): incidents.json #") || strings.Contains(out, private) || strings.Contains(out, "thr_child") {
		t.Fatalf("timeline %d %s\n%s", code, errs, out)
	}
	code, out, errs = f.run("timeline", "--kind", "child_retry", "--json")
	if code != exitOK || errs != "" || !strings.Contains(out, `"schemaVersion": 6`) || !strings.Contains(out, `"lastChildRetry"`) {
		t.Fatalf("timeline json %d %s\n%s", code, errs, out)
	}

	// The same outage with the option left off: lanes are counted and
	// announced as before, and none is inspected or retried.
	f = setup(false)
	outage(f)
	r = f.tick()
	if r["action"] != "wake" || r["erroredChildren"] != float64(3) || r["lanes"] != nil || r["childErrorNote"] != true {
		t.Fatalf("option off: %v", r)
	}
	calls = f.read("calls")
	if laneCalls(f, "retry")+laneCalls(f, "log") != 0 || !strings.Contains(f.read("thr_primary.told"), "[keepgoing:child-errors count=3]") {
		t.Fatalf("option off:\n%s", calls)
	}
	code, out, _ = f.run("status")
	if code != exitOK || strings.Contains(out, "lane recovery") {
		t.Fatalf("option off: status %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "state", "targets", "primary", "bb-lanes.json")); err == nil {
		t.Fatal("option off: a lane summary was written")
	}
}
