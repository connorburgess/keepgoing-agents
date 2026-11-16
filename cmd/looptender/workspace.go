package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/looptender/looptender/internal/state"
)

// workspaceVerdict compares the working directory BB reports for a thread with
// the loop's configured progress repositories and their registered worktrees.
// It only warns: an agent may legitimately work elsewhere, so the result never
// blocks a check, a diagnostic or a transfer. The path itself is never part of
// the message. Git is asked only to list worktrees, read-only.
func workspaceVerdict(ctx context.Context, envPath string, repos []string, timeout time.Duration) (status, message string) {
	if len(repos) == 0 {
		return "unavailable", "No progress repositories are configured, so the working directory BB reports for this thread was not compared with them."
	}
	if envPath == "" || !filepath.IsAbs(envPath) {
		return "unavailable", "BB did not report a usable working directory for this thread."
	}
	workspace, err := filepath.EvalSymlinks(envPath)
	if err != nil {
		return "unavailable", "The working directory BB reports for this thread cannot be read on this device; the path is withheld."
	}
	incomplete := false
	for i, repo := range repos {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		trees, err := state.Worktrees(ctx, repo)
		cancel()
		if err != nil {
			incomplete = true
			trees = nil
		}
		// The configured path counts even when Git cannot list its worktrees.
		for j, tree := range append([]string{repo}, trees...) {
			root, err := filepath.EvalSymlinks(tree)
			if err != nil {
				continue
			}
			if pathWithin(root, workspace) {
				where := "the configured path"
				if j == 1 {
					where = "its main working tree"
				} else if j > 1 {
					where = "one of its registered worktrees"
				}
				return "ok", fmt.Sprintf("BB reports this thread's working directory inside progress repository %d, in %s; the path is withheld.", i+1, where)
			}
		}
	}
	if incomplete {
		return "unavailable", "The working directory BB reports for this thread is not inside a configured progress repository, and the worktrees of at least one repository could not be listed."
	}
	return "warning", "BB reports this thread's working directory outside every configured progress repository and its registered worktrees; the path is withheld. Progress is measured only in those repositories, so work done there is not seen."
}

const workspaceFix = "Compare the thread's BB environment with progress.repositories. If the agent really works in another checkout, add that repository or create the thread in an environment that is one of its worktrees. This check only warns."
