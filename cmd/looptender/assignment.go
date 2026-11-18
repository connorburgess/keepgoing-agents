package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
)

// assignmentBinding reads only local records. Failure never returns usable counts.
func assignmentBinding(cfg *config.Config, ctl control.File) (control.AssignmentBinding, error) {
	b := control.AssignmentBinding{Loop: cfg.Loop.Name}
	if ctl.PendingTransfer != nil || (ctl.BBTarget != "" && (cfg.Loop.BB == nil || cfg.Loop.BB.Thread != ctl.BBTarget)) {
		return b, control.ErrAssignment
	}
	if cfg.Loop.Adapter != "bb" || cfg.Loop.BB == nil || ctl.Ownership == nil || control.CheckOwner(ctl, "", false) != nil {
		return b, control.ErrAssignment
	}
	var err error
	b.Config, err = filepath.EvalSymlinks(cfg.Path())
	if err != nil {
		return b, control.ErrAssignment
	}
	b.Config, err = filepath.Abs(b.Config)
	if err != nil {
		return b, control.ErrAssignment
	}
	b.Runtime, err = filepath.EvalSymlinks(cfg.Loop.StateDir)
	if err != nil {
		return b, control.ErrAssignment
	}
	b.Runtime, err = filepath.Abs(b.Runtime)
	if err != nil {
		return b, control.ErrAssignment
	}
	g, err := control.LoadGoal(cfg.Loop.StateDir)
	if err != nil || g == nil || g.OwningLoop != cfg.Loop.Name {
		return b, control.ErrAssignment
	}
	b.Goal, b.GoalRevision = g.ID, g.Summary().Revision
	b.Coordinator, b.Owner, b.Generation = cfg.Loop.BB.Thread, ctl.Ownership.ThreadID, ctl.Ownership.Generation
	return b, nil
}
func assignmentStatus(cfg *config.Config, ctl control.File) control.AssignmentSummary {
	r, err := control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil {
		return control.AssignmentSummary{Availability: "unavailable"}
	}
	if r == nil {
		return control.AssignmentSummary{Availability: "not_recorded"}
	}
	b, err := assignmentBinding(cfg, ctl)
	if err != nil {
		return control.AssignmentSummary{Availability: "unavailable"}
	}
	return control.AssignmentSummaryOf(r, b)
}
func assignmentJSON(e env, v any) int {
	if json.NewEncoder(e.stdout).Encode(v) != nil {
		return exitFailed
	}
	return exitOK
}
func assignmentCommand(e env, args []string, run doctorRun) int {
	if len(args) == 0 {
		e.errf("usage: assignment inventory|review|handoff|acknowledge|show; handoff --draft validates a lane document without loop access")
		return exitUsage
	}
	cmd := args[0]
	switch cmd {
	case "inventory", "review", "handoff", "acknowledge", "show":
	default:
		return exitUsage
	}
	fs := flag.NewFlagSet("assignment "+cmd, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	path := fs.String("c", "", configFlagHelp("configuration path"))
	file := fs.String("file", "", "bounded private JSON declaration")
	id := fs.String("id", "", "expected assignment record identity")
	rev := fs.Uint64("revision", 0, "expected current record revision")
	private := fs.Bool("private", false, "show complete private record")
	draft := fs.Bool("draft", false, "validate and emit a lane handoff document; no runtime access")
	asJSON := fs.Bool("json", false, "machine readable counts or private inventory")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() != 0 || (*private && cmd != "show") || (*draft && cmd != "handoff") || ((cmd == "review" || cmd == "handoff" || cmd == "acknowledge") != (*file != "")) || ((cmd == "show" || cmd == "inventory" || *draft) && (*id != "" || *rev != 0)) || ((*id == "") != (*rev == 0)) {
		return exitUsage
	}
	var raw []byte
	if *file != "" {
		var err error
		raw, err = control.ReadMigrationFile(*file, control.AssignmentInputLimit)
		if err != nil {
			e.errf("assignment input must be a bounded regular private file without symlinks")
			return exitUsage
		}
	}
	if *draft {
		h, err := control.ParseAssignmentHandoff(raw)
		if err != nil {
			e.errf("%v", err)
			return exitUsage
		}
		return assignmentJSON(e, h)
	}
	if !configDefault(e, "assignment", fs, path) {
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("assignment configuration unavailable; inspect privately")
		return exitUsage
	}
	if *file != "" {
		inputPath, pathErr := canonicalFuturePath(*file)
		if pathErr != nil || inputPath == cfg.Path() || pathWithin(cfg.Loop.StateDir, inputPath) {
			e.errf("assignment input must be separate from configuration and runtime records")
			return exitUsage
		}
		for _, repo := range cfg.Loop.Progress.Repositories {
			if pathWithin(repo, inputPath) {
				e.errf("assignment input must be outside progress checkouts")
				return exitUsage
			}
		}
	}
	ctl, err := control.Load(cfg.Loop.StateDir)
	if err != nil {
		e.errf("%v", control.ErrAssignment)
		return exitFailed
	}
	if cmd == "show" {
		if *private {
			r, err := control.LoadAssignments(cfg.Loop.StateDir)
			if err != nil || r == nil {
				e.errf("%v", control.ErrAssignment)
				return exitFailed
			}
			return assignmentJSON(e, r)
		}
		s := assignmentStatus(cfg, ctl)
		if *asJSON {
			return assignmentJSON(e, s)
		}
		printAssignmentSummary(e, s)
		return exitOK
	}
	if !cfg.Loop.AssignmentLifecycle {
		e.errf("assignment_lifecycle is not enabled for this loop")
		return exitUsage
	}
	if err = control.CheckOwner(ctl, os.Getenv("BB_THREAD_ID"), false); err != nil {
		return ownershipError(e, err)
	}
	if err = control.CheckTransferReady(ctl); err != nil {
		return ownershipError(e, err)
	}
	if err := checkTransferSources(cfg, false); err != nil {
		return ownershipError(e, err)
	}
	b, err := assignmentBinding(cfg, ctl)
	if err != nil {
		e.errf("record a goal and establish BB ownership before assignment review")
		return exitFailed
	}
	var inv control.AssignmentInventory
	if cmd == "inventory" || cmd == "review" {
		inv = assignmentInventory(e.ctx, cfg.Loop.BB.CLI, b.Coordinator, run)
		if cmd == "inventory" {
			_ = assignmentJSON(e, inv)
			if inv.Coverage == "partial" {
				return exitFailed
			}
			return exitOK
		}
	}
	var specs []control.AssignmentSpec
	var h control.AssignmentHandoff
	var ack struct {
		Assignment      string `json:"assignment"`
		HandoffRevision uint64 `json:"handoffRevision"`
	}
	switch cmd {
	case "review":
		var p struct {
			Assignments []control.AssignmentSpec `json:"assignments"`
		}
		if decodeAssignmentInput(raw, &p) != nil || p.Assignments == nil {
			return exitUsage
		}
		specs = p.Assignments
	case "handoff":
		h, err = control.ParseAssignmentHandoff(raw)
		if err != nil {
			e.errf("%v", err)
			return exitUsage
		}
	case "acknowledge":
		if decodeAssignmentInput(raw, &ack) != nil || ack.Assignment == "" || ack.HandoffRevision == 0 {
			return exitUsage
		}
	}
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		return ownershipError(e, err)
	}
	defer l.Release()
	if err = checkTransferSources(cfg, false); err != nil {
		e.errf("configuration changed; no assignment evidence saved")
		return exitSkipped
	}
	// Re-read ownership and goal after inventory, under tick lock. A handoff or
	// acknowledgement cannot silently reconcile a changed binding.
	fresh, err := control.Load(cfg.Loop.StateDir)
	if err != nil {
		return exitFailed
	}
	freshBinding, err := assignmentBinding(cfg, fresh)
	if err != nil || freshBinding != b {
		e.errf("assignment binding changed; no record saved")
		return exitSkipped
	}
	saved, err := control.WriteAssignments(cfg.Loop.StateDir, os.Getenv("BB_THREAD_ID"), b, *id, *rev, func(next *control.AssignmentRevision) error {
		if cmd != "review" {
			prior, err := control.LoadAssignments(cfg.Loop.StateDir)
			if err != nil || prior == nil || prior.Revisions[len(prior.Revisions)-1].Binding != b {
				return control.ErrAssignment
			}
		}
		switch cmd {
		case "review":
			old := map[string]control.AssignmentRow{}
			for _, r := range next.Rows {
				old[r.Spec.ID] = r
			}
			prior, err := control.LoadAssignments(cfg.Loop.StateDir)
			if err != nil {
				return err
			}
			rebind := prior != nil && prior.Revisions[len(prior.Revisions)-1].Binding != b
			next.Rows = []control.AssignmentRow{}
			for _, s := range specs {
				row, exists := old[s.ID]
				if !exists {
					row = control.AssignmentRow{Spec: s, Revision: 1}
				} else if rebind || !reflect.DeepEqual(row.Spec, s) {
					row = control.AssignmentRow{Spec: s, Revision: row.Revision + 1}
				}
				next.Rows = append(next.Rows, row)
			}
			next.Inventory = inv
		case "handoff":
			found := false
			for i := range next.Rows {
				r := &next.Rows[i]
				if r.Spec.ID == h.Assignment {
					if r.Revision != h.AssignmentRevision || r.Spec.Lane != h.DeclaredBy {
						return control.ErrAssignment
					}
					r.Handoff = &h
					r.HandoffRevision++
					r.Acknowledged = 0
					found = true
				}
			}
			if !found {
				return control.ErrAssignment
			}
		case "acknowledge":
			found := false
			for i := range next.Rows {
				r := &next.Rows[i]
				if r.Spec.ID == ack.Assignment {
					if r.Handoff == nil || r.HandoffRevision != ack.HandoffRevision {
						return control.ErrAssignment
					}
					r.Acknowledged = ack.HandoffRevision
					found = true
				}
			}
			if !found {
				return control.ErrAssignment
			}
		}
		return nil
	})
	if err != nil {
		return ownershipError(e, err)
	}
	if *asJSON {
		return assignmentJSON(e, control.AssignmentSummaryOf(saved, b))
	}
	printAssignmentSummary(e, control.AssignmentSummaryOf(saved, b))
	fmt.Fprintln(e.stdout, "Explicit review evidence saved; no lane was dispatched or changed. Inspect assignment show --private for revision-bound handoff imports.")
	return exitOK
}

func decodeAssignmentInput(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return control.ErrAssignment
	}
	return nil
}
func printAssignmentSummary(e env, s control.AssignmentSummary) {
	fmt.Fprintf(e.stdout, "Assignments: %s; outcomes and availability are explicit declarations, separate from BB execution.\n", s.Availability)
	if s.Counts == nil {
		fmt.Fprintln(e.stdout, "Assignment outcomes unknown; reconcile existing direct lanes before relying on completion.")
		return
	}
	if s.Coverage != "observed_direct_children" {
		fmt.Fprintln(e.stdout, "Coverage is partial; additional assignments and work are unknown.")
	}
	c := s.Counts
	fmt.Fprintf(e.stdout, "Historical direct-child evidence (%s), read %s: %d assignments, %d unknown, %d unacknowledged, %d unfinished; %d person-blocked, %d dependency-waiting. No work dispatched.\n", s.Coverage, s.ObservedAt.Format(time.RFC3339), c.Assignments, c.Unknown, c.Unacknowledged, c.Unfinished, c.PersonBlocked, c.DependencyWaiting)
}

// assignmentInventory reads only list/show structured identity/status fields.
// Matching bracket reads detect some races; they do not make an atomic snapshot.
func assignmentInventory(ctx context.Context, cli, parent string, run doctorRun) control.AssignmentInventory {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	inv := control.AssignmentInventory{Coverage: "observed_direct_children", Lanes: []control.AssignmentLane{}}
	if !assignmentCoordinatorKnown(ctx, cli, parent, run) {
		inv.Coverage = "partial"
	}
	first, ok := inventoryChildren(ctx, cli, parent, 2*time.Second, run)
	sort.Strings(first)
	if !ok || len(first) >= inventoryMaxLanes {
		inv.Coverage = "partial"
	}
	for _, id := range first {
		lane := control.AssignmentLane{Thread: id, Execution: "unknown"}
		if ctx.Err() == nil {
			q, cancel := context.WithTimeout(ctx, 2*time.Second)
			raw, err := run(q, cli, []string{"thread", "show", id, "--json"}, nil, "")
			var v struct {
				OK     *bool           `json:"ok"`
				Error  json.RawMessage `json:"error"`
				Thread *struct {
					ID       string          `json:"id"`
					Parent   string          `json:"parentThreadId"`
					Status   string          `json:"status"`
					Archived json.RawMessage `json:"archivedAt"`
					Deleted  json.RawMessage `json:"deletedAt"`
				} `json:"thread"`
			}
			if err == nil && q.Err() == nil && len(raw) <= 64*1024 && json.Unmarshal(raw, &v) == nil && v.Thread != nil && (v.OK == nil || *v.OK) && (len(v.Error) == 0 || string(v.Error) == "null") {
				t := v.Thread
				if t.ID == id && t.Parent == parent && inventoryStatuses[t.Status] && (len(t.Deleted) == 0 || string(t.Deleted) == "null") {
					lane.Execution = t.Status
					lane.Archived = len(t.Archived) > 0 && string(t.Archived) != "null"
				}
			}
			cancel()
		}
		if lane.Execution == "unknown" {
			inv.Coverage = "partial"
		}
		inv.Lanes = append(inv.Lanes, lane)
	}
	last, ok := inventoryChildren(ctx, cli, parent, 2*time.Second, run)
	sort.Strings(last)
	if !ok || len(last) >= inventoryMaxLanes || !reflect.DeepEqual(first, last) || ctx.Err() != nil {
		inv.Coverage = "partial"
	}
	if !assignmentCoordinatorKnown(ctx, cli, parent, run) {
		inv.Coverage = "partial"
	}
	inv.ObservedAt = time.Now().UTC()
	return inv
}

func assignmentCoordinatorKnown(ctx context.Context, cli, parent string, run doctorRun) bool {
	q, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := run(q, cli, []string{"thread", "show", parent, "--json"}, nil, "")
	var v struct {
		OK     *bool           `json:"ok"`
		Error  json.RawMessage `json:"error"`
		Thread *struct {
			ID      string          `json:"id"`
			Status  string          `json:"status"`
			Deleted json.RawMessage `json:"deletedAt"`
		} `json:"thread"`
	}
	if err != nil || q.Err() != nil || len(raw) > 64*1024 || json.Unmarshal(raw, &v) != nil || v.Thread == nil || (v.OK != nil && !*v.OK) || (len(v.Error) > 0 && string(v.Error) != "null") {
		return false
	}
	return v.Thread.ID == parent && inventoryStatuses[v.Thread.Status] && (len(v.Thread.Deleted) == 0 || string(v.Thread.Deleted) == "null")
}
