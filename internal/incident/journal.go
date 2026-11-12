package incident

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

const FileName = "incidents.json"

// SchemaVersion is written on append. Version 5 adds the post-check target
// key, probe results, retarget records and limit-wait causes. Version 6 adds
// retention tiers and collapsed runs of identical checks.
const SchemaVersion = 6

// Retention is bounded per tier, so frequent routine checks cannot evict rare
// events. MaxRecords is the sum of the tier caps; MaxBytes bounds the file.
const (
	MaxKeyRecords      = 96
	MaxActivityRecords = 96
	MaxRoutineRecords  = 64
	MaxRecords         = MaxKeyRecords + MaxActivityRecords + MaxRoutineRecords
	MaxBytes           = 512 * 1024
)

// ErrUnavailable deliberately excludes paths, provider output and raw errors.
var ErrUnavailable = errors.New("incident history unavailable; check private state storage")

// Journal is the retained history. NextSequence-1 always equals the retained
// records plus Dropped (pruned by retention) plus Collapsed (folded into a
// later record's summary), so a missing sequence is always accounted for.
type Journal struct {
	SchemaVersion int      `json:"schemaVersion"`
	NextSequence  uint64   `json:"nextSequence"`
	Dropped       uint64   `json:"dropped"`
	Collapsed     uint64   `json:"collapsed"`
	Pruned        Pruned   `json:"pruned"`
	Records       []Record `json:"records"`
}

// Pruned says what retention removed from each tier since schema 6. Records
// dropped by older writers are counted in Dropped only, with no tier.
type Pruned struct {
	Key      TierPruned `json:"key"`
	Activity TierPruned `json:"activity"`
	Routine  TierPruned `json:"routine"`
}

// TierPruned counts a tier's pruned records and keeps the newest pruned
// record's local time: retained evidence for that tier is incomplete up to it.
type TierPruned struct {
	Count    uint64     `json:"count"`
	NewestAt *time.Time `json:"newestAt"`
}

func (p *Pruned) tier(name string) *TierPruned {
	switch name {
	case TierKey:
		return &p.Key
	case TierActivity:
		return &p.Activity
	}
	return &p.Routine
}

func tierCap(name string) int {
	switch name {
	case TierKey:
		return MaxKeyRecords
	case TierActivity:
		return MaxActivityRecords
	}
	return MaxRoutineRecords
}

// Load is read-only. Atomic replacement permits readers without a write lock.
// Corrupt, oversized, aliased or future-schema history is never silently reset.
func Load(dir string) (Journal, error) {
	path := filepath.Join(dir, FileName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Journal{SchemaVersion: SchemaVersion, NextSequence: 1, Records: []Record{}}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return Journal{}, ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return Journal{}, ErrUnavailable
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil || len(b) > MaxBytes {
		return Journal{}, ErrUnavailable
	}
	var j Journal
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&j) != nil || d.Decode(new(any)) != io.EOF || (j.SchemaVersion < 1 || j.SchemaVersion > SchemaVersion) || len(j.Records) > MaxRecords || j.NextSequence == 0 {
		return Journal{}, ErrUnavailable
	}
	if j.SchemaVersion < 6 && (j.Collapsed != 0 || j.Pruned != (Pruned{})) {
		return Journal{}, ErrUnavailable
	}
	// Every allocated sequence is retained, pruned or collapsed.
	if j.Dropped > j.NextSequence-1 || j.Collapsed > j.NextSequence-1-j.Dropped || j.NextSequence-1-j.Dropped-j.Collapsed != uint64(len(j.Records)) {
		return Journal{}, ErrUnavailable
	}
	if j.Pruned.Key.Count+j.Pruned.Activity.Count+j.Pruned.Routine.Count > j.Dropped {
		return Journal{}, ErrUnavailable
	}
	var previous, folded uint64
	for i, r := range j.Records {
		if r.CheckStartedAt.IsZero() || r.Sequence <= previous || r.Sequence >= j.NextSequence {
			return Journal{}, ErrUnavailable
		}
		// Before schema 6 nothing was collapsed, so sequences were contiguous.
		if j.SchemaVersion < 6 && (r.Sequence != j.Dropped+uint64(i)+1 || r.Collapsed != nil) {
			return Journal{}, ErrUnavailable
		}
		previous = r.Sequence
		if j.SchemaVersion == 1 {
			r.Observation.Source = ""
			r.Observation.Category = ""
			r.Observation.ProviderCode = "unavailable"
			r.Observation.HTTPStatus = nil
			r.Observation.ExitCode = nil
		}
		if j.SchemaVersion < 3 {
			r.Decisions = Decisions{}
		}
		if j.SchemaVersion < 4 {
			r.Kind, r.ControlChange = "tick", nil
		}
		if j.SchemaVersion < 5 {
			// Older writers recorded neither a post-check target nor probes.
			r.TargetKeyAfter, r.Probes = "", Probes{}
			if r.Kind == "retarget" {
				r.Kind = "unavailable"
			}
		}
		if !validRecord(r) {
			return Journal{}, ErrUnavailable
		}
		if r.Collapsed != nil {
			folded += r.Collapsed.Count
		}
		j.Records[i] = clean(r)
	}
	// Summaries of pruned records leave the journal total larger, never smaller.
	if folded > j.Collapsed {
		return Journal{}, ErrUnavailable
	}
	return j, nil
}

// Append takes only the journal lock, never the control or tick lock. Failure
// does not roll back actions/holds and must be surfaced by the caller.
func Append(dir string, r Record) error {
	l, err := lock.AcquireNamed(dir, "incident")
	if err != nil {
		return ErrUnavailable
	}
	defer l.Release()
	j, err := Load(dir)
	if err != nil {
		return err
	}
	r.Collapsed = nil // Only the journal folds records.
	if !validRecord(r) || j.NextSequence == ^uint64(0) {
		return ErrUnavailable
	}
	if j.SchemaVersion < SchemaVersion {
		j = upgrade(j)
	}
	r.Sequence = j.NextSequence
	j.NextSequence++
	j.add(clean(r))
	b, err := json.Marshal(j)
	if err != nil {
		return ErrUnavailable
	}
	for len(b)+1 > MaxBytes && len(j.Records) > 1 {
		j.dropForBytes()
		b, err = json.Marshal(j)
		if err != nil {
			return ErrUnavailable
		}
	}
	if len(b)+1 > MaxBytes {
		return ErrUnavailable
	}
	f, err := os.CreateTemp(dir, ".incidents-*")
	if err != nil {
		return ErrUnavailable
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return ErrUnavailable
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return ErrUnavailable
	}
	if err = f.Close(); err != nil {
		return ErrUnavailable
	}
	if err = os.Rename(name, filepath.Join(dir, FileName)); err != nil {
		return ErrUnavailable
	}
	return nil
}

// upgrade replays a legacy journal through the current retention rules, so its
// repeated checks are collapsed instead of pruned in bulk. Sequence numbers
// and the legacy dropped count are preserved.
func upgrade(old Journal) Journal {
	j := Journal{SchemaVersion: SchemaVersion, NextSequence: old.NextSequence, Dropped: old.Dropped, Records: make([]Record, 0, len(old.Records))}
	for _, r := range old.Records {
		j.add(r)
	}
	return j
}

// add appends a cleaned record with its sequence already assigned. When the
// two newest records and this one are the same run of repeatable checks, the
// middle one is folded into this record's summary: a run keeps its first and
// its latest check, plus a count and time span for everything between.
func (j *Journal) add(r Record) {
	n := len(j.Records)
	if n >= 2 && sameRun(j.Records[n-1], r) && sameRun(j.Records[n-2], j.Records[n-1]) {
		last := j.Records[n-1]
		c := Collapsed{Count: 1, FirstAt: last.CheckStartedAt, LastAt: last.CheckStartedAt, PassedProbes: len(last.Probes.Results)}
		if last.Collapsed != nil {
			c.Count += last.Collapsed.Count
			c.FirstAt = last.Collapsed.FirstAt
			c.PassedProbes += last.Collapsed.PassedProbes
		}
		r.Collapsed = &c
		j.Records[n-1] = r
		j.Collapsed++
		return
	}
	j.Records = append(j.Records, r)
	counts := map[string]int{}
	for _, rec := range j.Records {
		counts[rec.Tier]++
	}
	for _, tier := range []string{TierRoutine, TierActivity, TierKey} {
		for counts[tier] > tierCap(tier) {
			j.dropOldest(tier)
			counts[tier]--
		}
	}
}

// dropOldest prunes the oldest record of one tier and reports whether it found one.
func (j *Journal) dropOldest(tier string) bool {
	for i, r := range j.Records {
		if r.Tier != tier {
			continue
		}
		p := j.Pruned.tier(tier)
		p.Count++
		if p.NewestAt == nil || r.CheckStartedAt.After(*p.NewestAt) {
			at := r.CheckStartedAt
			p.NewestAt = &at
		}
		j.Dropped++
		j.Records = append(j.Records[:i], j.Records[i+1:]...)
		return true
	}
	return false
}

// dropForBytes gives up routine history first and key events last. The
// newest record always survives.
func (j *Journal) dropForBytes() {
	newest := j.Records[len(j.Records)-1]
	j.Records = j.Records[:len(j.Records)-1]
	for _, tier := range []string{TierRoutine, TierActivity, TierKey} {
		if j.dropOldest(tier) {
			break
		}
	}
	j.Records = append(j.Records, newest)
}

func validRecord(r Record) bool {
	if r.CheckStartedAt.IsZero() {
		return false
	}
	if c := r.Collapsed; c != nil && (r.Kind != "tick" || c.Count == 0 || c.FirstAt.IsZero() || c.LastAt.IsZero() || c.PassedProbes < 0) {
		return false
	}
	// A retarget is a move: both hashed targets are known and they differ.
	if r.Kind == "retarget" && (!keyPattern.MatchString(r.TargetKey) || !keyPattern.MatchString(r.TargetKeyAfter) || r.TargetKey == r.TargetKeyAfter) {
		return false
	}
	return r.Kind != "control" || (r.ControlChange != nil && !r.ControlChange.SaveObservedAt.IsZero() && r.ControlBefore != nil && r.ControlAfter != nil)
}
