package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/incident"
	"github.com/looptender/looptender/internal/state"
)

// Bounds for one inventory. Hitting one makes the result incomplete; nothing
// is dropped silently.
const (
	inventoryMaxLanes     = 200
	inventoryMaxWorktrees = 500
	inventoryDeadline     = 2 * time.Minute
	// inventoryListLimit bounds the child listing's output.
	inventoryListLimit = 1 << 20
)

type inventoryThread struct {
	Thread      string `json:"thread"`
	Available   bool   `json:"available"`
	Provider    string `json:"provider,omitempty"`
	Status      string `json:"status,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	Host        string `json:"host,omitempty"`
	Path        string `json:"path,omitempty"`
	// Workspace: inside, outside or unavailable, against the progress
	// repositories' registered worktrees.
	Workspace  string `json:"workspace"`
	Repository int    `json:"repository,omitempty"` // 1-based, when inside
	Worktree   string `json:"worktree,omitempty"`   // the worktree it is inside
}

type inventoryWorktree struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Head     string `json:"head,omitempty"`
	Prunable bool   `json:"prunable,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
	Dirty    *int   `json:"dirty,omitempty"` // tracked files changed; nil when unavailable
	Ahead    *int   `json:"ahead,omitempty"` // commits not in the base; nil when unavailable
	// Merged: no commits beyond the base and no tracked changes. Nil when
	// either is unknown.
	Merged *bool `json:"merged,omitempty"`
	// Threads are the target and lanes whose BB working directory is inside
	// this worktree. None means unattributed: no guess is made from names.
	Threads      []string `json:"threads"`
	Unattributed bool     `json:"unattributed"`
}

type inventoryBase struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
	// RemoteTracking is true for a reference under refs/remotes, which
	// moves when a person fetches from or pushes to that remote. A local
	// branch says nothing about the remote.
	RemoteTracking bool `json:"remoteTracking"`
	// UpdatedAt is when the reference last moved, from its reflog; absent
	// when there is no reflog entry.
	UpdatedAt *time.Time `json:"refUpdatedAt,omitempty"`
}

type inventoryRepository struct {
	Index     int                 `json:"index"`
	Path      string              `json:"path"`
	Available bool                `json:"available"`
	Base      *inventoryBase      `json:"base,omitempty"`
	Worktrees []inventoryWorktree `json:"worktrees"`
}

type transferInventory struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Outcome       string                `json:"outcome"`
	ReadAt        time.Time             `json:"readAt"`
	Complete      bool                  `json:"complete"`
	Loop          string                `json:"loop"`
	Controls      *incident.Controls    `json:"controls,omitempty"`
	Owner         string                `json:"owner,omitempty"`
	Pending       bool                  `json:"pendingTransfer,omitempty"`
	Target        inventoryThread       `json:"target"`
	LanesListed   bool                  `json:"lanesListed"`
	Lanes         []inventoryThread     `json:"lanes"`
	Repositories  []inventoryRepository `json:"repositories"`
	Notes         []string              `json:"notes"`
}

var inventoryStatuses = map[string]bool{"pending": true, "idle": true, "starting": true, "active": true, "stopping": true, "error": true}

// transferInventory lists, read-only, what a person would have to move with a
// loop: its BB target, the target's direct child threads from BB metadata, and
// the worktrees of the configured progress repositories. It never reads
// events, interactions or message text, never fetches, never takes a lock and
// never writes. Paths are printed here and nowhere else.
// run answers thread show; listRun answers the child listing, which needs a
// larger output limit.
func transferInventoryCommand(e env, args []string, run, listRun doctorRun) int {
	fs := flag.NewFlagSet("transfer inventory", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("c", "", configFlagHelp("configuration path"))
	asJSON := fs.Bool("json", false, "versioned inventory")
	base := fs.String("base", "", "local reference to compare worktrees with (default: each repository's origin/HEAD); never fetched")
	timeout := fs.Duration("timeout", 5*time.Second, "timeout per read-only BB or Git query (at most 30s)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("transfer: invalid arguments; see transfer inventory --help")
		return exitUsage
	}
	if fs.NArg() != 0 || *timeout <= 0 || *timeout > 30*time.Second {
		e.errf("transfer: inventory takes no positional arguments; timeout must be greater than zero and at most 30s")
		return exitUsage
	}
	if !configDefault(e, "transfer", fs, path) {
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("transfer: configuration unavailable or invalid; inspect it privately")
		return exitUsage
	}
	a := cfg.Loop.Agent
	if a.Adapter != "bb" || a.BB == nil || !transferThreadID.MatchString(a.BB.Thread) {
		e.errf("transfer: inventory requires a BB primary with a valid thread ID")
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(e.ctx, inventoryDeadline)
	defer cancel()
	r := transferInventory{SchemaVersion: 1, Outcome: "inventory", ReadAt: time.Now().UTC(), Complete: true, Loop: cfg.Loop.Name, Lanes: []inventoryThread{}, Repositories: []inventoryRepository{}, Notes: []string{}}
	incomplete := func(note string) {
		r.Complete = false
		for _, n := range r.Notes {
			if n == note {
				return
			}
		}
		r.Notes = append(r.Notes, note)
	}
	// treesComplete is false when any repository's worktrees are missing from
	// the listing; a thread found in none of them is then not called outside.
	treesComplete := true
	var ctl control.File
	if readTransferRecord(cfg.Loop.StateDir, control.FileName, &ctl) != nil || control.CheckOwner(ctl, "", false) != nil {
		incomplete("Control or ownership metadata is unavailable.")
	} else {
		r.Controls = &incident.Controls{Enabled: ctl.Enabled, Paused: ctl.Paused, Stop: ctl.Stop, AwaitingUser: ctl.AwaitingUser, Completed: ctl.Completed}
		if ctl.Ownership != nil {
			r.Owner = ctl.Ownership.ThreadID
		}
		r.Pending = ctl.PendingTransfer != nil
	}
	var st state.File
	if readTransferRecord(cfg.Loop.StateDir, state.FileName, &st) == nil && st.Target() != state.Primary {
		r.Notes = append(r.Notes, "The loop is on a fallback; this inventory describes the configured BB primary.")
	}

	r.Target = inventoryShow(ctx, a.BB.CLI, a.BB.Thread, *timeout, run)
	if !r.Target.Available {
		incomplete("The target thread could not be verified through BB.")
	}
	ids, listed := inventoryChildren(ctx, a.BB.CLI, a.BB.Thread, *timeout, listRun)
	r.LanesListed = listed
	if !listed {
		incomplete("The target's direct child threads could not be listed completely.")
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			incomplete("The inventory deadline passed before every lane was read.")
			break
		}
		lane := inventoryShow(ctx, a.BB.CLI, id, *timeout, run)
		if !lane.Available {
			incomplete("At least one lane could not be verified through BB.")
		}
		r.Lanes = append(r.Lanes, lane)
	}

	total := 0
	type treeRef struct{ repo, tree int }
	var roots []struct {
		root string
		ref  treeRef
	}
	// The configured path itself counts even when Git cannot list its
	// worktrees (the same rule as doctor's workspace check). Tree -1 marks
	// such a root; listed worktrees win a tie because only a strictly
	// longer root replaces a match.
	var configured []struct {
		root string
		ref  treeRef
	}
	for i, repo := range cfg.Loop.Progress.Repositories {
		if root, err := filepath.EvalSymlinks(repo); err == nil {
			configured = append(configured, struct {
				root string
				ref  treeRef
			}{root, treeRef{i, -1}})
		}
	}
	for i, repo := range cfg.Loop.Progress.Repositories {
		ir := inventoryRepository{Index: i + 1, Path: repo, Worktrees: []inventoryWorktree{}}
		qctx, qcancel := context.WithTimeout(ctx, *timeout)
		trees, err := state.ListWorktrees(qctx, repo)
		qcancel()
		if err != nil {
			treesComplete = false
			incomplete(fmt.Sprintf("The worktrees of progress repository %d could not be listed.", i+1))
			r.Repositories = append(r.Repositories, ir)
			continue
		}
		ir.Available = true
		qctx, qcancel = context.WithTimeout(ctx, *timeout)
		b, err := state.ResolveBase(qctx, repo, *base)
		qcancel()
		if err != nil {
			r.Notes = append(r.Notes, fmt.Sprintf("Progress repository %d: no comparison base (%v); ahead and merged are unavailable.", i+1, err))
		} else {
			ir.Base = &inventoryBase{Ref: b.Name, Commit: b.Commit, RemoteTracking: b.RemoteTracking}
			if !b.UpdatedAt.IsZero() {
				ir.Base.UpdatedAt = &b.UpdatedAt
			}
		}
		for _, t := range trees {
			if total >= inventoryMaxWorktrees {
				treesComplete = false
				incomplete(fmt.Sprintf("More than %d worktrees; the rest are not listed.", inventoryMaxWorktrees))
				break
			}
			if ctx.Err() != nil {
				treesComplete = false
				incomplete("The inventory deadline passed before every worktree was read.")
				break
			}
			total++
			w := inventoryWorktree{Path: t.Path, Branch: t.Branch, Detached: t.Detached, Head: t.Head, Prunable: t.Prunable, Locked: t.Locked, Threads: []string{}}
			if !t.Bare && !t.Prunable {
				qctx, qcancel := context.WithTimeout(ctx, *timeout)
				if n, err := state.DirtyCount(qctx, t.Path); err == nil {
					w.Dirty = &n
				} else {
					incomplete("At least one worktree's changed files could not be counted.")
				}
				qcancel()
			}
			if ir.Base != nil && t.Head != "" {
				qctx, qcancel := context.WithTimeout(ctx, *timeout)
				if n, err := state.Ahead(qctx, repo, ir.Base.Commit, t.Head); err == nil {
					w.Ahead = &n
				} else {
					incomplete("At least one worktree's commits beyond the base could not be counted.")
				}
				qcancel()
			}
			// Merged needs both: no commits beyond the base, and no
			// uncommitted tracked work that a person could lose.
			if w.Ahead != nil && w.Dirty != nil {
				merged := *w.Ahead == 0 && *w.Dirty == 0
				w.Merged = &merged
			}
			if root, err := filepath.EvalSymlinks(t.Path); err == nil {
				roots = append(roots, struct {
					root string
					ref  treeRef
				}{root, treeRef{i, len(ir.Worktrees)}})
			}
			ir.Worktrees = append(ir.Worktrees, w)
		}
		r.Repositories = append(r.Repositories, ir)
	}

	roots = append(roots, configured...)
	// Attribute each thread to the deepest worktree containing its BB working
	// directory. Nothing else attributes a worktree.
	attribute := func(t *inventoryThread) {
		if !t.Available || t.Path == "" {
			t.Workspace = "unavailable"
			return
		}
		dir, err := filepath.EvalSymlinks(t.Path)
		if err != nil {
			t.Workspace = "unavailable"
			return
		}
		best := -1
		for k, c := range roots {
			if pathWithin(c.root, dir) && (best < 0 || len(c.root) > len(roots[best].root)) {
				best = k
			}
		}
		if best < 0 {
			t.Workspace = "outside"
			if !treesComplete || len(cfg.Loop.Progress.Repositories) == 0 {
				t.Workspace = "unavailable"
			}
			return
		}
		ref := roots[best].ref
		if ref.tree < 0 {
			// Inside the configured path, whose worktrees were not listed.
			t.Workspace, t.Repository, t.Worktree = "inside", ref.repo+1, cfg.Loop.Progress.Repositories[ref.repo]
			return
		}
		w := &r.Repositories[ref.repo].Worktrees[ref.tree]
		w.Threads = append(w.Threads, t.Thread)
		t.Workspace, t.Repository, t.Worktree = "inside", ref.repo+1, w.Path
	}
	attribute(&r.Target)
	for i := range r.Lanes {
		attribute(&r.Lanes[i])
	}
	for i := range r.Repositories {
		for j := range r.Repositories[i].Worktrees {
			w := &r.Repositories[i].Worktrees[j]
			w.Unattributed = len(w.Threads) == 0
		}
	}
	if len(cfg.Loop.Progress.Repositories) == 0 {
		r.Notes = append(r.Notes, "No progress repositories are configured, so no worktrees are listed.")
	}
	r.Notes = append(r.Notes,
		"Ahead compares each worktree's HEAD with a local reference as it is now; looptender never fetches, so a remote-tracking base is only as fresh as the last fetch or push a person made. Merged means no commits beyond the base and no tracked changes. Pull request state is not checked.",
		"A worktree is attributed to a thread only when BB reports that thread's working directory inside it; worktrees are never matched to lanes by name.",
		"Read from BB metadata and Git only: no events, interactions or message text, no lock, no writes.",
	)

	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if enc.Encode(r) != nil {
			return exitFailed
		}
	} else {
		printInventory(e.stdout, r)
	}
	if !r.Complete {
		return exitFailed
	}
	return exitOK
}

func printInventory(w io.Writer, r transferInventory) {
	completeness := "complete"
	if !r.Complete {
		completeness = "incomplete"
	}
	fmt.Fprintf(w, "Inventory of loop %s (%s, read %s)\n", r.Loop, completeness, r.ReadAt.Format(time.RFC3339))
	if r.Controls != nil {
		fmt.Fprintf(w, "Controls: enabled=%t paused=%t stop=%t awaiting-user=%t completed=%t pending-transfer=%t\n", r.Controls.Enabled, r.Controls.Paused, r.Controls.Stop, r.Controls.AwaitingUser, r.Controls.Completed, r.Pending)
	}
	if r.Owner != "" {
		fmt.Fprintf(w, "Recorded owner: %s\n", r.Owner)
	}
	thread := func(label string, t inventoryThread) {
		if !t.Available {
			fmt.Fprintf(w, "%s %s: unavailable\n", label, t.Thread)
			return
		}
		archived := ""
		if t.Archived {
			archived = " archived"
		}
		fmt.Fprintf(w, "%s %s: provider=%s status=%s%s project=%s environment=%s host=%s\n", label, t.Thread, t.Provider, t.Status, archived, t.Project, t.Environment, t.Host)
		fmt.Fprintf(w, "    path %s\n", t.Path)
		switch t.Workspace {
		case "inside":
			fmt.Fprintf(w, "    inside progress repository %d, worktree %s\n", t.Repository, t.Worktree)
		case "outside":
			fmt.Fprintln(w, "    outside every progress repository and its worktrees")
		default:
			fmt.Fprintln(w, "    workspace unavailable")
		}
	}
	thread("Target", r.Target)
	if r.LanesListed {
		fmt.Fprintf(w, "Lanes: %d direct child threads\n", len(r.Lanes))
	} else {
		fmt.Fprintf(w, "Lanes: listing incomplete, %d read\n", len(r.Lanes))
	}
	for _, l := range r.Lanes {
		thread("  Lane", l)
	}
	for _, repo := range r.Repositories {
		fmt.Fprintf(w, "Progress repository %d: %s\n", repo.Index, repo.Path)
		if !repo.Available {
			fmt.Fprintln(w, "    worktrees unavailable")
			continue
		}
		if repo.Base != nil {
			moved := "no reflog entry"
			if repo.Base.UpdatedAt != nil {
				moved = "last moved " + repo.Base.UpdatedAt.Format(time.RFC3339)
			}
			kind := "remote-tracking"
			if !repo.Base.RemoteTracking {
				kind = "local, not remote-tracking"
			}
			fmt.Fprintf(w, "    compared with %s at %s (%s; %s; not fetched by looptender)\n", repo.Base.Ref, short(repo.Base.Commit), kind, moved)
		}
		for _, t := range repo.Worktrees {
			branch := t.Branch
			if t.Detached || branch == "" {
				branch = "(detached)"
			}
			dirty, ahead := "dirty ?", "ahead ?"
			if t.Dirty != nil {
				dirty = fmt.Sprintf("dirty %d", *t.Dirty)
			}
			if t.Merged != nil && *t.Merged {
				ahead = "merged"
			} else if t.Ahead != nil {
				ahead = fmt.Sprintf("ahead %d", *t.Ahead)
			}
			who := "unattributed"
			if len(t.Threads) > 0 {
				who = fmt.Sprint(t.Threads)
			}
			flags := ""
			if t.Prunable {
				flags += " prunable"
			}
			if t.Locked {
				flags += " locked"
			}
			fmt.Fprintf(w, "    %s  %s  %s  %s  %s  %s%s\n", t.Path, branch, short(t.Head), dirty, ahead, who, flags)
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "Note: %s\n", n)
	}
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

// inventoryShow reads one thread's identity, provider, status and environment
// from `bb thread show --json`. Other fields, including title and any message
// content, are ignored.
func inventoryShow(ctx context.Context, cli, id string, timeout time.Duration, run doctorRun) inventoryThread {
	t := inventoryThread{Thread: id, Workspace: "unavailable"}
	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	b, err := run(qctx, cli, []string{"thread", "show", id, "--json"}, nil, "")
	if err != nil || qctx.Err() != nil || len(b) > 64*1024 {
		return t
	}
	var v struct {
		OK     *bool           `json:"ok"`
		Error  json.RawMessage `json:"error"`
		Thread *struct {
			ID          string          `json:"id"`
			Project     string          `json:"projectId"`
			Environment string          `json:"environmentId"`
			Provider    string          `json:"providerId"`
			Status      string          `json:"status"`
			Archived    json.RawMessage `json:"archivedAt"`
			Deleted     json.RawMessage `json:"deletedAt"`
		} `json:"thread"`
		Environment *struct {
			ID      string `json:"id"`
			Project string `json:"projectId"`
			Host    string `json:"hostId"`
			Path    string `json:"path"`
		} `json:"environment"`
	}
	if json.Unmarshal(b, &v) != nil || (v.OK != nil && !*v.OK) || (len(v.Error) != 0 && string(v.Error) != "null") || v.Thread == nil || v.Environment == nil {
		return t
	}
	th, en := v.Thread, v.Environment
	deleted := len(th.Deleted) != 0 && string(th.Deleted) != "null"
	if th.ID != id || deleted || !transferProjectID.MatchString(th.Project) || !transferEnvironmentID.MatchString(th.Environment) || !transferHostID.MatchString(en.Host) || !transferLabel.MatchString(th.Provider) || en.ID != th.Environment || en.Project != th.Project || !filepath.IsAbs(en.Path) || !inventoryStatuses[th.Status] {
		return t
	}
	t.Available = true
	t.Provider, t.Status, t.Project, t.Environment, t.Host, t.Path = th.Provider, th.Status, th.Project, th.Environment, en.Host, en.Path
	t.Archived = len(th.Archived) != 0 && string(th.Archived) != "null"
	return t
}

// inventoryChildren lists the direct child threads of parent, with the same
// listing and bounds the BB adapter uses. complete is false for anything that
// is not a bounded bare array of valid IDs.
func inventoryChildren(ctx context.Context, cli, parent string, timeout time.Duration, run doctorRun) (ids []string, complete bool) {
	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	b, err := run(qctx, cli, []string{"thread", "list", "--parent-thread", parent, "--include-hidden", "--json"}, nil, "")
	if err != nil || qctx.Err() != nil || len(b) > 1<<20 {
		return nil, false
	}
	var children []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(b, &children) != nil || children == nil {
		return nil, false
	}
	complete = true
	seen := map[string]bool{}
	for _, c := range children {
		if !transferThreadID.MatchString(c.ID) || c.ID == parent {
			complete = false
			continue
		}
		if seen[c.ID] {
			continue
		}
		if len(ids) >= inventoryMaxLanes {
			return ids, false
		}
		seen[c.ID] = true
		ids = append(ids, c.ID)
	}
	return ids, complete
}
