package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// workspaceGit runs git for a fixture with a fixed fictional identity and no
// user or system configuration.
func workspaceGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Example", "-c", "user.email=example@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// workspaceRepo makes a repository with one commit and one linked worktree
// outside the repository's directory, as a hand-made worktree would be.
func workspaceRepo(t *testing.T) (root, repo, linked string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root = t.TempDir()
	repo = filepath.Join(root, "example-repo")
	linked = filepath.Join(root, "elsewhere", "example-lane")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, repo, "init", "-q")
	workspaceGit(t, repo, "commit", "-q", "--allow-empty", "-m", "fixture")
	workspaceGit(t, repo, "worktree", "add", "-q", "-b", "example-lane", linked)
	return root, repo, linked
}

func TestWorkspaceVerdict(t *testing.T) {
	root, repo, linked := workspaceRepo(t)
	other := filepath.Join(root, "example-repo-other")
	notRepo := filepath.Join(root, "not-a-repository")
	for _, dir := range []string{other, notRepo, filepath.Join(repo, "sub"), filepath.Join(linked, "sub")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, path string
		repos      []string
		want, says string
	}{
		{"no repositories", repo, nil, "unavailable", "No progress repositories"},
		{"relative path", "example-repo", []string{repo}, "unavailable", "usable working directory"},
		{"empty path", "", []string{repo}, "unavailable", "usable working directory"},
		{"missing path", filepath.Join(root, "gone"), []string{repo}, "unavailable", "cannot be read"},
		{"repository root", repo, []string{repo}, "ok", "the configured path"},
		{"inside repository", filepath.Join(repo, "sub"), []string{repo}, "ok", "progress repository 1"},
		{"linked worktree", filepath.Join(linked, "sub"), []string{repo}, "ok", "registered worktrees"},
		{"second repository", linked, []string{notRepo, repo}, "ok", "progress repository 2"},
		{"name prefix is not inside", other, []string{repo}, "warning", "outside every configured"},
		{"unlisted repository", other, []string{notRepo}, "unavailable", "could not be listed"},
		{"configured path counts without git", notRepo, []string{notRepo}, "ok", "the configured path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, message := workspaceVerdict(context.Background(), tc.path, tc.repos, 10*time.Second)
			if status != tc.want || !strings.Contains(message, tc.says) {
				t.Fatalf("%s: %s", status, message)
			}
			if strings.Contains(message, root) || (tc.path != "" && strings.Contains(message, tc.path)) {
				t.Fatal("path exposed")
			}
		})
	}
	t.Run("symlink to a worktree", func(t *testing.T) {
		link := filepath.Join(root, "link-to-lane")
		if err := os.Symlink(linked, link); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		if status, _ := workspaceVerdict(context.Background(), link, []string{repo}, 10*time.Second); status != "ok" {
			t.Fatal(status)
		}
	})
}

func workspaceRunner(path string) doctorRun {
	return func(_ context.Context, _ string, args []string, _ map[string]string, _ string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"--version"}) {
			return []byte("codex-cli 1.2.3\n"), nil
		}
		b, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "thr_example"}, "environment": map[string]any{"path": path}})
		return b, nil
	}
}

func TestDoctorWorkspaceWarnsOnly(t *testing.T) {
	root, repo, linked := workspaceRepo(t)
	outside := filepath.Join(root, "unrelated-checkout")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	f := newDoctorFixture(t)
	f.loop["progress"] = map[string]any{"repositories": []string{repo}}
	f.save(t)

	code, r, out := f.run(t, workspaceRunner(outside))
	c := doctorCheckByID(t, r, "agent.primary.workspace")
	if code != exitOK || c.Status != "warning" || c.Fix == "" || strings.Contains(out, outside) {
		t.Fatalf("%d %+v", code, c)
	}
	if code, r, _ = f.run(t, workspaceRunner(linked)); code != exitOK || doctorCheckByID(t, r, "agent.primary.workspace").Status != "ok" {
		t.Fatal("worktree not recognised")
	}
	// No path in the response, an unverified thread, and offline mode never compare.
	if _, r, _ = f.run(t, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		return []byte(`{"thread":{"id":"thr_example"}}`), nil
	}); doctorCheckByID(t, r, "agent.primary.workspace").Status != "unavailable" {
		t.Fatal("missing path compared")
	}
	if _, r, _ = f.run(t, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		return []byte(`{"thread":{"id":"thr_other"},"environment":{"path":"` + filepath.ToSlash(outside) + `"}}`), nil
	}); doctorCheckByID(t, r, "agent.primary.workspace").Status != "unavailable" {
		t.Fatal("unverified thread compared")
	}
	if _, r, _ = f.run(t, nil, "--offline"); doctorCheckByID(t, r, "agent.primary.workspace").Status != "unavailable" {
		t.Fatal("offline compared")
	}
}

func TestTransferPreviewWorkspaceNeverBlocks(t *testing.T) {
	_, repo, linked := workspaceRepo(t)
	path, _ := transferFixture(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("  progress:\n    repositories: ["+strconvQuote(repo)+"]\n")...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(repo), "unrelated-checkout")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, want string }{
		{outside, "warning"},
		{linked, "ok"},
		{filepath.Join(filepath.Dir(repo), "PRIVATE-MISSING"), "unavailable"},
	} {
		runner := transferRunner(t, func(_ string, v map[string]any) {
			v["environment"].(map[string]any)["path"] = tc.path
		})
		code, r, out := runTransfer(t, path, runner)
		if code != exitOK || r.Compatibility != "compatible_metadata" {
			t.Fatalf("workspace changed the outcome: %d %s", code, r.Compatibility)
		}
		got := map[string]string{}
		for _, c := range r.Checks {
			got[c.ID] = c.Status
		}
		if got["workspace.from"] != tc.want || got["workspace.to"] != tc.want {
			t.Fatalf("%s: %v", tc.want, got)
		}
		if strings.Contains(out, tc.path) || strings.Contains(out, repo) {
			t.Fatal("path exposed")
		}
	}
	// Offline previews never run git or report a comparison.
	_, r, _ := runTransfer(t, path, nil, "--offline")
	for _, c := range r.Checks {
		if strings.HasPrefix(c.ID, "workspace.") {
			t.Fatal("offline compared")
		}
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
