package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/state"
)

const completionInputLimit = 4096

var errCompletion = errors.New("completion review unavailable or changed; review the current goal and assignments, then complete --review-file with an explicit whole-objective declaration (pause/stop remain available)")

type completionDeclaration struct {
	WholeObjectiveComplete bool   `json:"wholeObjectiveComplete"`
	Goal                   string `json:"goal"`
	GoalRevision           uint64 `json:"goalRevision"`
	Assignments            string `json:"assignments"`
	AssignmentRevision     uint64 `json:"assignmentRevision"`
}
type completionKey struct{}
type completionSnapshot struct {
	declaration completionDeclaration
	binding     control.AssignmentBinding
	record      *control.AssignmentRecord
}

// This fixed flat input rejects duplicate keys as well as unknown/null/missing
// fields. Private goal and assignment refs are never read or echoed.
func parseCompletion(raw []byte) (completionDeclaration, error) {
	var out completionDeclaration
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return out, errCompletion
	}
	seen := map[string]bool{}
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			return out, errCompletion
		}
		key, ok := tok.(string)
		if !ok || seen[key] {
			return out, errCompletion
		}
		seen[key] = true
		switch key {
		case "wholeObjectiveComplete", "goal", "goalRevision", "assignments", "assignmentRevision":
		default:
			return out, errCompletion
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return out, errCompletion
		}
	}
	if _, err = d.Token(); err != nil {
		return out, errCompletion
	}
	if _, err = d.Token(); err != io.EOF {
		return out, errCompletion
	}
	if len(raw) > completionInputLimit || len(seen) != 5 || json.Unmarshal(raw, &out) != nil || !out.WholeObjectiveComplete || out.Goal == "" || out.GoalRevision == 0 || out.Assignments == "" || out.AssignmentRevision == 0 {
		return out, errCompletion
	}
	return out, nil
}
func completionCommand(e env, cfg *config.Config, note, file string) int {
	if !cfg.Loop.AssignmentCompletion {
		if file != "" {
			e.errf("--review-file requires loop.assignment_completion")
			return exitUsage
		}
		return controlCmd(e, cfg, "complete", note)
	}
	raw, err := control.ReadMigrationFile(file, completionInputLimit)
	if err != nil {
		e.errf("%v", errCompletion)
		return exitUsage
	}
	// Never let an input be a runtime/config/progress record.
	path, err := canonicalFuturePath(file)
	if err != nil || path == cfg.Path() || pathWithin(cfg.Loop.StateDir, path) {
		e.errf("%v", errCompletion)
		return exitUsage
	}
	for _, repo := range cfg.Loop.Progress.Repositories {
		if pathWithin(repo, path) {
			e.errf("%v", errCompletion)
			return exitUsage
		}
	}
	declaration, err := parseCompletion(raw)
	if err != nil {
		e.errf("%v", errCompletion)
		return exitUsage
	}
	e.ctx = context.WithValue(e.ctx, completionKey{}, declaration)
	return controlCmd(e, cfg, "complete", note)
}

// Missing diagnostic state is the documented initial primary. An existing
// record must be a bounded regular JSON object; null/damage is not primary.
func completionPrimary(dir string) bool {
	path := filepath.Join(dir, state.FileName)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return true
	}
	raw, err := control.ReadMigrationFile(path, control.AssignmentLimit)
	if err != nil {
		return false
	}
	raw = bytes.TrimSpace(raw)
	var st state.File
	return len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &st) == nil && st.Target() == state.Primary
}
func completionBinding(cfg *config.Config, c control.File, d completionDeclaration) (control.AssignmentBinding, error) {
	if !completionPrimary(cfg.Loop.StateDir) {
		return control.AssignmentBinding{}, errCompletion
	}
	if control.CheckTransferReady(c) != nil || control.CheckOwner(c, os.Getenv("BB_THREAD_ID"), false) != nil {
		return control.AssignmentBinding{}, errCompletion
	}
	// Completion is not a substitute for a forbidden self-resume. It clears
	// AwaitingUser in the existing control action, so enforce the same boundary.
	if c.AwaitingUser && checkHoldClearer(cfg, os.Getenv("BB_THREAD_ID")) != nil {
		return control.AssignmentBinding{}, errCompletion
	}
	b, err := assignmentBinding(cfg, c)
	if err != nil || b.Goal != d.Goal || b.GoalRevision != d.GoalRevision {
		return b, errCompletion
	}
	return b, nil
}

// Called under tick.lock before taking control.lock. No model/turn/probe or
// transcript queries; the complete structured metadata read has a 20s ceiling.
func prepareCompletion(e env, cfg *config.Config) (*completionSnapshot, error) {
	return prepareCompletionWith(e, cfg, doctorExecLimit(inventoryListLimit))
}
func prepareCompletionWith(e env, cfg *config.Config, run doctorRun) (*completionSnapshot, error) {
	d, ok := e.ctx.Value(completionKey{}).(completionDeclaration)
	if !ok {
		return nil, errCompletion
	}
	c, err := control.Load(cfg.Loop.StateDir)
	if err != nil {
		return nil, errCompletion
	}
	b, err := completionBinding(cfg, c, d)
	if err != nil {
		return nil, errCompletion
	}
	r, err := control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil || control.CheckAssignmentCompletion(r, b, d.Assignments, d.AssignmentRevision) != nil {
		return nil, errCompletion
	}
	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	inv := assignmentInventory(ctx, cfg.Loop.BB.CLI, b.Coordinator, run)
	reviewed := r.Revisions[len(r.Revisions)-1].Inventory
	if inv.Coverage != "observed_direct_children" || !reflect.DeepEqual(inv.Lanes, reviewed.Lanes) {
		return nil, errCompletion
	}
	for _, lane := range inv.Lanes {
		if lane.Execution != "idle" || !completionInteractions(ctx, cfg.Loop.BB.CLI, lane.Thread, run) {
			return nil, errCompletion
		}
	}
	// Detect child-set/status changes during interaction checks without writing
	// a new review or dropping absent historical completed rows.
	last := assignmentInventory(ctx, cfg.Loop.BB.CLI, b.Coordinator, run)
	if ctx.Err() != nil || last.Coverage != "observed_direct_children" || !reflect.DeepEqual(last.Lanes, inv.Lanes) {
		return nil, errCompletion
	}
	return &completionSnapshot{d, b, r}, nil
}
func completionInteractions(ctx context.Context, cli, id string, run doctorRun) bool {
	q, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := run(q, cli, []string{"thread", "interactions", "list", id, "--json"}, nil, "")
	var rows []struct {
		Thread string `json:"threadId"`
		Status string `json:"status"`
	}
	if err != nil || q.Err() != nil || len(raw) > 64*1024 || json.Unmarshal(raw, &rows) != nil || rows == nil || len(rows) >= 500 {
		return false
	}
	for _, row := range rows {
		if row.Thread != id || (row.Status != "resolved" && row.Status != "interrupted") {
			return false
		}
	}
	return true
}

// Called inside the existing control mutation lock after the external scan.
func (s *completionSnapshot) check(cfg *config.Config, c control.File) error {
	if s == nil || checkTransferSources(cfg, false) != nil {
		return errCompletion
	}
	b, err := completionBinding(cfg, c, s.declaration)
	r, loadErr := control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil || loadErr != nil || b != s.binding || !reflect.DeepEqual(r, s.record) || control.CheckAssignmentCompletion(r, b, s.declaration.Assignments, s.declaration.AssignmentRevision) != nil {
		return errCompletion
	}
	return nil
}
