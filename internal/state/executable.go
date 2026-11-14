package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// CheckExecutableName is the private record of which file ran the loop's
// last check: its path, its build label and when. A panel started with
// changes allowed uses it to offer replacing that file with its own build.
// It is kept apart from state.json, which status prints whole: the path is
// never in status, a report, the timeline, the incident history or the
// panel's default view.
const CheckExecutableName = "check-executable.json"

const maxCheckExecutable = 8 << 10

// ErrCheckExecutable means the record is present but cannot be used.
var ErrCheckExecutable = errors.New("check executable record unavailable")

// CheckExecutable is the record's content.
type CheckExecutable struct {
	Version int       `json:"version"`
	Path    string    `json:"path"`
	Label   string    `json:"label"`
	At      time.Time `json:"at"`
}

// SaveCheckExecutable records the file that ran a check. path must be
// absolute.
func SaveCheckExecutable(dir, path, label string, at time.Time) error {
	if !filepath.IsAbs(path) || len(path) > 4096 || len(label) > 256 {
		return ErrCheckExecutable
	}
	b, err := json.Marshal(CheckExecutable{Version: 1, Path: path, Label: label, At: at.UTC()})
	if err != nil {
		return ErrCheckExecutable
	}
	return saveAllowance(dir, CheckExecutableName, b)
}

// LoadCheckExecutable reads the record: nil with no error when there is none.
func LoadCheckExecutable(dir string) (*CheckExecutable, error) {
	path := filepath.Join(dir, CheckExecutableName)
	if info, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil || !info.Mode().IsRegular() || info.Size() > maxCheckExecutable {
		return nil, ErrCheckExecutable
	}
	b, err := snapshot.ReadFile(path)
	if err != nil || len(b) > maxCheckExecutable {
		return nil, ErrCheckExecutable
	}
	var r CheckExecutable
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.Version != 1 || !filepath.IsAbs(r.Path) || r.At.IsZero() {
		return nil, ErrCheckExecutable
	}
	return &r, nil
}
