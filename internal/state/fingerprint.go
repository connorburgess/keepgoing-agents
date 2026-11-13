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

const fingerprintPrefix = "kg3"

// Fingerprint summarises the working state of every worktree of the given
// repositories. It reads only Git metadata and the size and modification
// time of tracked files that differ from HEAD, never file contents.
//
// The result holds one digest per worktree, keyed by a hash of its path, so
// two observations can be compared worktree by worktree: see SameProgress. A worktree that Git itself marks prunable and whose directory
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
	return parsed{missing: missing, digests: digests}.String(), nil
}

func treeDigest(ctx context.Context, tree string) (string, error) {
	h := sha256.New()
	// --no-show-signature: with log.showSignature set, a signed HEAD would
	// otherwise put its verification lines ahead of the requested format.
	head, err := git(ctx, tree, "log", "-1", "--no-show-signature", "--format=%H %ct")
	if err != nil {
		return "", fmt.Errorf("%s: git log: %w", tree, err)
	}
	commit, committed, ok := strings.Cut(head, " ")
	if _, err := strconv.ParseInt(committed, 10, 64); !ok || err != nil {
		return "", fmt.Errorf("%s: git log: unexpected output", tree)
	}
	fmt.Fprintf(h, "%s\x00", commit)
	status, err := git(ctx, tree, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return "", fmt.Errorf("%s: git status: %w", tree, err)
	}
	fmt.Fprintf(h, "%s\x00", status)
	out, err := git(ctx, tree, "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		return "", fmt.Errorf("%s: git diff: %w", tree, err)
	}
	dirty := "c"
	if status != "" || out != "" {
		dirty = "d"
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
	return committed + "." + dirty + "." + hex.EncodeToString(h.Sum(nil)[:16]), nil
}

// digestParts splits a tree digest. ok is false for anything else.
func digestParts(digest string) (committed int64, dirty bool, ok bool) {
	parts := strings.Split(digest, ".")
	if len(parts) != 3 || (parts[1] != "c" && parts[1] != "d") || parts[2] == "" {
		return 0, false, false
	}
	committed, err := strconv.ParseInt(parts[0], 10, 64)
	return committed, parts[1] == "d", err == nil
}

// parsed is a structured fingerprint: the count of prunable worktrees left
// out, the newest HEAD commit time remembered from earlier observations, and
// one digest per worktree.
type parsed struct {
	missing int
	newest  int64
	digests map[string]string
}

// newestCommit is the newest HEAD commit time the fingerprint knows of: the
// remembered one or any of its own worktrees'.
func (p parsed) newestCommit() int64 {
	newest := p.newest
	for _, digest := range p.digests {
		if committed, _, ok := digestParts(digest); ok && committed > newest {
			newest = committed
		}
	}
	return newest
}

// parseFingerprint splits a structured fingerprint. ok is false for any other
// string, such as one saved by an older build.
func parseFingerprint(fp string) (parsed, bool) {
	parts := strings.Split(fp, "|")
	if len(parts) < 2 || parts[0] != fingerprintPrefix || !strings.HasPrefix(parts[1], "m=") {
		return parsed{}, false
	}
	p := parsed{digests: map[string]string{}}
	var err error
	if p.missing, err = strconv.Atoi(strings.TrimPrefix(parts[1], "m=")); err != nil || p.missing < 0 {
		return parsed{}, false
	}
	for _, part := range parts[2:] {
		key, value, found := strings.Cut(part, "=")
		if !found || key == "" || value == "" {
			return parsed{}, false
		}
		if key == "n" {
			if p.newest, err = strconv.ParseInt(value, 10, 64); err != nil {
				return parsed{}, false
			}
			continue
		}
		p.digests[key] = value
	}
	return p, true
}

func (p parsed) String() string {
	keys := make([]string, 0, len(p.digests))
	for k := range p.digests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%s|m=%d", fingerprintPrefix, p.missing)
	if p.newest > 0 {
		fmt.Fprintf(&b, "|n=%d", p.newest)
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%s", k, p.digests[k])
	}
	return b.String()
}

// Remember returns current carrying the newest HEAD commit time known to
// previous, so that a worktree which goes away and comes back, however many
// checks later, is not mistaken for new work. It is the value to save. One
// number is kept, whatever the number of worktrees or observations.
func Remember(previous, current string) string {
	was, wasOK := parseFingerprint(previous)
	is, isOK := parseFingerprint(current)
	if !wasOK || !isOK {
		return current
	}
	if newest := was.newestCommit(); newest > is.newest {
		is.newest = newest
	}
	return is.String()
}

// SameProgress reports whether two fingerprints show no progress.
//
// A worktree present in both observations is progress when its digest
// changed. A worktree that disappeared is not progress. A worktree seen for
// the first time is progress only when it shows work of its own: tracked
// files that differ from HEAD, or a HEAD commit newer than every commit the
// previous fingerprint knows of, including those it remembers from earlier
// observations (see Remember). A fresh checkout of an existing commit, a
// returning worktree and a newly registered one are therefore not progress by
// themselves, while a commit made in a new worktree is.
//
// A fingerprint from an older build cannot be compared with a structured one,
// and that change of format is not progress either. Two unstructured values
// are compared as they are.
func SameProgress(previous, current string) bool {
	was, wasOK := parseFingerprint(previous)
	is, isOK := parseFingerprint(current)
	if !wasOK && !isOK {
		return previous == current
	}
	if wasOK != isOK {
		return true
	}
	newest := was.newestCommit()
	for key, digest := range is.digests {
		before, shared := was.digests[key]
		if shared {
			if before != digest {
				return false
			}
			continue
		}
		if committed, dirty, ok := digestParts(digest); ok && len(was.digests) > 0 && (dirty || committed > newest) {
			return false
		}
	}
	return true
}

// MissingWorktrees is how many registered worktrees the fingerprint left out
// because Git marks them prunable. Zero for an unstructured fingerprint.
func MissingWorktrees(fingerprint string) int {
	p, _ := parseFingerprint(fingerprint)
	return p.missing
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
