package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func inventoryGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Example", "-c", "user.email=example@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

type inventoryFixture struct {
	root, config, repo, laneA, laneB, detached, outside string
}

// newInventoryFixture builds a repository whose recorded origin/HEAD is main,
// with a lane worktree two commits ahead and dirty, a merged lane worktree,
// and a detached worktree no thread uses. All of it is fictional.
func newInventoryFixture(t *testing.T, repositories bool) *inventoryFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// Git reports canonical paths (macOS /private/var, Windows long names).
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &inventoryFixture{root: root, config: filepath.Join(root, "private", "loop.yaml"), repo: filepath.Join(root, "example-repo"), laneA: filepath.Join(root, "hand-made", "lane-a"), laneB: filepath.Join(root, "hand-made", "lane-b"), detached: filepath.Join(root, "hand-made", "old"), outside: filepath.Join(root, "other-checkout")}
	for _, d := range []string{f.repo, f.outside, filepath.Dir(f.config)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	inventoryGit(t, f.repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(f.repo, "README.md"), []byte("example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inventoryGit(t, f.repo, "add", "README.md")
	inventoryGit(t, f.repo, "commit", "-q", "-m", "base")
	inventoryGit(t, f.repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	inventoryGit(t, f.repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	inventoryGit(t, f.repo, "worktree", "add", "-q", "-b", "example-a", f.laneA)
	inventoryGit(t, f.laneA, "commit", "-q", "--allow-empty", "-m", "one")
	inventoryGit(t, f.laneA, "commit", "-q", "--allow-empty", "-m", "two")
	if err := os.WriteFile(filepath.Join(f.laneA, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inventoryGit(t, f.repo, "worktree", "add", "-q", "-b", "example-b", f.laneB)
	if err := os.MkdirAll(filepath.Join(f.laneB, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	inventoryGit(t, f.repo, "worktree", "add", "-q", "--detach", f.detached)
	// Untracked files are not counted; a tracked change at the base is not merged.
	if err := os.WriteFile(filepath.Join(f.laneB, "untracked.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.detached, "README.md"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := "version: 1\nloop:\n  name: example-loop\n  state_dir: ./state\n  adapter: bb\n  provider: codex\n  bb:\n    thread: thr_target\n  prompt: PRIVATE-PROMPT\n"
	if repositories {
		b, _ := json.Marshal(f.repo)
		raw += "  progress:\n    repositories: [" + string(b) + "]\n"
	}
	if err := os.WriteFile(f.config, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// runner answers the child listing and thread show for fictional threads, and
// fails the test on any other BB command.
func (f *inventoryFixture) runner(t *testing.T, children string, mutate func(id string, v map[string]any)) (doctorRun, *[][]string) {
	calls := &[][]string{}
	paths := map[string]string{"thr_target": f.outside, "thr_lane_a": f.laneA, "thr_lane_b": filepath.Join(f.laneB, "sub"), "thr_lane_c": f.outside}
	return func(ctx context.Context, _ string, args []string, env map[string]string, workdir string) ([]byte, error) {
		*calls = append(*calls, args)
		if _, ok := ctx.Deadline(); !ok || env != nil || workdir != "" {
			t.Fatal("unbounded or configured query")
		}
		if reflect.DeepEqual(args, []string{"thread", "list", "--parent-thread", "thr_target", "--include-hidden", "--json"}) {
			return []byte(children), nil
		}
		if len(args) != 4 || args[0] != "thread" || args[1] != "show" || args[3] != "--json" {
			t.Fatalf("unexpected BB command %v", args)
		}
		id := args[2]
		v := map[string]any{
			"thread":      map[string]any{"id": id, "projectId": "proj_example", "environmentId": "env_example", "providerId": "codex", "status": "idle", "archivedAt": nil, "deletedAt": nil, "title": "PRIVATE-TITLE", "lastMessage": "PRIVATE-MESSAGE"},
			"environment": map[string]any{"id": "env_example", "projectId": "proj_example", "hostId": "host_example", "path": paths[id]},
		}
		if mutate != nil {
			mutate(id, v)
		}
		return json.Marshal(v)
	}, calls
}

func (f *inventoryFixture) run(t *testing.T, runner doctorRun, extra ...string) (int, transferInventory, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := transferInventoryCommand(env{ctx: context.Background(), stdout: &out, stderr: &errs}, append([]string{"inventory", "-c", f.config, "--json"}, extra...), runner, runner)
	var r transferInventory
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("no report (%d): %s %s", code, out.String(), errs.String())
	}
	if strings.Contains(out.String()+errs.String(), "PRIVATE-") {
		t.Fatal("private content exposed")
	}
	return code, r, out.String(), errs.String()
}

// gitSnapshot records every file under the repository's Git directory, so a
// test can show that the inventory wrote nothing there.
func gitSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.Walk(filepath.Join(dir, ".git"), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		files[p] = fmt.Sprint(info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestTransferInventoryListsLanesAndWorktrees(t *testing.T) {
	f := newInventoryFixture(t, true)
	runner, calls := f.runner(t, `[{"id":"thr_lane_a"},{"id":"thr_lane_b"},{"id":"thr_lane_c"},{"id":"thr_lane_a"}]`, func(id string, v map[string]any) {
		if id == "thr_lane_b" {
			th := v["thread"].(map[string]any)
			th["providerId"], th["status"], th["archivedAt"] = "claude-code", "active", 123
		}
	})
	before := gitSnapshot(t, f.repo)
	code, r, out, _ := f.run(t, runner)
	if code != exitOK || !r.Complete || r.SchemaVersion != 1 || r.Outcome != "inventory" || r.Loop != "example-loop" || !r.LanesListed {
		t.Fatalf("%d %+v", code, r)
	}
	if len(*calls) != 5 {
		t.Fatalf("expected one list and four shows, got %v", *calls)
	}
	if r.Target.Workspace != "outside" || r.Target.Path != f.outside {
		t.Fatalf("target %+v", r.Target)
	}
	if len(r.Lanes) != 3 {
		t.Fatalf("lanes %+v", r.Lanes)
	}
	a, b, c := r.Lanes[0], r.Lanes[1], r.Lanes[2]
	if a.Workspace != "inside" || a.Repository != 1 || a.Worktree != f.laneA {
		t.Fatalf("lane a %+v", a)
	}
	if b.Provider != "claude-code" || b.Status != "active" || !b.Archived || b.Workspace != "inside" || b.Worktree != f.laneB {
		t.Fatalf("lane b %+v", b)
	}
	if c.Workspace != "outside" {
		t.Fatalf("lane c %+v", c)
	}
	if len(r.Repositories) != 1 || r.Repositories[0].Base == nil || r.Repositories[0].Base.Ref != "origin/main" || len(r.Repositories[0].Base.Commit) != 40 || !r.Repositories[0].Base.RemoteTracking || r.Repositories[0].Base.UpdatedAt == nil {
		t.Fatalf("repository %+v", r.Repositories)
	}
	trees := map[string]inventoryWorktree{}
	for _, w := range r.Repositories[0].Worktrees {
		trees[w.Path] = w
	}
	if len(trees) != 4 {
		t.Fatalf("worktrees %+v", trees)
	}
	main, ta, tb, td := trees[f.repo], trees[f.laneA], trees[f.laneB], trees[f.detached]
	if main.Branch != "main" || !main.Unattributed || *main.Dirty != 0 || !*main.Merged {
		t.Fatalf("main %+v", main)
	}
	if ta.Branch != "example-a" || *ta.Dirty != 1 || *ta.Ahead != 2 || *ta.Merged || !reflect.DeepEqual(ta.Threads, []string{"thr_lane_a"}) || ta.Unattributed {
		t.Fatalf("lane a tree %+v", ta)
	}
	if tb.Branch != "example-b" || *tb.Dirty != 0 || !*tb.Merged || !reflect.DeepEqual(tb.Threads, []string{"thr_lane_b"}) {
		t.Fatalf("lane b tree %+v", tb)
	}
	if !td.Detached || td.Branch != "" || !td.Unattributed || len(td.Head) != 40 || *td.Ahead != 0 || *td.Dirty != 1 || *td.Merged {
		t.Fatalf("detached %+v", td)
	}
	if !strings.Contains(out, "never fetches") || !strings.Contains(out, "no tracked changes") {
		t.Fatal("freshness not stated")
	}
	if !reflect.DeepEqual(before, gitSnapshot(t, f.repo)) {
		t.Fatal("inventory changed the repository")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.config), "state")); !os.IsNotExist(err) {
		t.Fatal("inventory created runtime state")
	}

	// Text output names the same facts.
	var text bytes.Buffer
	if code := transferCommand(env{ctx: context.Background(), stdout: &text, stderr: &text}, []string{"inventory", "-c", f.config}, runner); code != exitOK {
		t.Fatal(text.String())
	}
	for _, want := range []string{"Lanes: 3 direct child threads", "ahead 2", "ahead 0", "dirty 1", "merged", "unattributed", "(detached)", "remote-tracking; last moved", "not fetched by looptender", f.laneA, "outside every progress repository"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text lacks %q:\n%s", want, text.String())
		}
	}
	if strings.Contains(text.String(), "PRIVATE-") {
		t.Fatal("private content in text")
	}
}

func TestTransferInventoryIncompleteResults(t *testing.T) {
	f := newInventoryFixture(t, true)
	for _, tc := range []struct {
		name, children string
		mutate         func(string, map[string]any)
		lanesListed    bool
	}{
		{"envelope listing", `{"threads":[]}`, nil, false},
		{"null listing", `null`, nil, false},
		{"invalid child ID", `[{"id":"PRIVATE-ID"}]`, nil, false},
		{"child that cannot be verified", `[{"id":"thr_lane_a"}]`, func(id string, v map[string]any) {
			if id == "thr_lane_a" {
				v["thread"].(map[string]any)["status"] = "PRIVATE-STATUS"
			}
		}, true},
		{"deleted target", `[]`, func(id string, v map[string]any) {
			if id == "thr_target" {
				v["thread"].(map[string]any)["deletedAt"] = 1
			}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, _ := f.runner(t, tc.children, tc.mutate)
			code, r, _, _ := f.run(t, runner)
			if code != exitFailed || r.Complete || r.LanesListed != tc.lanesListed {
				t.Fatalf("%d %+v", code, r)
			}
		})
	}
	t.Run("too many lanes", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("[")
		for i := 0; i <= inventoryMaxLanes; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":"thr_many_%d"}`, i)
		}
		b.WriteString("]")
		runner, calls := f.runner(t, b.String(), nil)
		code, r, _, _ := f.run(t, runner)
		if code != exitFailed || r.Complete || r.LanesListed || len(r.Lanes) != inventoryMaxLanes || len(*calls) != inventoryMaxLanes+2 {
			t.Fatalf("%d lanes=%d calls=%d", code, len(r.Lanes), len(*calls))
		}
	})
	t.Run("unlistable repository", func(t *testing.T) {
		g := newInventoryFixture(t, false)
		raw, _ := os.ReadFile(g.config)
		plain := filepath.Join(g.root, "not-a-repository")
		if err := os.MkdirAll(filepath.Join(plain, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		missing, _ := json.Marshal(plain)
		raw = append(raw, []byte("  progress:\n    repositories: ["+string(missing)+"]\n")...)
		if err := os.WriteFile(g.config, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		runner, _ := g.runner(t, `[{"id":"thr_lane_c"},{"id":"thr_lane_d"}]`, func(id string, v map[string]any) {
			if id == "thr_lane_d" {
				v["environment"].(map[string]any)["path"] = filepath.Join(plain, "sub")
			}
		})
		code, r, _, _ := g.run(t, runner)
		if code != exitFailed || r.Complete || r.Repositories[0].Available || r.Lanes[0].Workspace != "unavailable" {
			t.Fatalf("%d %+v", code, r)
		}
		// The configured path counts even though Git cannot list it.
		if d := r.Lanes[1]; d.Workspace != "inside" || d.Repository != 1 || d.Worktree != plain {
			t.Fatalf("configured path not counted: %+v", d)
		}
	})
	t.Run("count that fails", func(t *testing.T) {
		g := newInventoryFixture(t, true)
		// A worktree whose .git file is damaged is still listed, but Git
		// cannot count its changes there.
		if err := os.WriteFile(filepath.Join(g.laneA, ".git"), []byte("gitdir: "+filepath.Join(g.root, "nowhere")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runner, _ := g.runner(t, `[]`, nil)
		code, r, _, _ := g.run(t, runner)
		if code != exitFailed || r.Complete {
			t.Fatalf("failed count left the result complete: %d", code)
		}
		for _, w := range r.Repositories[0].Worktrees {
			if w.Path == g.laneA && (w.Dirty != nil || w.Merged != nil) {
				t.Fatalf("%+v", w)
			}
		}
	})
}

func TestTransferInventoryBaseAndRepositories(t *testing.T) {
	f := newInventoryFixture(t, true)
	runner, _ := f.runner(t, `[{"id":"thr_lane_a"}]`, nil)
	code, r, _, _ := f.run(t, runner, "--base", "example-a")
	if code != exitOK || r.Repositories[0].Base.Ref != "example-a" || r.Repositories[0].Base.RemoteTracking {
		t.Fatalf("%d %+v", code, r.Repositories)
	}
	for _, w := range r.Repositories[0].Worktrees {
		if w.Path == f.laneA && (*w.Ahead != 0 || *w.Merged) {
			t.Fatalf("explicit base ignored: %+v", w)
		}
	}
	// A base that does not resolve leaves ahead/merged unavailable; it is not an error.
	code, r, _, _ = f.run(t, runner, "--base", "no-such-branch")
	if code != exitOK || r.Repositories[0].Base != nil || r.Repositories[0].Worktrees[0].Ahead != nil || r.Repositories[0].Worktrees[0].Dirty == nil {
		t.Fatalf("%d %+v", code, r.Repositories)
	}
	for _, bad := range []string{"--upload-pack=x", "a..b", "-x"} {
		if code, r, _, _ = f.run(t, runner, "--base", bad); code != exitOK || r.Repositories[0].Base != nil {
			t.Fatalf("base %q accepted", bad)
		}
	}
	// Without progress repositories nothing is listed and no thread is called outside.
	g := newInventoryFixture(t, false)
	runner, _ = g.runner(t, `[{"id":"thr_lane_a"}]`, nil)
	code, r, _, _ = g.run(t, runner)
	if code != exitOK || len(r.Repositories) != 0 || r.Target.Workspace != "unavailable" || r.Lanes[0].Workspace != "unavailable" {
		t.Fatalf("%d %+v", code, r)
	}
}

func TestTransferInventoryArguments(t *testing.T) {
	f := newInventoryFixture(t, false)
	for _, args := range [][]string{
		{"inventory", "-c", f.config, "extra"},
		{"inventory", "-c", f.config, "--timeout", "0s"},
		{"inventory", "-c", f.config, "--timeout", "31s"},
		{"inventory", "-c", filepath.Join(f.root, "missing.yaml")},
	} {
		var out bytes.Buffer
		if code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, args, nil); code != exitUsage {
			t.Fatalf("%v: %d %s", args, code, out.String())
		}
	}
	raw := "version: 1\nloop:\n  name: example-loop\n  state_dir: ./state\n  adapter: process\n  provider: example\n  process:\n    status: [example-status]\n    start: [example-start]\n    stop: [example-stop]\n"
	if err := os.WriteFile(f.config, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, []string{"inventory", "-c", f.config}, nil); code != exitUsage {
		t.Fatalf("non-BB loop: %d %s", code, out.String())
	}
}
