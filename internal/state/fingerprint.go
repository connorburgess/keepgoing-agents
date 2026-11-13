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
	"sort"
	"strconv"
	"strings"
	"time"
)

// FingerprintTimeout bounds one whole fingerprint, and gitTimeout one git
// command within it, so a hung repository or filesystem cannot hold a check
// open. gitWaitDelay bounds how long a killed command's pipes are waited on.
const (
	FingerprintTimeout = 60 * time.Second
	gitTimeout         = 30 * time.Second
	gitWaitDelay       = 2 * time.Second
)

const fingerprintPrefix = "kg2"

// Fingerprint summarises the working state of every worktree of the given
// repositories. It reads only Git metadata and the size and modification
// time of tracked files that differ from HEAD, never file contents.
//
// The result holds one digest per worktree, keyed by a hash of its path, so
// two observations can be compared over the worktrees present in both: see
// SameProgress. A worktree that Git itself marks prunable and whose directory
// is gone (it is not locked) has no working state and is left out and
// counted: see MissingWorktrees. Any other worktree that cannot be read,
// including a locked one whose volume is absent, is an error, because an
// unreadable worktree is not evidence that nothing changed.
func Fingerprint(repositories []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), FingerprintTimeout)
	defer cancel()
	missing := 0
	digests := map[string]string{}
	for _, repo := range repositories {
		trees, gone, err := worktrees(ctx, repo)
		if err != nil {
			return "", fmt.Errorf("%s: %w", repo, err)
		}
		missing += gone
		for _, tree := range trees {
			digest, err := treeDigest(ctx, tree)
			if err != nil {
				return "", err
			}
			key := sha256.Sum256([]byte(tree))
			digests[hex.EncodeToString(key[:8])] = digest
		}
	}
	keys := make([]string, 0, len(digests))
	for k := range digests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%s|m=%d", fingerprintPrefix, missing)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%s", k, digests[k])
	}
	return b.String(), nil
}

func treeDigest(ctx context.Context, tree string) (string, error) {
	h := sha256.New()
	for _, args := range [][]string{
		{"rev-parse", "HEAD"},
		{"status", "--porcelain", "--untracked-files=no"},
	} {
		out, err := git(ctx, tree, args...)
		if err != nil {
			return "", fmt.Errorf("%s: git %s: %w", tree, args[0], err)
		}
		fmt.Fprintf(h, "%s\x00", out)
	}
	out, err := git(ctx, tree, "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		return "", fmt.Errorf("%s: git diff: %w", tree, err)
	}
	for _, name := range strings.Split(out, "\x00") {
		if name == "" {
			continue
		}
		info, err := statWithin(ctx, filepath.Join(tree, name))
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(h, "%s:missing\x00", name)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("tracked file metadata unavailable: %w", err)
		}
		fmt.Fprintf(h, "%s:%d:%d\x00", name, info.Size(), info.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil)[:16]), nil
}

// parseFingerprint splits a structured fingerprint. ok is false for any other
// string, such as one saved by an older build.
func parseFingerprint(fp string) (missing int, digests map[string]string, ok bool) {
	parts := strings.Split(fp, "|")
	if len(parts) < 2 || parts[0] != fingerprintPrefix || !strings.HasPrefix(parts[1], "m=") {
		return 0, nil, false
	}
	missing, err := strconv.Atoi(strings.TrimPrefix(parts[1], "m="))
	if err != nil || missing < 0 {
		return 0, nil, false
	}
	digests = map[string]string{}
	for _, part := range parts[2:] {
		key, digest, found := strings.Cut(part, "=")
		if !found || key == "" || digest == "" {
			return 0, nil, false
		}
		digests[key] = digest
	}
	return missing, digests, true
}

// SameProgress reports whether two fingerprints show no progress. Structured
// fingerprints are compared over the worktrees present in both, so a worktree
// that disappears, returns or is newly registered is not progress by itself;
// its later changes are. A fingerprint from an older build cannot be compared
// with a structured one, and that change of format is not progress either.
// Two unstructured values are compared as they are.
func SameProgress(previous, current string) bool {
	_, was, wasOK := parseFingerprint(previous)
	_, is, isOK := parseFingerprint(current)
	if !wasOK && !isOK {
		return previous == current
	}
	if wasOK != isOK {
		return true
	}
	for key, digest := range was {
		if now, shared := is[key]; shared && now != digest {
			return false
		}
	}
	return true
}

// MissingWorktrees is how many registered worktrees the fingerprint left out
// because Git marks them prunable. Zero for an unstructured fingerprint.
func MissingWorktrees(fingerprint string) int {
	missing, _, _ := parseFingerprint(fingerprint)
	return missing
}

// worktrees lists the registered worktrees of repo that can be read and
// counts those Git marks prunable.
func worktrees(ctx context.Context, repo string) (trees []string, missing int, err error) {
	out, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, 0, err
	}
	for i, entry := range strings.Split(out, "\n\n") {
		tree, prunable := "", false
		for _, line := range strings.Split(entry, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				tree = strings.TrimPrefix(line, "worktree ")
			case line == "prunable" || strings.HasPrefix(line, "prunable "):
				prunable = true
			}
		}
		if tree == "" {
			continue
		}
		_, err := statWithin(ctx, tree)
		// Left out only when Git marks the entry prunable and its directory is
		// really gone. The first entry is the repository that just answered.
		if prunable && i > 0 && errors.Is(err, os.ErrNotExist) {
			missing++
			continue
		}
		if prunable {
			return nil, 0, errors.New("registered worktree is marked prunable but its directory still exists")
		}
		if err != nil {
			// Not prunable by Git's account, yet unreadable: a locked worktree
			// on an absent volume, or a Git too old to say. Fail closed.
			return nil, 0, fmt.Errorf("registered worktree cannot be inspected and is not marked prunable: %w", err)
		}
		trees = append(trees, tree)
	}
	return trees, missing, nil
}

// statWithin is os.Stat that gives up when ctx ends, for filesystems that
// hang instead of failing.
func statWithin(ctx context.Context, path string) (os.FileInfo, error) {
	type result struct {
		info os.FileInfo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		info, err := os.Stat(path)
		done <- result{info, err}
	}()
	select {
	case r := <-done:
		return r.info, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	cmd.WaitDelay = gitWaitDelay
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}
