package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Fingerprint summarises the working state of every worktree of the given
// repositories into one hash. It reads only Git metadata and the size and
// modification time of tracked files that differ from HEAD, never file
// contents. Equal fingerprints across ticks mean no visible progress.
func Fingerprint(repositories []string) (string, error) {
	h := sha256.New()
	for _, repo := range repositories {
		trees, err := worktrees(repo)
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

func worktrees(repo string) ([]string, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var trees []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			trees = append(trees, strings.TrimPrefix(line, "worktree "))
		}
	}
	return trees, nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}
