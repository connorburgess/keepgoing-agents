package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
	"github.com/looptender/looptender/internal/state"
)

var errReconcileSnapshot = errors.New("migration evidence incomplete, changed or unavailable; no review saved; preview again")

func transferReconcileCommand(e env, args []string, run, listRun doctorRun) int {
	if len(args) < 2 {
		e.errf("usage: transfer reconcile preview|apply|show; see transfer reconcile preview --help")
		return exitUsage
	}
	cmd := args[1]
	if cmd != "preview" && cmd != "apply" && cmd != "show" {
		return exitUsage
	}
	fs := flag.NewFlagSet("transfer reconcile "+cmd, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	path := fs.String("c", "", configFlagHelp("successor configuration"))
	sourcePath := fs.String("source-config", "", "predecessor configuration")
	file := fs.String("file", "", "private assignment JSON; never a task or permission instruction")
	digest := fs.String("digest", "", "reviewed preview digest (apply)")
	id := fs.String("id", "", "expected existing review identity")
	revision := fs.Uint64("revision", 0, "expected existing review revision")
	asJSON := fs.Bool("json", false, "minimized summary JSON")
	private := fs.Bool("private", false, "complete private review/history (show only)")
	if err := fs.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() != 0 || (cmd == "show" && (*file != "" || *sourcePath != "" || *digest != "" || *id != "" || *revision != 0)) || (cmd != "show" && (*file == "" || *sourcePath == "" || *private)) || (cmd == "preview" && *digest != "") || (cmd == "apply" && len(*digest) != 64) || ((*id == "") != (*revision == 0)) {
		e.errf("invalid reconciliation flags; see transfer reconcile %s --help", cmd)
		return exitUsage
	}
	if !configDefault(e, "transfer reconcile", fs, path) {
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("successor configuration unavailable; inspect privately")
		return exitUsage
	}
	if cmd == "show" {
		g, err := control.LoadMigrationReview(cfg.Loop.StateDir)
		if err != nil || g == nil {
			e.errf("migration review unavailable; inspect privately")
			return exitFailed
		}
		ident, err := reconcileIdentity(*path, cfg)
		if err != nil || ident != g.Successor {
			e.errf("review belongs to another successor configuration")
			return exitSkipped
		}
		if *private {
			return reconcileJSON(e, g)
		}
		return printReconcile(e, control.MigrationSummaryOf(g.ID, g.Revisions[len(g.Revisions)-1]), *asJSON)
	}
	source, err := config.Load(*sourcePath)
	if err != nil {
		e.errf("predecessor configuration unavailable; inspect privately")
		return exitUsage
	}
	from, err := reconcileIdentity(*sourcePath, source)
	if err != nil {
		e.errf("predecessor must have private configuration/state and a valid BB primary")
		return exitUsage
	}
	to, err := reconcileIdentity(*path, cfg)
	if err != nil || from.StateDir == to.StateDir || from.Thread == to.Thread {
		e.errf("review requires distinct private BB loops and threads")
		return exitUsage
	}
	// Inputs and records are private regular files. Reject aliases into runtime
	// records, configurations or checkouts before reading any assignment content.
	planPath, err := canonicalFuturePath(*file)
	if err != nil || !reconcilePlanPath(planPath, from, to, source, cfg) {
		e.errf("assignment input must be a separate private file outside checkouts and runtime directories")
		return exitUsage
	}
	raw, err := control.ReadMigrationFile(*file, control.MigrationPlanLimit)
	if err != nil {
		e.errf("assignment input must be a bounded regular file without symlinks")
		return exitUsage
	}
	plan, err := control.ParseMigrationPlan(raw)
	if err != nil {
		e.errf("%v", err)
		return exitUsage
	}
	g, err := control.LoadMigrationReview(cfg.Loop.StateDir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	if (g == nil && (*id != "" || *revision != 0)) || (g != nil && (g.ID != *id || uint64(len(g.Revisions)) != *revision || g.Source != from || g.Successor != to)) {
		e.errf("review identity, revision or binding changed; inspect show and preview again")
		return exitSkipped
	}
	ctx, cancel := context.WithTimeout(e.ctx, inventoryDeadline)
	defer cancel()
	readEnv := e
	readEnv.ctx = ctx
	local, err := reconcileLocal(source, cfg)
	if err != nil {
		e.errf("%v", errReconcileSnapshot)
		return exitSkipped
	}
	observation, err := reconcileObserve(readEnv, *sourcePath, source, cfg, run, listRun)
	if err != nil {
		e.errf("%v", errReconcileSnapshot)
		return exitSkipped
	}
	// Independent reads do not form a transaction. Bracket the inventory with
	// a second typed read; changes refuse publication rather than hide a race.
	again, err := reconcileObserve(readEnv, *sourcePath, source, cfg, run, listRun)
	if err != nil || reconcileHash(observation) != reconcileHash(again) {
		e.errf("%v", errReconcileSnapshot)
		return exitSkipped
	}
	after, err := reconcileLocal(source, cfg)
	if err != nil || after != local {
		e.errf("%v", errReconcileSnapshot)
		return exitSkipped
	}
	if err := reconcileAssignments(plan, observation, g); err != nil {
		e.errf("%v", err)
		return exitSkipped
	}
	reviewed := reconcileHash(struct {
		Source, Successor control.MigrationIdentity
		Plan              control.MigrationPlan
		Observation       control.MigrationObservation
		Local             string
		ID                string
		Revision          uint64
	}{from, to, plan, observation, local, *id, *revision})
	observation.ObservedAt = time.Now().UTC()
	r := control.MigrationRevision{RecordedAt: time.Now().UTC(), Digest: reviewed, Plan: plan, Observation: observation}
	if cmd == "preview" {
		s := control.MigrationSummaryOf(*id, r)
		s.Revision = *revision
		s.Outcome = "review_preview_not_execution"
		return printReconcile(e, s, *asJSON)
	}
	if reviewed != *digest {
		e.errf("reviewed evidence changed; no review saved; preview again")
		return exitSkipped
	}
	// Nonwaiting locks do not stop or claim workers. Taking both tick locks
	// excludes cooperating loop mutations only during local validation/publication.
	dirs := []string{from.StateDir, to.StateDir}
	sort.Strings(dirs)
	first, err := lock.Acquire(dirs[0])
	if err != nil {
		return ownershipError(e, err)
	}
	defer first.Release()
	second, err := lock.Acquire(dirs[1])
	if err != nil {
		return ownershipError(e, err)
	}
	defer second.Release()
	verify := func() error {
		if ctx.Err() != nil {
			return errReconcileSnapshot
		}
		now, err := reconcileLocal(source, cfg)
		if err != nil || now != local {
			return errReconcileSnapshot
		}
		fresh, err := control.ReadMigrationFile(*file, control.MigrationPlanLimit)
		if err != nil || !bytes.Equal(fresh, raw) {
			return errReconcileSnapshot
		}
		return nil
	}
	saved, err := control.WriteMigrationReview(cfg.Loop.StateDir, os.Getenv("BB_THREAD_ID"), from, to, r, *id, *revision, verify)
	if err != nil {
		return ownershipError(e, err)
	}
	return printReconcile(e, control.MigrationSummaryOf(saved.ID, saved.Revisions[len(saved.Revisions)-1]), *asJSON)
}
func reconcileIdentity(path string, cfg *config.Config) (control.MigrationIdentity, error) {
	a := cfg.Loop.Agent
	if a.Adapter != "bb" || a.BB == nil || !transferThreadID.MatchString(a.BB.Thread) || transferPrivatePaths(path, cfg) != nil {
		return control.MigrationIdentity{}, errReconcileSnapshot
	}
	p, err := canonicalFuturePath(path)
	if err != nil {
		return control.MigrationIdentity{}, err
	}
	d, err := canonicalFuturePath(cfg.Loop.StateDir)
	if err != nil {
		return control.MigrationIdentity{}, err
	}
	// Require canonical runtime paths: aliases must not defeat the distinct-loop
	// check or lock ordering. Configuration aliases are bound by their real path.
	if d != filepath.Clean(cfg.Loop.StateDir) {
		return control.MigrationIdentity{}, errReconcileSnapshot
	}
	return control.MigrationIdentity{Loop: cfg.Loop.Name, Thread: a.BB.Thread, Config: p, StateDir: d}, nil
}
func reconcilePlanPath(path string, from, to control.MigrationIdentity, cfgs ...*config.Config) bool {
	if path == from.Config || path == to.Config || pathWithin(from.StateDir, path) || pathWithin(to.StateDir, path) {
		return false
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil || !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	for _, cfg := range cfgs {
		for _, repo := range cfg.Loop.Progress.Repositories {
			root, err := canonicalFuturePath(repo)
			if err != nil || pathWithin(root, path) {
				return false
			}
		}
	}
	return true
}

// Bind complete local controls/state/goal snapshots, including unknown fields.
// These private digests are compared, never used to interpret permission.
func reconcileLocal(cfgs ...*config.Config) (string, error) {
	values := []string{}
	for _, cfg := range cfgs {
		if cfg.CheckSources() != nil || control.CheckControlPath(cfg.Loop.StateDir) != nil {
			return "", errReconcileSnapshot
		}
		ctl, err := control.Load(cfg.Loop.StateDir)
		if err != nil || control.CheckOwner(ctl, "", false) != nil || ctl.PendingTransfer != nil || (ctl.BBTarget != "" && ctl.BBTarget != cfg.Loop.Agent.BB.Thread) {
			return "", errReconcileSnapshot
		}
		goal, err := control.LoadGoal(cfg.Loop.StateDir)
		if err != nil || (goal != nil && goal.OwningLoop != cfg.Loop.Name) {
			return "", errReconcileSnapshot
		}
		var st state.File
		if readTransferRecord(cfg.Loop.StateDir, state.FileName, &st) != nil || st.Target() != state.Primary {
			return "", errReconcileSnapshot
		}
		values = append(values, cfg.SourcesDigest())
		for _, name := range []string{control.FileName, state.FileName, control.GoalName} {
			path := filepath.Join(cfg.Loop.StateDir, name)
			if _, err := snapshot.LstatRecord(path); errors.Is(err, os.ErrNotExist) {
				if name == control.FileName {
					return "", errReconcileSnapshot
				}
				values = append(values, "absent")
				continue
			}
			b, err := control.ReadMigrationFile(path, 1<<20)
			if err != nil {
				return "", errReconcileSnapshot
			}
			values = append(values, reconcileHash(b))
		}
	}
	return reconcileHash(values), nil
}
func reconcileHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func reconcileLane(t inventoryThread) control.MigrationLane {
	return control.MigrationLane{Thread: t.Thread, Provider: t.Provider, Status: t.Status, Archived: t.Archived, Project: t.Project, Environment: t.Environment, Host: t.Host, Path: t.Path, Worktree: t.Worktree}
}
func reconcileObserve(e env, path string, source, dest *config.Config, run, listRun doctorRun) (control.MigrationObservation, error) {
	sourceID := source.Loop.Agent.BB.Thread
	strict := func(next doctorRun) doctorRun {
		return func(ctx context.Context, cli string, args []string, env map[string]string, dir string) ([]byte, error) {
			b, err := next(ctx, cli, args, env, dir)
			if err != nil {
				return nil, err
			}
			if len(args) > 1 && args[1] == "list" {
				var rows []struct {
					ID string `json:"id"`
				}
				if len(b) > inventoryListLimit || json.Unmarshal(b, &rows) != nil || rows == nil || len(rows) >= inventoryMaxLanes {
					return nil, errReconcileSnapshot
				}
				seen := map[string]bool{}
				for _, r := range rows {
					if seen[r.ID] {
						return nil, errReconcileSnapshot
					}
					seen[r.ID] = true
				}
			} else if len(args) == 4 && args[2] != sourceID {
				var v struct {
					Thread struct {
						Parent string `json:"parentThreadId"`
					} `json:"thread"`
				}
				if json.Unmarshal(b, &v) != nil || v.Thread.Parent != sourceID {
					return nil, errReconcileSnapshot
				}
			}
			return b, nil
		}
	}
	var out, errs bytes.Buffer
	code := transferInventoryCommand(env{ctx: e.ctx, stdout: &out, stderr: &errs}, []string{"inventory", "-c", path, "--json"}, strict(run), strict(listRun))
	var inv transferInventory
	if code != exitOK || out.Len() > control.MigrationReviewLimit || json.Unmarshal(out.Bytes(), &inv) != nil || !inv.Complete || !inv.LanesListed || e.ctx.Err() != nil {
		return control.MigrationObservation{}, errReconcileSnapshot
	}
	successor := inventoryShow(e.ctx, dest.Loop.Agent.BB.CLI, dest.Loop.Agent.BB.Thread, 5*time.Second, run)
	if !successor.Available || successor.Archived || inv.Target.Thread != sourceID || inv.Target.Provider != source.Loop.Agent.Provider || successor.Provider != dest.Loop.Agent.Provider || inv.Target.Project != successor.Project || inv.Target.Host != successor.Host {
		return control.MigrationObservation{}, errReconcileSnapshot
	}
	o := control.MigrationObservation{Coverage: "direct_lanes_only", Source: reconcileLane(inv.Target), Successor: reconcileLane(successor), Lanes: []control.MigrationLane{}, Worktrees: []control.MigrationWorktree{}}
	for _, l := range inv.Lanes {
		if !l.Available || l.Project != inv.Target.Project || l.Host != inv.Target.Host {
			return o, errReconcileSnapshot
		}
		o.Lanes = append(o.Lanes, reconcileLane(l))
	}
	for _, repo := range inv.Repositories {
		if !repo.Available {
			return o, errReconcileSnapshot
		}
		for _, w := range repo.Worktrees {
			o.Worktrees = append(o.Worktrees, control.MigrationWorktree{Path: w.Path, Head: w.Head, Branch: w.Branch, Detached: w.Detached, Locked: w.Locked, Prunable: w.Prunable, Dirty: w.Dirty, Ahead: w.Ahead})
		}
	}
	sort.Slice(o.Lanes, func(i, j int) bool { return o.Lanes[i].Thread < o.Lanes[j].Thread })
	sort.Slice(o.Worktrees, func(i, j int) bool { return o.Worktrees[i].Path < o.Worktrees[j].Path })
	return o, nil
}
func reconcileAssignments(p control.MigrationPlan, o control.MigrationObservation, g *control.MigrationReview) error {
	rows := map[string]control.MigrationAssignment{}
	ids := map[string]bool{}
	for _, a := range p.Assignments {
		rows[a.LaneThread] = a
		ids[a.ID] = true
	}
	seen := map[string]bool{}
	for _, l := range o.Lanes {
		a, ok := rows[l.Thread]
		if !ok {
			return errors.New("each observed predecessor direct lane needs an explicit assignment row")
		}
		seen[l.Thread] = true
		if a.ExpectedWorktree != "" && filepath.Clean(a.ExpectedWorktree) != l.Worktree {
			return errors.New("declared worktree does not match observed lane; inspect privately")
		}
	}
	prior := map[string]bool{}
	if g != nil {
		for _, a := range g.Revisions[len(g.Revisions)-1].Plan.Assignments {
			prior[a.ID+"\x00"+a.LaneThread] = true
			if a.DeclaredState != "complete" && !ids[a.ID] {
				return errors.New("unfinished assignment missing; retain it until explicitly revised")
			}
		}
	}
	for _, a := range p.Assignments {
		if a.LaneThread != "" && !seen[a.LaneThread] && !prior[a.ID+"\x00"+a.LaneThread] {
			return errors.New("unobserved lane mapping; only existing missing-lane rows may be retained")
		}
	}
	return nil
}
func reconcileJSON(e env, v any) int {
	enc := json.NewEncoder(e.stdout)
	enc.SetIndent("", "  ")
	if enc.Encode(v) != nil {
		return exitFailed
	}
	return exitOK
}
func printReconcile(e env, s control.MigrationSummary, asJSON bool) int {
	if asJSON {
		return reconcileJSON(e, s)
	}
	fmt.Fprintf(e.stdout, "Migration review: %s revision %d; %d unfinished (%d dormant, %d unresolved), %d direct lanes; %d assignments absent from this listing.\n", s.ID, s.Revision, s.Unfinished, s.Dormant, s.Unresolved, s.DirectLanes, s.AbsentAssignments)
	fmt.Fprintf(e.stdout, "Evidence read %s; direct lanes only, descendants excluded. Declared assignment state is separate from observed activity. Review metadata does not establish migration execution or permission.\n", s.ObservedAt.Format(time.RFC3339))
	if s.Digest != "" {
		fmt.Fprintf(e.stdout, "Review digest: %s\n", s.Digest)
	}
	return exitOK
}

// Parsed in-memory configurations lack a source path; their runtime/loop/thread
// binding is still checked. Loaded configurations additionally bind the path.
func migrationReviewMatchesConfig(g *control.MigrationReview, cfg *config.Config) bool {
	if cfg.Loop.Agent.BB == nil || g.Successor.Loop != cfg.Loop.Name || g.Successor.Thread != cfg.Loop.Agent.BB.Thread {
		return false
	}
	dir, err := canonicalFuturePath(cfg.Loop.StateDir)
	if err != nil || dir != g.Successor.StateDir {
		return false
	}
	if cfg.Path() != "" {
		path, err := canonicalFuturePath(cfg.Path())
		if err != nil || path != g.Successor.Config {
			return false
		}
	}
	return true
}
