//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func (r *routesFixture) change(args ...string) (int, routeChangeOut, string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), append(append([]string{"routes"}, args...), "--directory", r.registry, "--json"), &out, &errb)
	var v routeChangeOut
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		r.t.Fatalf("JSON: %v %s %s", err, &out, &errb)
	}
	return code, v, errb.String()
}

func (r *routesFixture) read() string {
	r.t.Helper()
	b, err := os.ReadFile(r.list)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

const routesHandWritten = `# The routes every loop may follow.
version: 1
redundancy: {min_healthy: 1, span: provider}

routes:
  # Same conversation, larger model.
  - {id: large-same, provider: claude-code, model: example-large, group: claude-large, enter: same_conversation}

  - id: codex-new
    provider: codex
    model: example-codex
    group: codex-main
    enter: new_conversation # starts a fresh conversation
  # Kept for later.
  - {id: spare, provider: claude-code, model: example-other, group: claude-other, enter: same_conversation, disabled: true}
probe: {every: 4h, ttl: 6h}
`

func TestRoutesAddCreatesAPrivateListAndNeverReusesAnID(t *testing.T) {
	r := newRoutesFixture(t)
	code, v, errs := r.change("add", "--id", "large-same", "--provider", "claude-code", "--model", "example-large", "--group", "claude-large", "--enter", "same_conversation", "--dry-run")
	if code != 0 || v.Outcome != "preview" || v.Expect == "" || len(v.Before) != 0 || strings.Join(v.After, ",") != "large-same" {
		t.Fatalf("preview %d %+v %s", code, v, errs)
	}
	if len(v.Loops) != 1 || v.Loops[0].Loop != "follower-loop" || strings.Join(v.Loops[0].After, ",") != "large-same" || v.Loops[0].NoRouteApplies {
		t.Fatalf("effect on the follower: %+v", v.Loops)
	}
	if _, err := os.Stat(r.list); !os.IsNotExist(err) {
		t.Fatalf("a preview wrote the list: %v", err)
	}
	code, v, errs = r.change("add", "--id", "large-same", "--provider", "claude-code", "--model", "example-large", "--group", "claude-large", "--enter", "same_conversation", "--yes", "--expect", v.Expect)
	if code != 0 || v.Outcome != "changed" {
		t.Fatalf("apply %d %+v %s", code, v, errs)
	}
	info, err := os.Stat(r.list)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("list mode: %v %v", info, err)
	}
	want := "version: 1\nroutes:\n  - {id: large-same, provider: claude-code, model: example-large, group: claude-large, enter: same_conversation}\n"
	if got := r.read(); got != want {
		t.Fatalf("list:\n%s", got)
	}
	// A route added at a place, and with a probe login, stays one line.
	code, v, errs = r.change("add", "--id", "codex-headless", "--provider", "codex", "--model", "example-codex", "--group", "codex-main", "--enter", "headless", "--account-dir", "/fictional/private/account with space", "--probe-model", "example-codex-small", "--at", "1", "--yes")
	if code != 0 || v.Outcome != "changed" || strings.Join(v.After, ",") != "codex-headless,large-same" {
		t.Fatalf("add at 1: %d %+v %s", code, v, errs)
	}
	if lines := strings.Split(strings.TrimSpace(r.read()), "\n"); len(lines) != 4 || !strings.HasPrefix(lines[2], "  - {id: codex-headless,") {
		t.Fatalf("list:\n%s", r.read())
	}
	// An id in the list, or one used before, is refused.
	if code, v, _ = r.change("add", "--id", "large-same", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "id_taken" {
		t.Fatalf("taken id: %d %+v", code, v)
	}
	if code, v, _ = r.change("remove", "codex-headless", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("remove: %d %+v", code, v)
	}
	before := r.read()
	if code, v, _ = r.change("add", "--id", "codex-headless", "--provider", "codex", "--model", "example-codex", "--group", "codex-main", "--enter", "headless", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "id_used_before" || r.read() != before {
		t.Fatalf("reused id: %d %+v", code, v)
	}
	// The history names every change, by a person, from the command line.
	hist, err := os.ReadFile(filepath.Join(filepath.Dir(r.list), routesHistoryName))
	if err != nil || strings.Count(string(hist), "\n") != 3 || !strings.Contains(string(hist), `"op":"remove","route":"codex-headless"`) || !strings.Contains(string(hist), `"by":"person","source":"cli"`) {
		t.Fatalf("history: %s %v", hist, err)
	}
	// An unreadable history refuses a new id instead of assuming it is new.
	if err := os.Chmod(filepath.Join(filepath.Dir(r.list), routesHistoryName), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, v, _ = r.change("add", "--id", "brand-new", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "history_unreadable" {
		t.Fatalf("open history: %d %+v", code, v)
	}
	// An invalid route is refused with the reason and nothing is written.
	os.Chmod(filepath.Join(filepath.Dir(r.list), routesHistoryName), 0o600)
	if code, v, _ = r.change("add", "--id", "Bad_ID", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "invalid_route" || !strings.Contains(v.Refusal.Text, "id must be") || r.read() != before {
		t.Fatalf("invalid: %d %+v", code, v)
	}
}

func TestRoutesMoveRemoveOffOnKeepEveryOtherLine(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesHandWritten, 0o600)
	code, v, errs := r.change("move", "spare", "--to", "1", "--yes")
	if code != 0 || v.Outcome != "changed" || v.From != 3 || v.To != 1 || strings.Join(v.After, ",") != "spare,large-same,codex-new" {
		t.Fatalf("move %d %+v %s", code, v, errs)
	}
	want := strings.Replace(routesHandWritten, "routes:\n", "routes:\n  # Kept for later.\n  - {id: spare, provider: claude-code, model: example-other, group: claude-other, enter: same_conversation, disabled: true}\n", 1)
	want = strings.Replace(want, "    enter: new_conversation # starts a fresh conversation\n  # Kept for later.\n  - {id: spare, provider: claude-code, model: example-other, group: claude-other, enter: same_conversation, disabled: true}\n", "    enter: new_conversation # starts a fresh conversation\n", 1)
	if got := r.read(); got != want {
		t.Fatalf("after move:\n%s", got)
	}
	// Off on a block entry adds one line; on turns the flow entry's value.
	if code, v, _ = r.change("off", "codex-new", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("off: %d %+v", code, v)
	}
	want = strings.Replace(want, "  - id: codex-new\n", "  - id: codex-new\n    disabled: true\n", 1)
	if got := r.read(); got != want {
		t.Fatalf("after off:\n%s", got)
	}
	if code, v, _ = r.change("on", "spare", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("on: %d %+v", code, v)
	}
	want = strings.Replace(want, "enter: same_conversation, disabled: true}", "enter: same_conversation, disabled: false}", 1)
	if got := r.read(); got != want {
		t.Fatalf("after on:\n%s", got)
	}
	if code, v, _ = r.change("on", "spare", "--yes"); code != 0 || v.Outcome != "unchanged" {
		t.Fatalf("on again: %d %+v", code, v)
	}
	if code, v, _ = r.change("off", "large-same", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("off flow: %d %+v", code, v)
	}
	if !strings.Contains(r.read(), "enter: same_conversation, disabled: true}\n\n  - id: codex-new") {
		t.Fatalf("after off of a flow entry:\n%s", r.read())
	}
	// Remove takes the entry and its comment, and nothing else.
	if code, v, _ = r.change("remove", "spare", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("remove: %d %+v", code, v)
	}
	if got := r.read(); strings.Contains(got, "Kept for later") || strings.Contains(got, "id: spare") || !strings.HasPrefix(got, "# The routes every loop may follow.\nversion: 1\nredundancy: {min_healthy: 1, span: provider}\n\nroutes:\n  # Same conversation") || !strings.HasSuffix(got, "probe: {every: 4h, ttl: 6h}\n") {
		t.Fatalf("after remove:\n%s", got)
	}
	if code, v, _ = r.change("move", "missing-id", "--to", "1", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "unknown_route" {
		t.Fatalf("unknown: %d %+v", code, v)
	}
	if code, v, _ = r.change("move", "codex-new", "--to", "9", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "bad_position" {
		t.Fatalf("bad place: %d %+v", code, v)
	}
}

func TestRoutesStandardListChangesArePersonOnlyAndBoundToTheReview(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	t.Setenv("BB_THREAD_ID", "thr_fictional_agent")
	code, v, _ := r.change("off", "large-same", "--yes")
	if code == 0 || v.Outcome != "refused" || v.Refusal.Code != "agent_identity" || r.read() != routesStandardList {
		t.Fatalf("agent: %d %+v", code, v)
	}
	_, list, _, _ := r.list_("--json")
	if len(list.Standard.Can) != 0 || list.Standard.CanReason != "agent_identity" {
		t.Fatalf("agent can: %+v", list.Standard)
	}
	t.Setenv("BB_THREAD_ID", "")
	_, list, _, _ = r.list_("--json")
	if strings.Join(list.Standard.Can, ",") != "add,move,remove,off,on" || loopView(t, list, "follower-loop").CanReason != "follows_standard" || strings.Join(loopView(t, list, "example-loop").Can, ",") != "move,remove,off,on" {
		t.Fatalf("person can: %+v", list)
	}
	// A preview, then the list changes: the reviewed apply writes nothing.
	_, v, _ = r.change("move", "codex-new", "--to", "1", "--dry-run")
	changed := strings.Replace(routesStandardList, "model: example-codex, group: codex-main, enter: new_conversation", "model: example-codex-2, group: codex-main, enter: new_conversation", 1)
	write(t, r.list, changed, 0o600)
	code, again, _ := r.change("move", "codex-new", "--to", "1", "--yes", "--expect", v.Expect)
	if code != exitSkipped || again.Outcome != "review_again" || r.read() != changed {
		t.Fatalf("stale review: %d %+v", code, again)
	}
	// So does a change to a following loop's current route.
	_, v, _ = r.change("move", "codex-new", "--to", "1", "--dry-run")
	if err := state.Save(filepath.Join(filepath.Dir(r.follower), "state"), state.File{ActiveTarget: "large-same"}); err != nil {
		t.Fatal(err)
	}
	if code, again, _ = r.change("move", "codex-new", "--to", "1", "--yes", "--expect", v.Expect); code != exitSkipped || again.Outcome != "review_again" {
		t.Fatalf("loop moved: %d %+v", code, again)
	}
	// The preview says what happens to the loop on a route being taken out.
	_, v, _ = r.change("off", "large-same", "--dry-run")
	if len(v.Loops) != 1 {
		t.Fatalf("loops: %+v", v)
	}
	f := v.Loops[0]
	if f.Current != "large-same" || !f.CurrentLeavesList || strings.Join(f.Before, ",") != "large-same" || len(f.After) != 0 || !f.NoRouteApplies || len(f.NewlySkipped) != 1 || f.NewlySkipped[0].ID != "large-same" {
		t.Fatalf("effect: %+v", f)
	}
	if code, v, _ = r.change("off", "large-same", "--yes", "--expect", v.Expect); code != 0 || v.Outcome != "changed" {
		t.Fatalf("reviewed apply: %d %+v", code, v)
	}
}

func TestRoutesEditKeepsASymbolicLinkAndRefusesAnUnsafeList(t *testing.T) {
	r := newRoutesFixture(t)
	real := filepath.Join(t.TempDir(), "dotfiles-routes.yaml")
	write(t, real, routesStandardList, 0o600)
	if err := os.Symlink(real, r.list); err != nil {
		t.Fatal(err)
	}
	if code, v, _ := r.change("move", "codex-headless", "--to", "1", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("%d %+v", code, v)
	}
	if info, err := os.Lstat(r.list); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v %v", info, err)
	}
	if b, _ := os.ReadFile(real); !strings.HasPrefix(string(b), "version: 1\nroutes:\n  - {id: codex-headless,") {
		t.Fatalf("target: %s", b)
	}
	if info, _ := os.Stat(real); info.Mode().Perm() != 0o600 {
		t.Fatalf("target mode %v", info.Mode())
	}
	os.Chmod(real, 0o644)
	if code, v, _ := r.change("move", "codex-headless", "--to", "2", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != config.RoutesNotPrivate {
		t.Fatalf("open list: %d %+v", code, v)
	}
	os.Chmod(real, 0o600)
	write(t, real, "version: 1\nroutes: [{id: a, provider: codex, model: m, group: g, enter: headless}]\n", 0o600)
	if code, v, _ := r.change("off", "a", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "edit_by_hand" {
		t.Fatalf("flow list: %d %+v", code, v)
	}
}

func TestRoutesOwnListChangesGoThroughTheLoop(t *testing.T) {
	r := newRoutesFixture(t)
	fallbacks := filepath.Join(r.own.dir, "fallbacks.yaml")
	before, _ := os.ReadFile(fallbacks)
	code, v, errs := r.change("off", "codex-main", "--loop", "example-loop", "--dry-run")
	if code != 0 || v.Outcome != "preview" || v.List != "own" || v.Loop != "example-loop" || len(v.Loops) != 1 || strings.Join(v.Loops[0].After, ",") != "claude-other-account" {
		t.Fatalf("preview %d %+v %s", code, v, errs)
	}
	if code, v, _ = r.change("off", "codex-main", "--loop", "example-loop", "--yes", "--expect", v.Expect); code != 0 || v.Outcome != "changed" {
		t.Fatalf("off %d %+v", code, v)
	}
	after, _ := os.ReadFile(fallbacks)
	if want := strings.Replace(string(before), "  - id: codex-main\n", "  - id: codex-main\n    disabled: true\n", 1); string(after) != want {
		t.Fatalf("fallbacks:\n%s", after)
	}
	if code, v, _ = r.change("move", "codex-main", "--to", "1", "--loop", "example-loop", "--yes"); code != 0 || v.Outcome != "changed" || strings.Join(v.After, ",") != "codex-main,claude-other-account" {
		t.Fatalf("move %d %+v", code, v)
	}
	// The loop is on a fallback: it cannot be switched off under it.
	if err := state.Save(filepath.Join(r.own.dir, "state"), state.File{ActiveTarget: "claude-other-account"}); err != nil {
		t.Fatal(err)
	}
	if code, v, _ = r.change("off", "claude-other-account", "--loop", "example-loop", "--yes"); code == 0 || v.Refusal == nil || !strings.Contains(v.Refusal.Text, "running on") {
		t.Fatalf("off under the loop: %d %+v", code, v)
	}
	if code, v, _ = r.change("off", "large-same", "--loop", "follower-loop", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "follows_standard" {
		t.Fatalf("follower: %d %+v", code, v)
	}
}

func TestRoutesCheckValidatesWithoutProbing(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"routes", "check", "--json", "--directory", r.registry}, &out, &errb); code != 0 {
		t.Fatalf("%d %s %s", code, &out, &errb)
	}
	var v routeCheckOut
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.State != "ok" || v.Routes != 4 || len(v.Loops) != 1 || strings.Join(v.Loops[0].Routes, ",") != "large-same" {
		t.Fatalf("%v %s", err, &out)
	}
	write(t, r.list, "version: 1\nroutes:\n  - {id: a, provider: other, model: m, group: g, enter: headless}\n", 0o600)
	out.Reset()
	if code := run(context.Background(), []string{"routes", "check", "--json", "--directory", r.registry}, &out, &errb); code != exitFailed {
		t.Fatalf("invalid: %d %s", code, &out)
	}
	v = routeCheckOut{}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.Problem != config.RoutesInvalid || len(v.Errors) == 0 || !strings.Contains(v.Errors[0], "provider") {
		t.Fatalf("%v %s", err, &out)
	}
}

func TestRoutesTimelineNotesAListChangeOnce(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"enable", "-c", r.follower}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	tick := func() map[string]any {
		t.Helper()
		out.Reset()
		errb.Reset()
		if code := run(context.Background(), []string{"tick", "-c", r.follower}, &out, &errb); code != 0 {
			t.Fatalf("tick %d: %s %s", code, &out, &errb)
		}
		var v map[string]any
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := tick(); v["routes"] != nil {
		t.Fatalf("first read: %v", v)
	}
	if code, v, _ := r.change("move", "codex-headless", "--to", "1", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("%d %+v", code, v)
	}
	// A dry run records nothing, and leaves the note to the next check.
	out.Reset()
	if code := run(context.Background(), []string{"tick", "--dry-run", "-c", r.follower}, &out, &errb); code != 0 {
		t.Fatalf("dry run: %s", &errb)
	}
	if runs, _ := os.ReadFile(filepath.Join(filepath.Dir(r.follower), "state", "runs.jsonl")); strings.Contains(string(runs), "listChanged") {
		t.Fatalf("a dry run recorded the note:\n%s", runs)
	}
	v := tick()
	routes, _ := v["routes"].(map[string]any)
	if routes == nil || routes["listChanged"] != true {
		t.Fatalf("after the change: %v", v)
	}
	if v := tick(); v["routes"] != nil {
		t.Fatalf("noted twice: %v", v)
	}
	// The note is in the record of the check and in the timeline, once.
	stateDir := filepath.Join(filepath.Dir(r.follower), "state")
	runs, err := os.ReadFile(filepath.Join(stateDir, "runs.jsonl"))
	if err != nil || strings.Count(string(runs), `"listChanged":true`) != 1 {
		t.Fatalf("runs.jsonl: %v\n%s", err, runs)
	}
	out.Reset()
	if code := run(context.Background(), []string{"timeline", "--json", "-c", r.follower}, &out, &errb); code != 0 {
		t.Fatalf("timeline: %s", &errb)
	}
	var tl timelineReport
	if err := json.Unmarshal(out.Bytes(), &tl); err != nil {
		t.Fatal(err)
	}
	noted := 0
	for _, ev := range tl.Events {
		if contains(ev.Kinds, "route_list") {
			noted++
			if !strings.Contains(ev.Description, "standard route list changed") {
				t.Fatalf("description: %s", ev.Description)
			}
		}
	}
	if noted != 1 {
		t.Fatalf("timeline notes %d: %s", noted, &out)
	}
}

func TestRoutesEditStagesBeforeRecordingAndRefusesUnsafeLinks(t *testing.T) {
	r := newRoutesFixture(t)
	history := filepath.Join(filepath.Dir(r.list), routesHistoryName)
	// A link into a directory that cannot be written: nothing is recorded.
	locked := filepath.Join(t.TempDir(), "locked")
	os.Mkdir(locked, 0o700)
	write(t, filepath.Join(locked, "routes.yaml"), routesStandardList, 0o600)
	if err := os.Symlink(filepath.Join(locked, "routes.yaml"), r.list); err != nil {
		t.Fatal(err)
	}
	os.Chmod(locked, 0o500)
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	if code, v, _ := r.change("move", "codex-new", "--to", "1", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "write_failed" {
		t.Fatalf("read-only target: %d %+v", code, v)
	}
	if _, err := os.Stat(history); !os.IsNotExist(err) {
		t.Fatalf("a failed write was recorded: %v", err)
	}
	// A dangling link is refused while planning, and adds nothing.
	os.Remove(r.list)
	os.Symlink(filepath.Join(t.TempDir(), "missing.yaml"), r.list)
	if code, v, _ := r.change("add", "--id", "new-route", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--dry-run"); code == 0 || v.Refusal == nil || v.Refusal.Code != "dangling_link" {
		t.Fatalf("dangling: %d %+v", code, v)
	}
	// A link into a Git checkout is read, never written.
	checkout := filepath.Join(t.TempDir(), "dotfiles")
	os.MkdirAll(filepath.Join(checkout, ".git"), 0o700)
	write(t, filepath.Join(checkout, "routes.yaml"), routesStandardList, 0o600)
	os.Remove(r.list)
	os.Symlink(filepath.Join(checkout, "routes.yaml"), r.list)
	if _, list, _, _ := r.list_("--json"); list.Standard.State != "ok" {
		t.Fatalf("read through the link: %+v", list.Standard)
	}
	if code, v, _ := r.change("move", "codex-new", "--to", "1", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != config.RoutesInsideCheckout {
		t.Fatalf("checkout: %d %+v", code, v)
	}
	if b, _ := os.ReadFile(filepath.Join(checkout, "routes.yaml")); string(b) != routesStandardList {
		t.Fatal("written into a checkout")
	}
}

func TestRoutesHistoryToleratesATornRecordAndLoopRecordsKeepIDs(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	history := filepath.Join(filepath.Dir(r.list), routesHistoryName)
	write(t, history, `{"at":"2026-11-01T00:00:00Z","op":"remove","route":"old-route","before":["old-route"],"after":[],"by":"person","source":"cli"}`+"\n"+`{"at":"2026-11-02T00:00:00Z","op":"add","rou`, 0o600)
	code, v, _ := r.change("add", "--id", "fresh-route", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--yes")
	if code != 0 || v.Outcome != "changed" || !strings.Contains(strings.Join(v.Warnings, "\n"), "cut short") {
		t.Fatalf("torn last line: %d %+v", code, v)
	}
	b, _ := os.ReadFile(history)
	if lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"); len(lines) != 3 || !strings.HasPrefix(lines[2], `{"at":`) || !strings.Contains(lines[2], "fresh-route") {
		t.Fatalf("history:\n%s", b)
	}
	if code, v, _ = r.change("add", "--id", "old-route", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--yes"); code == 0 || v.Refusal.Code != "id_used_before" {
		t.Fatalf("old id: %d %+v", code, v)
	}
	// A torn line in the middle is not skipped.
	write(t, history, "not a record\n"+string(b), 0o600)
	if code, v, _ = r.change("add", "--id", "other-route", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--yes"); code == 0 || v.Refusal.Code != "history_unreadable" || !strings.Contains(v.Refusal.Text, "move routes-history.jsonl aside") {
		t.Fatalf("torn middle: %d %+v", code, v)
	}
	_, list, _, _ := r.list_("--json")
	if strings.Join(list.Standard.Can, ",") != "move,remove,off,on" {
		t.Fatalf("can without a readable history: %+v", list.Standard.Can)
	}
	os.Remove(history)
	// An id removed by hand, still in a following loop's records, is refused.
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	reg.Record("hand-removed", probe.Result{OK: true, At: time.Now().UTC()})
	if err := reg.Save(filepath.Join(filepath.Dir(r.follower), "state")); err != nil {
		t.Fatal(err)
	}
	if code, v, _ = r.change("add", "--id", "hand-removed", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--dry-run"); code == 0 || v.Refusal.Code != "id_in_loop_records" || !strings.Contains(v.Refusal.Text, "follower-loop") {
		t.Fatalf("loop records: %d %+v", code, v)
	}
	// An empty list offers only add.
	write(t, r.list, "version: 1\nroutes: # the routes, most preferred first\n", 0o600)
	if _, list, _, _ = r.list_("--json"); strings.Join(list.Standard.Can, ",") != "add" {
		t.Fatalf("can on an empty list: %+v", list.Standard.Can)
	}
	if code, v, _ = r.change("add", "--id", "first-route", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--yes"); code != 0 {
		t.Fatalf("first add: %d %+v", code, v)
	}
	if got := r.read(); got != "version: 1\nroutes: # the routes, most preferred first\n  - {id: first-route, provider: codex, model: example-codex, group: codex-b, enter: headless}\n" {
		t.Fatalf("first add:\n%s", got)
	}
}

func TestRoutesEditNitsExpectCallerAndComments(t *testing.T) {
	r := newRoutesFixture(t)
	const list = `version: 1
routes:
# Most preferred first.
  - {id: one, provider: codex, model: example-codex, group: codex-a, enter: headless}
  - id: two
    provider: codex
    model: example-codex
    group: codex-b
    enter: headless
    # effort: high
  - {id: three, provider: codex, model: example-codex, group: codex-c, enter: headless}
`
	write(t, r.list, list, 0o600)
	if code, v, _ := r.change("move", "one", "--to", "3", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("move: %d %+v", code, v)
	}
	want := "version: 1\nroutes:\n# Most preferred first.\n  - id: two\n    provider: codex\n    model: example-codex\n    group: codex-b\n    enter: headless\n    # effort: high\n  - {id: three, provider: codex, model: example-codex, group: codex-c, enter: headless}\n  - {id: one, provider: codex, model: example-codex, group: codex-a, enter: headless}\n"
	if got := r.read(); got != want {
		t.Fatalf("heading and commented key moved:\n%s", got)
	}
	// A reviewed apply of a no-op, or of a plan now refused, is review_again.
	if code, v, _ := r.change("on", "two", "--yes", "--expect", "stale"); code != exitSkipped || v.Outcome != "review_again" {
		t.Fatalf("no-op: %d %+v", code, v)
	}
	if code, v, _ := r.change("remove", "missing", "--yes", "--expect", "stale"); code != exitSkipped || v.Outcome != "review_again" {
		t.Fatalf("refusal: %d %+v", code, v)
	}
	// Who may change the list is settled before --like looks anything up.
	t.Setenv("BB_THREAD_ID", "thr_fictional_agent")
	if code, v, _ := r.change("add", "--like", "no-such-loop:x", "--yes"); code == 0 || v.Refusal.Code != "agent_identity" {
		t.Fatalf("like as an agent: %d %+v", code, v)
	}
	// An agent may not change a loop's own list while the loop has no owner.
	if code, v, _ := r.change("off", "codex-main", "--loop", "example-loop", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "agent_identity" {
		t.Fatalf("unowned loop: %d %+v", code, v)
	}
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"fallbacks", "move", "codex-main", "--to", "1", "--yes", "-c", r.own.cfg}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "no recorded owner") {
		t.Fatalf("fallbacks move on an unowned loop: %d %s", code, &errb)
	}
	_, lists, _, _ := r.list_("--json")
	if l := loopView(t, lists, "example-loop"); len(l.Can) != 0 || l.CanReason != "agent_identity" {
		t.Fatalf("can: %+v", l)
	}
	// The owning thread may, and the change is in the loop's timeline.
	if code := run(context.Background(), []string{"enable", "-c", r.own.cfg}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if code, v, errs := r.change("off", "codex-main", "--loop", "example-loop", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("owner: %d %+v %s", code, v, errs)
	}
	out.Reset()
	if code := run(context.Background(), []string{"timeline", "--json", "-c", r.own.cfg}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var tl timelineReport
	json.Unmarshal(out.Bytes(), &tl)
	found := false
	for _, ev := range tl.Events {
		if contains(ev.Kinds, "route_change") && ev.Incident.RouteChange.Operation == "off" && ev.Incident.RouteChange.Caller == "owner_thread" && strings.Contains(ev.Description, "by the owning BB thread") {
			found = true
		}
	}
	if !found {
		t.Fatalf("timeline: %s", &out)
	}
}

func TestRoutesEditRefusesADirectoryLinkIntoACheckout(t *testing.T) {
	r := newRoutesFixture(t)
	// GNU Stow folds a directory: the whole keepgoing directory is a link
	// into a dotfiles checkout.
	checkout := filepath.Join(t.TempDir(), "dotfiles")
	folded := filepath.Join(checkout, "keepgoing")
	os.MkdirAll(filepath.Join(checkout, ".git"), 0o700)
	os.MkdirAll(folded, 0o700)
	write(t, filepath.Join(folded, config.RoutesFileName), routesStandardList, 0o600)
	userDir := filepath.Dir(r.list)
	os.RemoveAll(userDir)
	if err := os.Symlink(folded, userDir); err != nil {
		t.Fatal(err)
	}
	if code, v, _ := r.change("move", "codex-new", "--to", "1", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != config.RoutesInsideCheckout {
		t.Fatalf("folded directory: %d %+v", code, v)
	}
	// Nor is a first list added there.
	os.Remove(filepath.Join(folded, config.RoutesFileName))
	if code, v, _ := r.change("add", "--id", "new-route", "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--account-dir", "/fictional/private/account", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != config.RoutesInsideCheckout {
		t.Fatalf("first add: %d %+v", code, v)
	}
	entries, _ := os.ReadDir(folded)
	if len(entries) != 0 {
		t.Fatalf("written into the checkout: %v", entries)
	}
}

func TestRoutesAddRefusesTheIDALoopIsOnOrCannotBeChecked(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	stateDir := filepath.Join(filepath.Dir(r.follower), "state")
	add := func(id string, extra ...string) (int, routeChangeOut) {
		code, v, _ := r.change(append([]string{"add", "--id", id, "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless"}, extra...)...)
		return code, v
	}
	// Put there by retarget, then the route removed by hand.
	if err := state.Save(stateDir, state.File{ActiveTarget: "on-it"}); err != nil {
		t.Fatal(err)
	}
	if code, v := add("on-it", "--dry-run"); code == 0 || v.Refusal == nil || v.Refusal.Code != "id_in_loop_records" || !strings.Contains(v.Refusal.Text, "its current route") {
		t.Fatalf("target: %d %+v", code, v)
	}
	write(t, filepath.Join(stateDir, "route-current.yaml"), "version: 1\nroute: {id: recorded-only, provider: codex, model: example-codex, group: codex-b, enter: headless}\n", 0o600)
	if code, v := add("recorded-only", "--dry-run"); code == 0 || v.Refusal == nil || !strings.Contains(v.Refusal.Text, "the record of the route it is on") {
		t.Fatalf("record: %d %+v", code, v)
	}
	// Records that cannot be read refuse the id.
	write(t, filepath.Join(stateDir, health.FileName), "not json", 0o600)
	if code, v := add("fresh-id", "--dry-run"); code == 0 || v.Refusal == nil || v.Refusal.Code != "loop_records_unreadable" {
		t.Fatalf("unreadable: %d %+v", code, v)
	}
	// A loop whose configuration cannot be loaded is checked through the
	// state directory the registry records.
	os.Remove(filepath.Join(stateDir, health.FileName))
	write(t, r.follower, "not: [a valid configuration\n", 0o600)
	if code, v := add("on-it", "--dry-run"); code == 0 || v.Refusal == nil || v.Refusal.Code != "id_in_loop_records" || !strings.Contains(v.Refusal.Text, "follower-loop") {
		t.Fatalf("unloadable loop: %d %+v", code, v)
	}
	if code, v := add("fresh-id", "--dry-run"); code != 0 || v.Outcome != "preview" {
		t.Fatalf("fresh id: %d %+v", code, v)
	}
}

func TestRoutesHistoryRefusesTrailingContentAndReadsSpacedTornRecords(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	history := filepath.Join(filepath.Dir(r.list), routesHistoryName)
	add := func(id string) (int, routeChangeOut) {
		code, v, _ := r.change("add", "--id", id, "--provider", "codex", "--model", "example-codex", "--group", "codex-b", "--enter", "headless", "--dry-run")
		return code, v
	}
	write(t, history, `{"at":"2026-11-01T00:00:00Z","op":"remove","route":"old-route","before":[],"after":[],"by":"person","source":"cli"} and more`+"\n", 0o600)
	if code, v := add("brand-new"); code == 0 || v.Refusal == nil || v.Refusal.Code != "history_unreadable" {
		t.Fatalf("trailing content: %d %+v", code, v)
	}
	write(t, history, `{"at": "2026-11-01T00:00:00Z", "op": "remove", "route": "spaced-id", "before": ["spaced-id", "other-`, 0o600)
	if code, v := add("spaced-id"); code == 0 || v.Refusal == nil || v.Refusal.Code != "id_used_before" {
		t.Fatalf("spaced torn record: %d %+v", code, v)
	}
	if code, v := add("brand-new"); code != 0 || v.Outcome != "preview" {
		t.Fatalf("other id: %d %+v", code, v)
	}
}

func TestRoutesOwnListExpectAndFixedOwnerWording(t *testing.T) {
	r := newRoutesFixture(t)
	// A reviewed apply whose plan is now refused is review_again.
	if code, v, _ := r.change("remove", "no-such-fallback", "--loop", "example-loop", "--yes", "--expect", "stale"); code != exitSkipped || v.Outcome != "review_again" {
		t.Fatalf("own-list expect: %d %+v", code, v)
	}
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"enable", "-c", r.own.cfg}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	t.Setenv("BB_THREAD_ID", "thr_example_visitor")
	code, v, _ := r.change("off", "codex-main", "--loop", "example-loop", "--yes")
	if code == 0 || v.Refusal == nil || v.Refusal.Code != "not_owner" || strings.Contains(v.Refusal.Text, "thr_") || strings.Contains(v.Refusal.Text, "\n") {
		t.Fatalf("not owner: %d %+v", code, v)
	}
}
