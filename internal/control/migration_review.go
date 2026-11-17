package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

const MigrationReviewName = "migration-review.json"
const MigrationReviewLimit = 1 << 20
const MigrationPlanLimit = 100 << 10
const migrationRevisionLimit = 32

var ErrMigrationReview = errors.New("migration review unavailable or invalid; inspect the private record before retrying")
var assignmentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var migrationID = regexp.MustCompile(`^review_[0-9a-f]{32}$`)

type MigrationAssignment struct {
	ID               string `json:"id"`
	Summary          string `json:"summary"`
	DeclaredState    string `json:"declaredState"`
	LaneThread       string `json:"laneThread,omitempty"`
	ExpectedWorktree string `json:"expectedWorktree,omitempty"`
}
type MigrationPlan struct {
	Assignments []MigrationAssignment `json:"assignments"`
}
type MigrationIdentity struct {
	Loop     string `json:"loop"`
	Thread   string `json:"thread"`
	Config   string `json:"config"`
	StateDir string `json:"stateDir"`
}
type MigrationLane struct {
	Thread      string `json:"thread"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
	Archived    bool   `json:"archived"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Host        string `json:"host"`
	Path        string `json:"path"`
	Worktree    string `json:"worktree,omitempty"`
}
type MigrationWorktree struct {
	Path     string `json:"path"`
	Head     string `json:"head"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
	Dirty    *int   `json:"dirty,omitempty"`
	Ahead    *int   `json:"ahead,omitempty"`
}
type MigrationObservation struct {
	ObservedAt time.Time           `json:"observedAt"`
	Coverage   string              `json:"coverage"`
	Source     MigrationLane       `json:"source"`
	Successor  MigrationLane       `json:"successor"`
	Lanes      []MigrationLane     `json:"lanes"`
	Worktrees  []MigrationWorktree `json:"worktrees"`
}
type MigrationRevision struct {
	Revision    uint64               `json:"revision"`
	RecordedAt  time.Time            `json:"recordedAt"`
	Digest      string               `json:"digest"`
	Plan        MigrationPlan        `json:"plan"`
	Observation MigrationObservation `json:"observation"`
}

// MigrationReview is an explicit descriptive checklist, never supervisor authority
// or proof of migration execution. Only its dedicated command reads its content.
type MigrationReview struct {
	Version   int                 `json:"version"`
	ID        string              `json:"id"`
	Source    MigrationIdentity   `json:"source"`
	Successor MigrationIdentity   `json:"successor"`
	Revisions []MigrationRevision `json:"revisions"`
}
type MigrationSummary struct {
	ID                string    `json:"id,omitempty"`
	Revision          uint64    `json:"revision"`
	Outcome           string    `json:"outcome"`
	Coverage          string    `json:"coverage"`
	Digest            string    `json:"digest,omitempty"`
	ObservedAt        time.Time `json:"observedAt"`
	Assignments       int       `json:"assignments"`
	Unfinished        int       `json:"unfinished"`
	Dormant           int       `json:"dormant"`
	Unresolved        int       `json:"unresolved"`
	DirectLanes       int       `json:"directLanes"`
	AbsentAssignments int       `json:"absentAssignments"`
}

func MigrationSummaryOf(id string, r MigrationRevision) MigrationSummary {
	s := MigrationSummary{ID: id, Revision: r.Revision, Outcome: "review_recorded_not_execution", Coverage: r.Observation.Coverage, Digest: r.Digest, ObservedAt: r.Observation.ObservedAt, Assignments: len(r.Plan.Assignments), DirectLanes: len(r.Observation.Lanes)}
	present := map[string]bool{}
	for _, l := range r.Observation.Lanes {
		present[l.Thread] = true
	}
	for _, a := range r.Plan.Assignments {
		if a.DeclaredState != "complete" {
			s.Unfinished++
		}
		if a.DeclaredState == "dormant" {
			s.Dormant++
		}
		if a.DeclaredState == "unresolved" {
			s.Unresolved++
		}
		if a.LaneThread != "" && !present[a.LaneThread] {
			s.AbsentAssignments++
		}
	}
	return s
}
func validMigrationPlan(p MigrationPlan) bool {
	if p.Assignments == nil || len(p.Assignments) > 200 {
		return false
	}
	ids := map[string]bool{}
	lanes := map[string]bool{}
	for _, a := range p.Assignments {
		if !assignmentID.MatchString(a.ID) || ids[a.ID] || strings.TrimSpace(a.Summary) == "" || len(a.Summary) > 1024 {
			return false
		}
		ids[a.ID] = true
		switch a.DeclaredState {
		case "active", "dormant", "unresolved", "complete":
		default:
			return false
		}
		if a.LaneThread != "" {
			if !ownerID.MatchString(a.LaneThread) || lanes[a.LaneThread] {
				return false
			}
			lanes[a.LaneThread] = true
		}
		if a.ExpectedWorktree != "" && (!filepath.IsAbs(a.ExpectedWorktree) || len(a.ExpectedWorktree) > 4096 || a.LaneThread == "") {
			return false
		}
	}
	return true
}
func ParseMigrationPlan(b []byte) (MigrationPlan, error) {
	var p MigrationPlan
	if len(b) > MigrationPlanLimit || decodeGoalJSON(b, &p) != nil || !validMigrationPlan(p) {
		return p, errors.New("invalid migration assignments; use a bounded private JSON plan")
	}
	return p, nil
}

// ReadMigrationFile rejects symlinks/nonregular files and verifies the opened
// handle against the initial observation. Diagnostics never include input text.
func ReadMigrationFile(path string, limit int) ([]byte, error) {
	info, err := snapshot.LstatRecord(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, ErrMigrationReview
	}
	f, err := snapshot.OpenRecord(path)
	if err != nil {
		return nil, ErrMigrationReview
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, ErrMigrationReview
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(b) > limit {
		return nil, ErrMigrationReview
	}
	return b, nil
}
func validMigrationIdentity(v MigrationIdentity) bool {
	return strings.TrimSpace(v.Loop) != "" && len(v.Loop) <= 256 && ownerID.MatchString(v.Thread) && filepath.IsAbs(v.Config) && filepath.IsAbs(v.StateDir) && len(v.Config) <= 4096 && len(v.StateDir) <= 4096
}
func validMigrationLane(l MigrationLane) bool {
	return ownerID.MatchString(l.Thread) && len(l.Provider) > 0 && len(l.Provider) <= 128 && len(l.Project) > 0 && len(l.Project) <= 128 && len(l.Environment) > 0 && len(l.Environment) <= 128 && len(l.Host) > 0 && len(l.Host) <= 128 && filepath.IsAbs(l.Path) && len(l.Path) <= 4096 && len(l.Worktree) <= 4096 && (l.Worktree == "" || filepath.IsAbs(l.Worktree)) && (l.Status == "idle" || l.Status == "active" || l.Status == "pending" || l.Status == "starting" || l.Status == "stopping" || l.Status == "error")
}
func validMigrationRevision(r MigrationRevision) bool {
	o := r.Observation
	if r.RecordedAt.IsZero() || !digest.MatchString(r.Digest) || !validMigrationPlan(r.Plan) || o.ObservedAt.IsZero() || o.Coverage != "direct_lanes_only" || !validMigrationLane(o.Source) || !validMigrationLane(o.Successor) || len(o.Lanes) >= 200 || len(o.Worktrees) > 500 {
		return false
	}
	seen := map[string]bool{}
	for _, l := range o.Lanes {
		if !validMigrationLane(l) || seen[l.Thread] || l.Thread == o.Source.Thread || l.Thread == o.Successor.Thread {
			return false
		}
		seen[l.Thread] = true
	}
	for _, w := range o.Worktrees {
		if !filepath.IsAbs(w.Path) || len(w.Path) > 4096 || len(w.Head) > 64 || len(w.Branch) > 512 {
			return false
		}
	}
	return true
}
func LoadMigrationReview(dir string) (*MigrationReview, error) {
	path := filepath.Join(dir, MigrationReviewName)
	if _, err := snapshot.LstatRecord(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	b, err := ReadMigrationFile(path, MigrationReviewLimit)
	if err != nil {
		return nil, err
	}
	var g MigrationReview
	if decodeGoalJSON(b, &g) != nil || g.Version != 1 || !migrationID.MatchString(g.ID) || !validMigrationIdentity(g.Source) || !validMigrationIdentity(g.Successor) || g.Source.StateDir == g.Successor.StateDir || len(g.Revisions) == 0 || len(g.Revisions) > migrationRevisionLimit {
		return nil, ErrMigrationReview
	}
	for i, r := range g.Revisions {
		if r.Revision != uint64(i+1) || !validMigrationRevision(r) || r.Observation.Source.Thread != g.Source.Thread || r.Observation.Successor.Thread != g.Successor.Thread {
			return nil, ErrMigrationReview
		}
	}
	return &g, nil
}

// WriteMigrationReview requires both loop tick locks. The short control lock
// checks the destination's existing ownership. Neither controls nor other loop
// records are written. verify rechecks captured local evidence before publication.
func WriteMigrationReview(dir, caller string, source, successor MigrationIdentity, r MigrationRevision, id string, revision uint64, verify func() error) (*MigrationReview, error) {
	if !validMigrationIdentity(source) || !validMigrationIdentity(successor) || source.StateDir == successor.StateDir || dir != successor.StateDir || !validMigrationRevision(r) || r.Observation.Source.Thread != source.Thread || r.Observation.Successor.Thread != successor.Thread {
		return nil, ErrMigrationReview
	}
	l, err := lock.AcquireNamed(dir, lock.Control)
	if err != nil {
		return nil, err
	}
	defer l.Release()
	if err := CheckControlPath(dir); err != nil {
		return nil, err
	}
	ctl, err := Load(dir)
	if err != nil {
		return nil, ErrMigrationReview
	}
	if err := CheckTransferReady(ctl); err != nil {
		return nil, err
	}
	if err := CheckOwner(ctl, caller, false); err != nil {
		return nil, err
	}
	g, err := LoadMigrationReview(dir)
	if err != nil {
		return nil, err
	}
	if id == "" {
		if g != nil || revision != 0 {
			return nil, errors.New("migration review exists; inspect its identity and revision")
		}
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, ErrMigrationReview
		}
		g = &MigrationReview{Version: 1, ID: "review_" + hex.EncodeToString(b[:]), Source: source, Successor: successor}
	} else if g == nil || g.ID != id || uint64(len(g.Revisions)) != revision {
		return nil, errors.New("migration review identity or revision changed; preview again")
	}
	if g.Source != source || g.Successor != successor {
		return nil, errors.New("migration review belongs to different source or successor")
	}
	if len(g.Revisions) >= migrationRevisionLimit {
		return nil, errors.New("migration review history full; no history discarded")
	}
	if len(g.Revisions) > 0 {
		current := map[string]bool{}
		for _, a := range r.Plan.Assignments {
			current[a.ID] = true
		}
		for _, a := range g.Revisions[len(g.Revisions)-1].Plan.Assignments {
			if a.DeclaredState != "complete" && !current[a.ID] {
				return nil, errors.New("unfinished assignment missing; retain it and explicitly revise its declared state")
			}
		}
	}
	if verify == nil {
		return nil, ErrMigrationReview
	}
	if err := verify(); err != nil {
		return nil, err
	}
	r.Revision = uint64(len(g.Revisions) + 1)
	g.Revisions = append(g.Revisions, r)
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil || len(b)+1 > MigrationReviewLimit {
		return nil, errors.New("migration review history exceeds size limit; no history discarded")
	}
	if err := WritePrivateFile(filepath.Join(dir, MigrationReviewName), append(b, '\n')); err != nil {
		return nil, errors.New("could not save migration review; inspect saved record before retrying")
	}
	return g, nil
}
