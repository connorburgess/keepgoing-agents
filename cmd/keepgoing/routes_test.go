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
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

const routesStandardList = `version: 1
routes:
  - {id: large-same, provider: claude-code, model: example-large, effort: high, group: claude-large, enter: same_conversation, probe: {model: example-small}}
  - {id: codex-new, provider: codex, model: example-codex, group: codex-main, enter: new_conversation}
  - {id: codex-headless, provider: codex, model: example-codex, group: codex-main, enter: headless, account_dir: /fictional/private/account}
  - {id: switched-off, provider: claude-code, model: example-other, group: claude-other, enter: same_conversation, disabled: true}
`

type routesFixture struct {
	t        *testing.T
	own      *fixture // a loop with a fallbacks file of its own
	follower string   // config path of a loop that follows the standard list
	registry string
	list     string
}

func newRoutesFixture(t *testing.T) *routesFixture {
	t.Helper()
	own := newFixture(t)
	r := &routesFixture{t: t, own: own, registry: filepath.Join(own.dir, "registry"), list: filepath.Join(own.dir, "user", config.RoutesFileName)}
	if err := os.MkdirAll(filepath.Dir(r.list), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(r.registry, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.RoutesFileEnv, r.list)
	dir := filepath.Join(own.dir, "follower")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r.follower = filepath.Join(dir, "keepgoing.yaml")
	write(t, r.follower, `version: 1
loop:
  name: follower-loop
  state_dir: state
  adapter: bb
  provider: claude-code
  group: claude-main
  bb: {thread: thr_follower_private, model: example-medium, cli: `+filepath.Join(own.dir, "bin", "bb")+`}
  prompt: Continue the fictional loop.
  routes: standard
`, 0o600)
	for _, path := range []string{own.cfg, r.follower} {
		cfg := loadFixture(t, path)
		if _, err := registry.Add(r.registry, cfg.Loop.Name, path, cfg.Loop.StateDir); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r *routesFixture) list_(args ...string) (int, routeListsOut, string, string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{"routes", "list", "--directory", r.registry}, args...), &out, &errb)
	var v routeListsOut
	if contains(args, "--json") && code == 0 {
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			r.t.Fatalf("JSON: %v %s", err, &out)
		}
	}
	return code, v, out.String(), errb.String()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func loopView(t *testing.T, v routeListsOut, name string) loopRoutesView {
	t.Helper()
	for _, l := range v.Loops {
		if l.Loop == name {
			return l
		}
	}
	t.Fatalf("loop %s missing from %+v", name, v.Loops)
	return loopRoutesView{}
}

func TestRoutesListShowsStandardListAndEachLoopsEffectiveRoutes(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	code, v, raw, errs := r.list_("--json")
	if code != 0 || v.SchemaVersion != 1 || v.Kind != "route_lists" {
		t.Fatalf("%d %s %s", code, raw, errs)
	}
	// The standard list, in order, and who follows it.
	if v.Standard.State != "ok" || len(v.Standard.Routes) != 4 || v.Standard.Routes[0].ID != "large-same" || v.Standard.Routes[0].Enter != "same_conversation" || v.Standard.Routes[0].ProbeModel != "example-small" || !v.Standard.Routes[3].Disabled {
		t.Fatalf("standard: %+v", v.Standard)
	}
	if strings.Join(v.Standard.Followers, ",") != "follower-loop" {
		t.Fatalf("followers: %v", v.Standard.Followers)
	}
	// The follower's effective routes come from the standard list.
	f := loopView(t, v, "follower-loop")
	if f.List != "standard" || f.ListUnreadable || f.Current != "primary" || f.Handoff != "on" || f.AllowsNewConversation {
		t.Fatalf("follower: %+v", f)
	}
	if f.First == nil || f.First.ID != "primary" || f.First.Model != "example-medium" || f.First.Source != "loop" || !f.First.Current {
		t.Fatalf("first route: %+v", f.First)
	}
	if len(f.Routes) != 1 || f.Routes[0].ID != "large-same" || f.Routes[0].Source != "standard" || f.Routes[0].Enter != "same_conversation" || f.Routes[0].Model != "example-large" || f.Routes[0].Effort != "high" || f.Routes[0].Current || f.Routes[0].NotInList {
		t.Fatalf("follower routes: %+v", f.Routes)
	}
	skipped := map[string]string{}
	for _, s := range f.Skipped {
		skipped[s.ID] = s.Reason
	}
	if len(skipped) != 3 || !strings.Contains(skipped["codex-new"], "does not allow") || !strings.Contains(skipped["codex-headless"], "headless") || !strings.Contains(skipped["switched-off"], "switched off") {
		t.Fatalf("skipped: %v", skipped)
	}
	// The other loop keeps its own list.
	o := loopView(t, v, "example-loop")
	if o.List != "own" || len(o.Routes) != 2 || o.Routes[0].ID != "claude-other-account" || o.Routes[0].Source != "own" || o.Routes[0].Enter != "other_conversation" || len(o.Skipped) != 0 || o.Redundancy == nil {
		t.Fatalf("own: %+v", o)
	}
	// Nothing private: no conversation ID, account directory or path.
	for _, private := range []string{"thr_", "/fictional/private", r.own.dir, "account_dir", "cli"} {
		if strings.Contains(raw, private) {
			t.Fatalf("output contains %q:\n%s", private, raw)
		}
	}
	// Reading wrote nothing.
	for _, name := range []string{"state.json", "health.json", "route-current.yaml"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(r.follower), "state", name)); !os.IsNotExist(err) {
			t.Fatalf("routes list wrote %s", name)
		}
	}

	// The text form says the same in words.
	code, _, text, _ := r.list_()
	for _, want := range []string{"Standard route list: 4 routes; followed by 1 of the loops shown.", "follower-loop: follows the standard list; handoff on; now on primary.", "example-loop: has its own list", "not used here: codex-new", "same conversation"} {
		if code != 0 || !strings.Contains(text, want) {
			t.Fatalf("text form lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "thr_") {
		t.Fatalf("text form shows a conversation ID:\n%s", text)
	}

	// One loop, by name or by file.
	if _, v, _, _ := r.list_("--json", "--loop", "follower-loop"); len(v.Loops) != 1 || v.Loops[0].Loop != "follower-loop" {
		t.Fatalf("--loop: %+v", v.Loops)
	}
	if _, v, _, _ := r.list_("--json", "-c", r.own.cfg); len(v.Loops) != 1 || v.Loops[0].Loop != "example-loop" || len(v.Standard.Followers) != 0 {
		t.Fatalf("-c: %+v", v)
	}
	if code, _, _, errs := r.list_("--json", "--loop", "absent-loop"); code != exitUsage || !strings.Contains(errs, "no registered loop") {
		t.Fatalf("unknown loop: %d %s", code, errs)
	}
	if code, _, _, _ := r.list_("--json", "--loop", "follower-loop", "-c", r.own.cfg); code != exitUsage {
		t.Fatalf("both selectors accepted: %d", code)
	}
}

func TestRoutesListWithUnusableStandardListStillAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, content, state, problem string
		mode                          os.FileMode
	}{
		{"missing", "", "missing", "", 0o600},
		{"invalid", "version: 1\nroutes:\n  - {id: bad, provider: other, model: m, group: g, enter: headless}\n", "unreadable", "invalid", 0o600},
		{"not_private", routesStandardList, "unreadable", "not_private", 0o644},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRoutesFixture(t)
			if tc.content != "" {
				write(t, r.list, tc.content, tc.mode)
				if err := os.Chmod(r.list, tc.mode); err != nil {
					t.Fatal(err)
				}
			}
			code, v, raw, errs := r.list_("--json")
			if code != 0 || v.Standard.State != tc.state || v.Standard.Problem != tc.problem || len(v.Standard.Routes) != 0 || strings.Contains(raw, r.list) {
				t.Fatalf("%d %s %s", code, raw, errs)
			}
			if tc.problem != "" {
				if _, _, text, _ := r.list_(); !strings.Contains(text, "unreadable ("+tc.problem+")") {
					t.Fatalf("text form lacks the reason:\n%s", text)
				}
			}
			f := loopView(t, v, "follower-loop")
			if f.List != "standard" || !f.ListUnreadable || len(f.Routes) != 0 || f.First == nil || !strings.Contains(strings.Join(f.Warnings, "\n"), config.StandardRoutesUnreadable) {
				t.Fatalf("follower: %+v", f)
			}
			if o := loopView(t, v, "example-loop"); o.List != "own" || len(o.Routes) != 2 {
				t.Fatalf("a loop with its own list was affected: %+v", o)
			}
			// validate and status say so and do not fail.
			var out, errb bytes.Buffer
			if code := run(context.Background(), []string{"validate", "-c", r.follower}, &out, &errb); code != 0 || !strings.Contains(errb.String(), config.StandardRoutesUnreadable) {
				t.Fatalf("validate: %d %s %s", code, &out, &errb)
			}
			if tc.problem == "invalid" && !strings.Contains(errb.String(), "provider must be claude-code or codex") {
				t.Fatalf("validate does not say why the list is invalid: %s", &errb)
			}
			out.Reset()
			errb.Reset()
			if code := run(context.Background(), []string{"status", "-c", r.follower}, &out, &errb); code != 0 || !strings.Contains(out.String(), config.StandardRoutesUnreadable) {
				t.Fatalf("status: %d %s %s", code, &out, &errb)
			}
		})
	}

	t.Run("unloadable_loop_is_named_not_fatal", func(t *testing.T) {
		r := newRoutesFixture(t)
		write(t, r.list, routesStandardList, 0o600)
		write(t, r.follower, "version: 1\nloop: {name: follower-loop\n", 0o600)
		code, v, raw, _ := r.list_("--json")
		if code != 0 || len(v.Loops) != 2 {
			t.Fatalf("%d %s", code, raw)
		}
		broken := false
		for _, l := range v.Loops {
			// Named by its registered name, not its directory.
			broken = broken || (l.Problem == "configuration_unavailable" && l.Loop == "follower-loop")
		}
		if !broken || len(v.Standard.Followers) != 0 {
			t.Fatalf("%+v", v)
		}
	})
}

// A check through the real command, then a change to the standard list: the
// read side shows the loop still on its route, marked as outside the list.
func TestRoutesListMarksCurrentRouteNoLongerInTheList(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	stateDir := filepath.Join(filepath.Dir(r.follower), "state")
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"enable", "-c", r.follower}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if err := state.Save(stateDir, state.File{ActiveTarget: "large-same"}); err != nil {
		t.Fatal(err)
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
	if v := tick(); v["target"] != "large-same" || v["routes"] != nil {
		t.Fatalf("on the route: %v", v)
	}
	// The route is removed from the standard list.
	write(t, r.list, strings.Replace(routesStandardList, "  - {id: large-same, provider: claude-code, model: example-large, effort: high, group: claude-large, enter: same_conversation, probe: {model: example-small}}\n", "", 1), 0o600)
	v := tick()
	routes, _ := v["routes"].(map[string]any)
	if v["target"] != "large-same" || routes == nil || routes["currentNotInList"] != true {
		t.Fatalf("after removal: %v", v)
	}
	_, list, raw, _ := r.list_("--json", "--loop", "follower-loop")
	f := loopView(t, list, "follower-loop")
	if f.Current != "large-same" || len(f.Routes) != 1 || !f.Routes[0].Current || !f.Routes[0].NotInList || f.Routes[0].Source != "standard" || !strings.Contains(strings.Join(f.Warnings, "\n"), "no longer in its list") {
		t.Fatalf("read side: %s", raw)
	}
	// Doctor and status agree that the target is the route the loop is on.
	out.Reset()
	errb.Reset()
	run(context.Background(), []string{"doctor", "-c", r.follower, "--offline", "--json"}, &out, &errb)
	if strings.Contains(out.String(), "Recorded active target is missing") {
		t.Fatalf("doctor disagrees with status: %s", &out)
	}
	// And when the whole list becomes unreadable, the loop is still there.
	if err := os.Remove(r.list); err != nil {
		t.Fatal(err)
	}
	if v := tick(); v["target"] != "large-same" {
		t.Fatalf("unreadable list moved the loop: %v", v)
	}
	if st, _ := state.Load(stateDir); st.Target() != "large-same" || !st.RoutesUnreadable {
		t.Fatalf("state: %+v", st)
	}
}

func TestFallbacksCommandsOnAFollowingLoopPointToTheStandardList(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	for _, args := range [][]string{
		{"fallbacks", "add", "--non-interactive", "--agent", "bb", "--thread", "thr_example_other", "--provider", "codex", "--dry-run"},
		{"fallbacks", "move", "large-same", "--to", "1", "--dry-run"},
		{"fallbacks", "remove", "large-same", "--dry-run"},
		{"fallbacks", "handoff", "off", "--dry-run"},
	} {
		t.Run(args[1], func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(context.Background(), append(args, "-c", r.follower), &out, &errb)
			want := "keepgoing routes add, move, remove, off or on"
			if args[1] == "handoff" {
				want = "errors.failover_on"
			}
			if code == 0 || !strings.Contains(errb.String(), "follows the standard route list") || !strings.Contains(errb.String(), want) {
				t.Fatalf("%d %s %s", code, &out, &errb)
			}
		})
	}
}

// Retarget saves the record of the route first, so removing that route from
// the list straight afterwards, with no check in between, leaves the loop on it.
func TestRetargetThenRouteRemovedKeepsTheLoopThere(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	var out, errb bytes.Buffer
	for _, args := range [][]string{{"enable"}, {"retarget", "large-same", "--note", "fictional test"}} {
		out.Reset()
		errb.Reset()
		if code := run(context.Background(), append(args, "-c", r.follower), &out, &errb); code != 0 {
			t.Fatalf("%v: %d %s %s", args, code, &out, &errb)
		}
	}
	write(t, r.list, strings.Replace(routesStandardList, "  - {id: large-same, provider: claude-code, model: example-large, effort: high, group: claude-large, enter: same_conversation, probe: {model: example-small}}\n", "", 1), 0o600)
	out.Reset()
	errb.Reset()
	if code := run(context.Background(), []string{"tick", "-c", r.follower}, &out, &errb); code != 0 {
		t.Fatalf("tick %d: %s %s", code, &out, &errb)
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	routes, _ := v["routes"].(map[string]any)
	if v["target"] != "large-same" || routes == nil || routes["currentNotInList"] != true {
		t.Fatalf("%v", v)
	}
}
