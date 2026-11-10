package control

import (
	"bytes"
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

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

const GoalName = "goal.json"
const maxGoalBytes = 1 << 20
const maxGoalRevisions = 128

var ErrGoal = errors.New("goal record unavailable or invalid; restore a valid goal.json before editing")
var goalID = regexp.MustCompile(`^goal_[0-9a-f]{32}$`)

// GoalSpec is descriptive private metadata. References are opaque: never read,
// executed, sent to providers, or treated as evidence of authorization/success.
type GoalSpec struct {
	Title             string          `json:"title"`
	ObjectiveRefs     []string        `json:"objectiveRefs"`
	AcceptanceRefs    []string        `json:"acceptanceRefs"`
	ScopeRefs         []string        `json:"scopeRefs,omitempty"`
	AuthorizationRefs []string        `json:"authorizationRefs,omitempty"`
	NextAction        *GoalNextAction `json:"nextAction,omitempty"`
	EvidenceRefs      []string        `json:"evidenceRefs,omitempty"`
}

type GoalNextAction struct {
	Summary string   `json:"summary"`
	Refs    []string `json:"refs,omitempty"`
}

type GoalRevision struct {
	Revision   uint64    `json:"revision"`
	RecordedAt time.Time `json:"recordedAt"`
	Spec       GoalSpec  `json:"spec"`
}

// Goal retains every explicit specification revision in one atomic record.
// Recorded is metadata state, independent of loop controls and task completion.
type Goal struct {
	Version    int            `json:"version"`
	ID         string         `json:"id"`
	OwningLoop string         `json:"owningLoop"`
	State      string         `json:"state"`
	Revisions  []GoalRevision `json:"revisions"`
}

// GoalSummary deliberately excludes all caller-supplied task text and references.
type GoalSummary struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	State    string `json:"state"`
}

func (g *Goal) Summary() GoalSummary {
	return GoalSummary{ID: g.ID, Revision: g.Revisions[len(g.Revisions)-1].Revision, State: g.State}
}

func decodeGoalJSON(b []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return ErrGoal
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ErrGoal
	}
	return nil
}

// ParseGoalSpec bounds input and never returns decoder text containing private input.
func ParseGoalSpec(b []byte) (GoalSpec, error) {
	var spec GoalSpec
	if len(b) > 32768 || decodeGoalJSON(b, &spec) != nil || !validGoalSpec(spec) {
		return spec, errors.New("invalid goal input: expected a JSON object with title, objectiveRefs and acceptanceRefs (maximum 32 KiB)")
	}
	return spec, nil
}

func validGoalSpec(s GoalSpec) bool {
	if strings.TrimSpace(s.Title) == "" || len(s.Title) > 256 || len(s.ObjectiveRefs) == 0 || len(s.AcceptanceRefs) == 0 {
		return false
	}
	lists := [][]string{s.ObjectiveRefs, s.AcceptanceRefs, s.ScopeRefs, s.AuthorizationRefs, s.EvidenceRefs}
	if s.NextAction != nil {
		if strings.TrimSpace(s.NextAction.Summary) == "" || len(s.NextAction.Summary) > 1024 {
			return false
		}
		lists = append(lists, s.NextAction.Refs)
	}
	for _, refs := range lists {
		if len(refs) > 32 {
			return false
		}
		for _, ref := range refs {
			if strings.TrimSpace(ref) == "" || len(ref) > 2048 {
				return false
			}
		}
	}
	return true
}

// LoadGoal is bounded and read-only. Missing metadata is compatible with old loops.
func LoadGoal(dir string) (*Goal, error) {
	path := filepath.Join(dir, GoalName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxGoalBytes {
		return nil, ErrGoal
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrGoal
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxGoalBytes+1))
	var g Goal
	if err != nil || len(b) > maxGoalBytes || decodeGoalJSON(b, &g) != nil || g.Version != 1 || !goalID.MatchString(g.ID) || strings.TrimSpace(g.OwningLoop) == "" || g.State != "recorded" || len(g.Revisions) == 0 || len(g.Revisions) > maxGoalRevisions {
		return nil, ErrGoal
	}
	for i, r := range g.Revisions {
		if r.Revision != uint64(i+1) || r.RecordedAt.IsZero() || !validGoalSpec(r.Spec) {
			return nil, ErrGoal
		}
	}
	return &g, nil
}

// WriteGoal requires the caller to hold tick.lock and verify config snapshots.
// The control lock serializes ownership validation with claims and holds. Controls
// are never saved here, so metadata changes cannot clear or enable supervision.
// Empty expectedID means create; revisions require both identity and generation.
func WriteGoal(dir, loop, caller string, spec GoalSpec, expectedID string, expectedRevision uint64) (*Goal, error) {
	if !validGoalSpec(spec) || strings.TrimSpace(loop) == "" {
		return nil, errors.New("invalid goal specification or owning loop")
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
		return nil, errors.New("cannot verify goal ownership: control record unavailable")
	}
	if err := CheckOwner(ctl, caller, false); err != nil {
		return nil, err
	}
	g, err := LoadGoal(dir)
	if err != nil {
		return nil, err
	}
	if expectedID == "" {
		if g != nil || expectedRevision != 0 {
			return nil, errors.New("goal already exists or invalid create revision; inspect goal show before revising")
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, errors.New("could not allocate goal identity")
		}
		g = &Goal{Version: 1, ID: "goal_" + hex.EncodeToString(id[:]), OwningLoop: loop, State: "recorded"}
	} else if g == nil || g.ID != expectedID || g.Summary().Revision != expectedRevision {
		return nil, errors.New("goal identity or revision changed; inspect goal show before retrying")
	}
	if g.OwningLoop != loop {
		return nil, errors.New("goal belongs to a different loop name; restore the owning configuration")
	}
	if len(g.Revisions) >= maxGoalRevisions {
		return nil, errors.New("goal revision history is full; no history was discarded")
	}
	g.Revisions = append(g.Revisions, GoalRevision{Revision: uint64(len(g.Revisions) + 1), RecordedAt: time.Now().UTC(), Spec: spec})
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil || len(b)+1 > maxGoalBytes {
		return nil, errors.New("goal history exceeds maximum size; no history was discarded")
	}
	if err := writeAtomic(filepath.Join(dir, GoalName), append(b, '\n'), 0600); err != nil {
		return nil, errors.New("could not save goal record")
	}
	return g, nil
}
