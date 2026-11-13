package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Fingerprint summarises the working state of every worktree of the given
// repositories that exists on disk into one hash. It reads only Git metadata and the size and
// modification time of tracked files that differ from HEAD, never file
// contents. Equal fingerprints across ticks mean no visible progress.
func Fingerprint(repositories []string) (string, error) {
	h := sha256.New()
	for _, repo := range repositories {
		trees, _, err := worktrees(repo)
		if err != nil {
			return "", fmt.Errorf("%s: %w", repo, err)
		}
		for _, tree := range trees {
			for _, args := range [][]string{
				{"rev-parse", "HEAD"},
				{"status", "--porcelain", "--untracked-files=no"},
			} {
				out, err := git(tree, args...)
				if err != nil {
					return "", fmt.Errorf("%s: git %s: %w", tree, args[0], err)
				}
				fmt.Fprintf(h, "%s\x00%s\x00", tree, out)
			}
			out, err := git(tree, "diff", "--name-only", "-z", "HEAD")
			if err != nil {
				return "", fmt.Errorf("%s: git diff: %w", tree, err)
			}
			for _, name := range strings.Split(out, "\x00") {
				if name == "" {
					continue
				}
				info, err := os.Stat(filepath.Join(tree, name))
				if errors.Is(err, os.ErrNotExist) {
					fmt.Fprintf(h, "%s:missing\x00", name)
					continue
				}
				if err != nil {
					return "", fmt.Errorf("tracked file metadata unavailable: %w", err)
				}
				fmt.Fprintf(h, "%s:%d:%d\x00", name, info.Size(), info.ModTime().UnixNano())
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// gitTimeout bounds one git metadata command, so a hung filesystem cannot
// hold a check open.
const gitTimeout = 30 * time.Second

// worktrees lists the registered worktrees of repo that exist on disk, and
// counts those whose directory is gone. A worktree registered under a
// temporary directory disappears on reboot while Git still lists it until
// `git worktree prune`; it has no working state to fingerprint and is
// skipped. Any other failure to inspect a listed directory is an error: an
// unreadable worktree must not be mistaken for an absent one.
func worktrees(repo string) (trees []string, missing int, err error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, 0, err
	}
	for i, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		tree := strings.TrimPrefix(line, "worktree ")
		// The first entry is the repository itself, which just answered.
		if _, err := os.Stat(tree); i > 0 && errors.Is(err, os.ErrNotExist) {
			missing++
			continue
		} else if err != nil {
			return nil, 0, fmt.Errorf("worktree metadata unavailable: %w", err)
		}
		trees = append(trees, tree)
	}
	return trees, missing, nil
}

// MissingWorktrees counts registered worktrees of the repositories whose
// directories no longer exist. It is diagnostic: a repository that cannot be
// listed counts as none, because Fingerprint reports that failure itself.
func MissingWorktrees(repositories []string) int {
	n := 0
	for _, repo := range repositories {
		if _, missing, err := worktrees(repo); err == nil {
			n += missing
		}
	}
	return n
}

func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}
