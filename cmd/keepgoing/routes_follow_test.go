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

func TestRoutesFollowStandardThenOwnChangeOnlyTheRoutesLines(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	stateDir := filepath.Join(r.own.dir, "state")
	// A check result recorded under an id the standard list also uses, for
	// another route: it must not count for the standard one.
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	reg.Record("large-same", probe.Result{OK: true, At: time.Now().UTC()})
	if err := reg.Save(stateDir); err != nil {
		t.Fatal(err)
	}
	cfgBefore, _ := os.ReadFile(r.own.cfg)
	fallbacksBefore, _ := os.ReadFile(filepath.Join(r.own.dir, "fallbacks.yaml"))

	code, v, errs := r.change("follow-standard", "--loop", "example-loop", "--dry-run")
	if code != 0 || v.Outcome != "preview" || v.Op != "follow_standard" || strings.Join(v.Before, ",") != "claude-other-account,codex-main" || strings.Join(v.After, ",") != "large-same" {
		t.Fatalf("preview %d %+v %s", code, v, errs)
	}
	if len(v.Loops) != 1 || len(v.Loops[0].NewlySkipped) != 3 || !strings.Contains(strings.Join(v.Warnings, "\n"), "large-same are cleared") {
		t.Fatalf("preview details: %+v", v)
	}
	if code, v, errs = r.change("follow-standard", "--loop", "example-loop", "--yes", "--expect", v.Expect); code != 0 || v.Outcome != "changed" {
		t.Fatalf("apply %d %+v %s", code, v, errs)
	}
	got, _ := os.ReadFile(r.own.cfg)
	if want := strings.Replace(string(cfgBefore), "  fallbacks: fallbacks.yaml\n", "  routes: standard\n", 1); string(got) != want {
		t.Fatalf("keepgoing.yaml:\n%s", got)
	}
	if kept, _ := os.ReadFile(filepath.Join(r.own.dir, "fallbacks.yaml")); string(kept) != string(fallbacksBefore) {
		t.Fatal("the loop's own fallbacks file changed")
	}
	if reg, err := health.Load(stateDir); err != nil || reg.Candidates["large-same"] != nil {
		t.Fatalf("stale result kept: %v %v", reg, err)
	}
	cfg := loadFixture(t, r.own.cfg)
	if !cfg.Loop.FollowsStandard() || strings.Join(candidateIDs(cfg.FallbackSet), ",") != "large-same" {
		t.Fatalf("loaded: %+v", cfg.RouteList)
	}
	if code, v, _ = r.change("follow-standard", "--loop", "example-loop", "--yes"); code != 0 || v.Outcome != "unchanged" {
		t.Fatalf("again: %d %+v", code, v)
	}
	// A following loop is not offered own while its list cannot be read.
	os.Chmod(r.list, 0o644)
	_, lists, _, _ := r.list_("--json")
	if l := loopView(t, lists, "example-loop"); len(l.Can) != 0 || l.CanReason != "list_unreadable" {
		t.Fatalf("can with an unreadable list: %+v", l)
	}
	os.Chmod(r.list, 0o600)

	// Back to a list of its own: the routes as they resolve now, copied.
	if code, v, _ = r.change("own", "--loop", "example-loop", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "file_exists" {
		t.Fatalf("existing file: %d %+v", code, v)
	}
	if code, v, errs = r.change("own", "--loop", "example-loop", "--file", "own-routes.yaml", "--yes"); code != 0 || v.Outcome != "changed" || strings.Join(v.After, ",") != "large-same" {
		t.Fatalf("own %d %+v %s", code, v, errs)
	}
	got, _ = os.ReadFile(r.own.cfg)
	if want := strings.Replace(string(cfgBefore), "  fallbacks: fallbacks.yaml\n", "  fallbacks: own-routes.yaml\n", 1); string(got) != want {
		t.Fatalf("keepgoing.yaml:\n%s", got)
	}
	if info, err := os.Stat(filepath.Join(r.own.dir, "own-routes.yaml")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("own file: %v %v", info, err)
	}
	cfg = loadFixture(t, r.own.cfg)
	if cfg.Loop.FollowsStandard() || strings.Join(candidateIDs(cfg.FallbackSet), ",") != "large-same" {
		t.Fatalf("loaded own: %+v", cfg.FallbackSet)
	}
}

func TestRoutesOwnDropsRoutesAllowAndFollowRefusesOffTheFirstRoute(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	raw, _ := os.ReadFile(r.follower)
	withAllow := strings.Replace(string(raw), "  routes: standard\n", "  routes: standard # follows the list\n  routes_allow:\n    - new_conversation\n", 1)
	write(t, r.follower, withAllow, 0o600)
	code, v, errs := r.change("own", "--loop", "follower-loop", "--yes")
	if code != 0 || v.Outcome != "changed" || strings.Join(v.After, ",") != "large-same,codex-new" {
		t.Fatalf("own %d %+v %s", code, v, errs)
	}
	got, _ := os.ReadFile(r.follower)
	if want := strings.Replace(string(raw), "  routes: standard\n", "  fallbacks: fallbacks.yaml # follows the list\n", 1); string(got) != want {
		t.Fatalf("keepgoing.yaml:\n%s", got)
	}
	cfg := loadFixture(t, r.follower)
	if c, ok := cfg.FallbackSet.Find("codex-new"); !ok || c.BB == nil || !c.BB.NewThread {
		t.Fatalf("copied route: %+v", cfg.FallbackSet)
	}
	// A loop on one of its fallbacks is moved back before it follows.
	if err := state.Save(filepath.Join(filepath.Dir(r.follower), "state"), state.File{ActiveTarget: "codex-new"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(r.follower)
	if code, v, _ = r.change("follow-standard", "--loop", "follower-loop", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "not_on_first_route" {
		t.Fatalf("off the first route: %d %+v", code, v)
	}
	if after, _ := os.ReadFile(r.follower); string(after) != string(before) {
		t.Fatal("written after a refusal")
	}
	// And the list says what each loop may do.
	_, list, _, _ := r.list_("--json")
	// The loop is on a fallback, so following the list is not offered.
	if strings.Join(loopView(t, list, "follower-loop").Can, ",") != "move,remove,off,on" {
		t.Fatalf("can: %+v", loopView(t, list, "follower-loop"))
	}
}

func TestInitOffersTheStandardListOnlyWhenAsked(t *testing.T) {
	f := newSetupFixture(t)
	list := filepath.Join(f.root, "user", config.RoutesFileName)
	t.Setenv(config.RoutesFileEnv, list)
	if err := os.Mkdir(filepath.Dir(list), 0o700); err != nil {
		t.Fatal(err)
	}
	// A script follows the list only with --routes standard, even when one is written.
	write(t, list, routesStandardList, 0o600)
	if code, _, errOut := f.run(t, f.args(), ""); code != 0 {
		t.Fatalf("script: %d %s", code, errOut)
	}
	if cfg := loadFixture(t, filepath.Join(f.destination, "keepgoing.yaml")); cfg.Loop.FollowsStandard() {
		t.Fatal("a script followed the list without --routes standard")
	}
	other := filepath.Join(f.root, "following")
	code, out, errOut := f.run(t, append(f.args(), "--directory", other, "--routes", "standard", "--json"), "")
	if code != 0 {
		t.Fatalf("--routes standard: %d %s", code, errOut)
	}
	var rep initReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.Routes != "standard" || len(rep.Fallbacks) != 1 || rep.Fallbacks[0].ID != "codex-headless" || strings.Join(rep.Limits.FailoverOn, ",") != "rate_limit" {
		t.Fatalf("report: %v %s", err, out)
	}
	cfg := loadFixture(t, filepath.Join(other, "keepgoing.yaml"))
	if !cfg.Loop.FollowsStandard() || strings.Join(candidateIDs(cfg.FallbackSet), ",") != "codex-headless" {
		t.Fatalf("follower: %+v", cfg.RouteList)
	}
	if _, err := os.Stat(filepath.Join(other, "fallbacks.yaml")); !os.IsNotExist(err) {
		t.Fatalf("a following loop got a fallbacks file: %v", err)
	}
	if code, _, errOut = f.run(t, append(f.args(), "--directory", filepath.Join(f.root, "both"), "--routes", "standard", "--fallback", "claude"), ""); code == 0 || !strings.Contains(errOut, "exclude each other") {
		t.Fatalf("both: %d %s", code, errOut)
	}
	// Asked, the default is yes; and the fallback questions are not asked.
	wizard := filepath.Join(f.root, "wizard")
	input := strings.Join([]string{"example-loop", f.project, "", "Complete and verify the fictional example task.", "no", "codex", "", "", "", "", "", "", "", "", "", "", wizard, ""}, "\n")
	code, out, errOut = f.run(t, nil, input)
	if code != 0 || !strings.Contains(out, "Follow your standard route list for handoffs? 1 of its 4 routes applies to this loop. (yes/no) [yes]") || strings.Contains(out, "Next fallback agent") || !strings.Contains(out, "Routes: follows your standard route list; 1 apply to this loop: codex-headless.") {
		t.Fatalf("wizard: %d %s %s", code, out, errOut)
	}
	if cfg := loadFixture(t, filepath.Join(wizard, "keepgoing.yaml")); !cfg.Loop.FollowsStandard() {
		t.Fatal("wizard default did not follow")
	}
	// An empty list is refused too.
	write(t, list, "version: 1\nroutes: []\n", 0o600)
	if code, _, errOut = f.run(t, append(f.args(), "--directory", filepath.Join(f.root, "empty"), "--routes", "standard"), ""); code == 0 || !strings.Contains(errOut, "has no routes yet") {
		t.Fatalf("empty list: %d %s", code, errOut)
	}
	// The JSON says the handoff setting that is written, even when no route applies yet.
	write(t, list, "version: 1\nroutes:\n  - {id: elsewhere, provider: claude-code, model: example-large, group: claude-large, enter: same_conversation}\n", 0o600)
	code, out, errOut = f.run(t, append(f.args(), "--directory", filepath.Join(f.root, "none-apply"), "--routes", "standard", "--json"), "")
	rep = initReport{}
	if err := json.Unmarshal([]byte(out), &rep); code != 0 || err != nil || len(rep.Fallbacks) != 0 || strings.Join(rep.Limits.FailoverOn, ",") != "rate_limit" {
		t.Fatalf("none apply: %d %v %s %s", code, err, out, errOut)
	}
	// Without a usable list, --routes standard is refused rather than leave a loop with no routes.
	os.Remove(list)
	if code, _, errOut = f.run(t, append(f.args(), "--directory", filepath.Join(f.root, "none"), "--routes", "standard"), ""); code == 0 || !strings.Contains(errOut, "no standard route list has been written") {
		t.Fatalf("no list: %d %s", code, errOut)
	}
}

func TestRoutesFollowAndFallbacksAddHandoffShareTheOwnListRule(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, routesStandardList, 0o600)
	before, _ := os.ReadFile(r.own.cfg)
	// An agent, on a loop with no recorded owner, is refused everywhere.
	t.Setenv("BB_THREAD_ID", "thr_fictional_agent")
	if code, v, _ := r.change("follow-standard", "--loop", "example-loop", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "agent_identity" {
		t.Fatalf("follow-standard: %d %+v", code, v)
	}
	for _, args := range [][]string{
		{"fallbacks", "handoff", "off", "--yes"},
		{"fallbacks", "add", "--non-interactive", "--agent", "bb", "--thread", "thr_example_other", "--provider", "codex"},
	} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), append(args, "-c", r.own.cfg), &out, &errb); code != exitUsage || !strings.Contains(errb.String(), "no recorded owner") {
			t.Fatalf("%v: %d %s", args, code, &errb)
		}
	}
	if after, _ := os.ReadFile(r.own.cfg); string(after) != string(before) {
		t.Fatal("written after a refusal")
	}
	// A person: a reviewed no-op is review_again, and each change is in the timeline.
	t.Setenv("BB_THREAD_ID", "")
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"fallbacks", "handoff", "off", "--yes", "-c", r.own.cfg}, &out, &errb); code != 0 {
		t.Fatalf("handoff off: %s", &errb)
	}
	if code, v, _ := r.change("own", "--loop", "example-loop", "--yes", "--expect", "stale"); code != exitSkipped || v.Outcome != "review_again" {
		t.Fatalf("reviewed no-op: %d %+v", code, v)
	}
	if code, v, _ := r.change("follow-standard", "--loop", "example-loop", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("follow: %d %+v", code, v)
	}
	if got := strings.Join(timelineOps(t, r.own.cfg), ","); got != "handoff_off/terminal,follow_standard/terminal" {
		t.Fatalf("timeline: %s", got)
	}
}

func TestRoutesFollowStandardClearsEveryListedIDWhateverItsSkipReason(t *testing.T) {
	r := newRoutesFixture(t)
	// The loop's own conversation runs example-medium.
	raw, _ := os.ReadFile(r.own.cfg)
	write(t, r.own.cfg, strings.Replace(string(raw), "bb: {thread: thr_primary, cli:", "bb: {thread: thr_primary, model: example-medium, cli:", 1), 0o600)
	write(t, r.list, `version: 1
routes:
  - {id: applies, provider: claude-code, model: example-large, group: claude-large, enter: same_conversation}
  - {id: switched-off, provider: claude-code, model: example-other, group: claude-other, enter: same_conversation, disabled: true}
  - {id: needs-allow, provider: codex, model: example-codex, group: codex-main, enter: new_conversation}
  - {id: same-group, provider: claude-code, model: example-other, group: claude-account-a, enter: same_conversation}
  - {id: same-model, provider: claude-code, model: example-medium, group: claude-other, enter: same_conversation}
  - {id: headless-only, provider: codex, model: example-codex, group: codex-main, enter: headless}
`, 0o600)
	stateDir := filepath.Join(r.own.dir, "state")
	ids := []string{"applies", "switched-off", "needs-allow", "same-group", "same-model", "headless-only"}
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range ids {
		reg.Record(id, probe.Result{OK: true, At: time.Now().UTC()})
	}
	if err := reg.Save(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "targets", "needs-allow"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, v, errs := r.change("follow-standard", "--loop", "example-loop", "--dry-run")
	if code != 0 || strings.Join(v.After, ",") != "applies" || len(v.Loops[0].NewlySkipped) != 5 {
		t.Fatalf("preview: %d %+v %s", code, v, errs)
	}
	warned := strings.Join(v.Warnings, "\n")
	for _, id := range ids {
		if !strings.Contains(warned, id) {
			t.Fatalf("preview does not name %s: %s", id, warned)
		}
	}
	if code, v, errs = r.change("follow-standard", "--loop", "example-loop", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("apply: %d %+v %s", code, v, errs)
	}
	after, err := health.Load(stateDir)
	if err != nil || len(after.Candidates) != 0 {
		t.Fatalf("records kept: %v %v", after.Candidates, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "targets", "needs-allow")); !os.IsNotExist(err) {
		t.Fatalf("the old session can still be resumed: %v", err)
	}
}

func TestRoutesOwnRefusesACopyTheLoopCouldNotLoad(t *testing.T) {
	r := newRoutesFixture(t)
	write(t, r.list, "version: 1\nprobe: {every: 4h, ttl: 6h, timeout: 90s}\nroutes:\n  - {id: large-same, provider: claude-code, model: example-large, group: claude-large, enter: same_conversation}\n", 0o600)
	raw, _ := os.ReadFile(r.follower)
	withTimeout := strings.Replace(string(raw), "  routes: standard\n", "  routes: standard\n  tick_timeout: 170s\n", 1)
	write(t, r.follower, withTimeout, 0o600)
	if cfg := loadFixture(t, r.follower); !cfg.Loop.FollowsStandard() {
		t.Fatal("the following loop should load, with a warning")
	}
	// A check directory left by a killed own, holding a copy of the
	// configuration, is removed by the next own.
	// One another own is using now (changed just now) is left alone.
	leftover := filepath.Join(filepath.Dir(r.follower), ".routes-own-12345")
	os.Mkdir(leftover, 0o700)
	write(t, filepath.Join(leftover, "keepgoing.yaml"), withTimeout, 0o600)
	inUse := filepath.Join(filepath.Dir(r.follower), ".routes-own-67890")
	os.Mkdir(inUse, 0o700)
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(leftover, old, old); err != nil {
		t.Fatal(err)
	}
	removeOwnCheckLeftovers(r.follower)
	if _, err := os.Stat(inUse); err != nil {
		t.Fatalf("a check in use was removed: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("a stale leftover was kept: %v", err)
	}
	os.Remove(inUse)
	os.Mkdir(leftover, 0o700)
	os.Chtimes(leftover, old, old)
	code, v, _ := r.change("own", "--loop", "follower-loop", "--yes")
	if code == 0 || v.Refusal == nil || v.Refusal.Code != "not_possible" || !strings.Contains(v.Refusal.Text, "would not load") || !strings.Contains(v.Refusal.Text, "tick_timeout") || strings.Contains(v.Refusal.Text, r.own.dir) {
		t.Fatalf("own: %d %+v", code, v)
	}
	dir := filepath.Dir(r.follower)
	if got, _ := os.ReadFile(r.follower); string(got) != withTimeout {
		t.Fatal("keepgoing.yaml changed")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "keepgoing.yaml" && e.Name() != "state" {
			t.Fatalf("left behind: %s", e.Name())
		}
	}
	// When the check cannot run, the refusal says so, and names no path.
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	code, v, _ = r.change("own", "--loop", "follower-loop", "--dry-run")
	if code == 0 || v.Refusal == nil || !strings.Contains(v.Refusal.Text, "could not run") || strings.Contains(v.Refusal.Text, "would not load") || strings.Contains(v.Refusal.Text, dir) {
		t.Fatalf("check could not run: %d %+v", code, v)
	}
}
