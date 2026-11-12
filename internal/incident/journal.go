package incident

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

const FileName = "incidents.json"

// SchemaVersion is written on append. Version 5 adds the post-check target
// key, probe results, retarget records and limit-wait causes.
const SchemaVersion = 5
const MaxRecords = 256
const MaxBytes = 512 * 1024

// ErrUnavailable deliberately excludes paths, provider output and raw errors.
var ErrUnavailable = errors.New("incident history unavailable; check private state storage")

type Journal struct {
	SchemaVersion int      `json:"schemaVersion"`
	NextSequence  uint64   `json:"nextSequence"`
	Dropped       uint64   `json:"dropped"`
	Records       []Record `json:"records"`
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
	if j.Dropped > j.NextSequence-1 || j.NextSequence-1-j.Dropped != uint64(len(j.Records)) {
		return Journal{}, ErrUnavailable
	}
	for i, r := range j.Records {
		if r.Sequence != j.Dropped+uint64(i)+1 || r.CheckStartedAt.IsZero() {
			return Journal{}, ErrUnavailable
		}
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
		j.Records[i] = clean(r)
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
	if !validRecord(r) || j.NextSequence == ^uint64(0) {
		return ErrUnavailable
	}
	j.SchemaVersion = SchemaVersion
	r = clean(r)
	r.Sequence = j.NextSequence
	j.NextSequence++
	j.Records = append(j.Records, r)
	for len(j.Records) > MaxRecords {
		j.Records = j.Records[1:]
		j.Dropped++
	}
	b, err := json.Marshal(j)
	if err != nil {
		return ErrUnavailable
	}
	for len(b)+1 > MaxBytes && len(j.Records) > 1 {
		j.Records = j.Records[1:]
		j.Dropped++
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

func validRecord(r Record) bool {
	if r.CheckStartedAt.IsZero() {
		return false
	}
	// A retarget is a move: both hashed targets are known and they differ.
	if r.Kind == "retarget" && (!keyPattern.MatchString(r.TargetKey) || !keyPattern.MatchString(r.TargetKeyAfter) || r.TargetKey == r.TargetKeyAfter) {
		return false
	}
	return r.Kind != "control" || (r.ControlChange != nil && !r.ControlChange.SaveObservedAt.IsZero() && r.ControlBefore != nil && r.ControlAfter != nil)
}
