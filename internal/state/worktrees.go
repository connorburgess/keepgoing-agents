package state

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Worktree is one entry of `git worktree list --porcelain`, as Git reports
// it. It is for read-only inventories; the progress fingerprint keeps its own
// stricter reading.
type Worktree struct {
	Path     string
	Head     string // full commit ID, "" for a bare or empty entry
	Branch   string // short branch name, "" when detached
	Detached bool
	Bare     bool
	Prunable bool
	Locked   bool
}

var commitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ListWorktrees lists every registered worktree of repo, the main one first.
// It runs one read-only Git command and never prunes or repairs anything.
func ListWorktrees(ctx context.Context, repo string) ([]Worktree, error) {
	out, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []Worktree
	for _, entry := range strings.Split(out, "\n\n") {
		var w Worktree
		for _, line := range strings.Split(entry, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				// Git writes forward slashes on Windows too.
				w.Path = filepath.FromSlash(strings.TrimPrefix(line, "worktree "))
			case strings.HasPrefix(line, "HEAD "):
				w.Head = strings.TrimPrefix(line, "HEAD ")
			case strings.HasPrefix(line, "branch "):
				w.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
			case line == "detached":
				w.Detached = true
			case line == "bare":
				w.Bare = true
			case line == "prunable" || strings.HasPrefix(line, "prunable "):
				w.Prunable = true
			case line == "locked" || strings.HasPrefix(line, "locked "):
				w.Locked = true
			}
		}
		if w.Path == "" {
			continue
		}
		if w.Head != "" && !commitID.MatchString(w.Head) {
			return nil, errors.New("git reported an unrecognised commit ID")
		}
		list = append(list, w)
	}
	if len(list) == 0 {
		return nil, errors.New("git listed no worktrees")
	}
	return list, nil
}

// DirtyCount is how many tracked files differ from HEAD in tree, by the same
// definition as the progress fingerprint: untracked files are not counted.
// Optional Git locks are off, so the index is not refreshed on disk.
func DirtyCount(ctx context.Context, tree string) (int, error) {
	out, err := git(ctx, tree, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return 0, err
	}
	if out == "" {
		return 0, nil
	}
	return len(strings.Split(out, "\n")), nil
}

// BaseRef is the reference a repository's worktrees are compared with: the
// name given, or the remote's default branch when name is "".
type BaseRef struct {
	Name   string
	Commit string
	// RemoteTracking is true when Name is a reference under refs/remotes.
	RemoteTracking bool
	// UpdatedAt is when the reference last moved, from its own reflog; zero
	// when it has none. A remote-tracking reference moves on a fetch from, or
	// a push to, its remote.
	UpdatedAt time.Time
}

var refName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// ResolveBase resolves the comparison reference in repo without fetching.
func ResolveBase(ctx context.Context, repo, name string) (BaseRef, error) {
	if name == "" {
		out, err := git(ctx, repo, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD")
		if err != nil || out == "" {
			return BaseRef{}, errors.New("the remote's default branch is not recorded; name a base")
		}
		name = out
	}
	if !refName.MatchString(name) || strings.Contains(name, "..") {
		return BaseRef{}, errors.New("unsupported base name")
	}
	commit, err := git(ctx, repo, "rev-parse", "--verify", "-q", name+"^{commit}")
	if err != nil || !commitID.MatchString(commit) {
		return BaseRef{}, errors.New("base does not resolve to a commit")
	}
	b := BaseRef{Name: name, Commit: commit}
	full, err := git(ctx, repo, "rev-parse", "--symbolic-full-name", name)
	if err == nil && strings.HasPrefix(full, "refs/") {
		b.RemoteTracking = strings.HasPrefix(full, "refs/remotes/")
		// The reference's own reflog, unlike FETCH_HEAD, belongs to this
		// reference and is shared by every worktree.
		if out, err := git(ctx, repo, "reflog", "show", "-1", "--format=%ct", full, "--"); err == nil {
			if sec, err := strconv.ParseInt(out, 10, 64); err == nil {
				b.UpdatedAt = time.Unix(sec, 0).UTC()
			}
		}
	}
	return b, nil
}

// Ahead is how many commits head has that base does not.
func Ahead(ctx context.Context, repo, base, head string) (int, error) {
	if !commitID.MatchString(base) || !commitID.MatchString(head) {
		return 0, errors.New("unsupported commit ID")
	}
	out, err := git(ctx, repo, "rev-list", "--count", base+".."+head)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}
