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

	"github.com/connorburgess/keepgoing-agents/internal/config"
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
	if strings.Join(list.Standard.Can, ",") != "add,move,remove,off,on" || strings.Join(loopView(t, list, "follower-loop").Can, ",") != "own" || strings.Join(loopView(t, list, "example-loop").Can, ",") != "move,remove,off,on,follow_standard" {
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
	v := tick()
	routes, _ := v["routes"].(map[string]any)
	if routes == nil || routes["listChanged"] != true {
		t.Fatalf("after the change: %v", v)
	}
	if v := tick(); v["routes"] != nil {
		t.Fatalf("noted twice: %v", v)
	}
}
