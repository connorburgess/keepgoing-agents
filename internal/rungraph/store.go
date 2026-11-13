package rungraph

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// Availability says what a read found. Only Available has usable records.
type Availability string

const (
	Available   Availability = "available"
	Missing     Availability = "missing"     // no graph yet, including every loop started by an older build
	Unsupported Availability = "unsupported" // written by a newer schema
	Corrupt     Availability = "corrupt"     // unreadable, oversized, aliased or failing validation
)

// ErrUnavailable deliberately excludes paths and raw errors.
var ErrUnavailable = errors.New("run reference record unavailable; discovery evidence is incomplete")

const lockName = "graph"

// NewID returns prefix plus 32 random hex digits.
func NewID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrUnavailable
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// Load is read-only and takes no lock. It never creates, repairs or resets a
// record: anything but Available returns an empty graph.
func Load(dir string) (Graph, Availability) {
	path := filepath.Join(dir, FileName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Graph{}, Missing
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return Graph{}, Corrupt
	}
	f, err := control.OpenPrivateSnapshot(path)
	if errors.Is(err, os.ErrNotExist) {
		return Graph{}, Missing
	}
	if err != nil {
		return Graph{}, Corrupt
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil || len(b) > MaxBytes {
		return Graph{}, Corrupt
	}
	var version struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if json.Unmarshal(b, &version) != nil || version.SchemaVersion < 1 {
		return Graph{}, Corrupt
	}
	if version.SchemaVersion > SchemaVersion {
		return Graph{}, Unsupported
	}
	var g Graph
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&g) != nil || d.Decode(new(any)) != io.EOF || !g.valid() {
		return Graph{}, Corrupt
	}
	return g, Available
}

// Update serializes one read-modify-write under the graph lock, which is
// independent of the tick and control locks. The first update of a directory
// with no graph allocates the durable loop identity. fn reports whether it
// changed anything; an unchanged graph is not rewritten. An unsupported or
// corrupt record is left exactly as it is.
func Update(dir string, now time.Time, fn func(*Graph) (bool, error)) error {
	return updateGraph(dir, now, true, fn)
}

// UpdateExisting is Update for callers that must never create a runtime
// file: it does nothing unless the graph lock file and a readable graph both
// already exist. The graph is checked again under the lock, so a graph removed
// in between is not recreated.
func UpdateExisting(dir string, now time.Time, fn func(*Graph) (bool, error)) error {
	if info, err := os.Lstat(filepath.Join(dir, lockName+".lock")); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	return updateGraph(dir, now, false, fn)
}

func updateGraph(dir string, now time.Time, create bool, fn func(*Graph) (bool, error)) error {
	var l *lock.Lock
	var err error
	for try := 0; ; try++ {
		if l, err = lock.AcquireNamed(dir, lockName); err == nil {
			break
		}
		if !errors.Is(err, lock.ErrHeld) || try == 20 {
			return ErrUnavailable
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer l.Release()
	g, availability := Load(dir)
	switch availability {
	case Missing:
		if !create {
			return nil
		}
		id, err := NewID("supervision_")
		if err != nil {
			return err
		}
		g = Graph{SchemaVersion: SchemaVersion, LoopID: id}
	case Available:
	default:
		if !create {
			return nil
		}
		return ErrUnavailable
	}
	changed, err := fn(&g)
	if err != nil || !changed {
		return err
	}
	g.UpdatedAt, g.SchemaVersion = now.UTC(), SchemaVersion
	g.trim()
	g.index = nil
	// Keep empty collections as [] in the file.
	g.Registrations = append([]Registration{}, g.Registrations...)
	g.Runs = append([]Run{}, g.Runs...)
	g.Attempts = append([]Attempt{}, g.Attempts...)
	g.Links = append([]Link{}, g.Links...)
	g.Unrecorded = append([]Unrecorded{}, g.Unrecorded...)
	if g.Waiting != nil {
		g.Waiting.Children = append([]Child{}, g.Waiting.Children...)
	}
	if !g.valid() {
		return ErrUnavailable
	}
	b, err := json.Marshal(g)
	if err != nil || len(b)+1 > MaxBytes {
		return ErrUnavailable
	}
	if control.WritePrivateFile(filepath.Join(dir, FileName), append(b, '\n')) != nil {
		return ErrUnavailable
	}
	return nil
}

// GateOf reduces a saved control record to what a run boundary needs.
func GateOf(f control.File) Gate {
	return Gate{Enabled: f.Enabled, Paused: f.Paused, Stop: f.Stop, AwaitingUser: f.AwaitingUser, Completed: f.Completed, PendingTransfer: f.PendingTransfer != nil}
}
