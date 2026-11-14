package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// CheckExecutableName is the private record of which file ran the loop's
// checks: its path, its build label, its identity (device, inode, size,
// modification time and link count) and when that was first seen. A panel
// started with changes allowed uses it to offer replacing that file with its
// own build, and only while the file at the path is still the one recorded.
// It is kept apart from state.json, which status prints whole: the path is
// never in status, a report, the timeline, the incident history or the
// panel's default view. Builds before this record existed never write it.
const CheckExecutableName = "check-executable.json"

const maxCheckExecutable = 8 << 10

// ErrCheckExecutable means the record is present but cannot be used.
var ErrCheckExecutable = errors.New("check executable record unavailable")

// FileIdentity says which file a path named when it was looked at. Two
// identities are equal only when the file is the same and unchanged.
type FileIdentity struct {
	Dev   uint64 `json:"dev"`
	Ino   uint64 `json:"ino"`
	Size  int64  `json:"size"`
	ModNs int64  `json:"modNs"`
	Nlink uint64 `json:"nlink"`
}

// IdentityOf is a file's identity from its metadata; ok is false where the
// platform gives no device and inode (Windows).
func IdentityOf(info os.FileInfo) (FileIdentity, bool) {
	dev, ino, nlink, ok := fileIDs(info)
	if !ok {
		return FileIdentity{}, false
	}
	return FileIdentity{Dev: dev, Ino: ino, Size: info.Size(), ModNs: info.ModTime().UnixNano(), Nlink: nlink}, true
}

// CheckExecutable is the record's content. At is when this file, with this
// label and identity, was first seen running a check.
type CheckExecutable struct {
	Version  int          `json:"version"`
	Path     string       `json:"path"`
	Label    string       `json:"label"`
	At       time.Time    `json:"at"`
	Identity FileIdentity `json:"identity"`
}

// CleanPath says whether p is an absolute, clean path a record may hold: no
// NUL, line break or other control character.
func CleanPath(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && len(p) <= 4096 && !strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// SaveCheckExecutable records the file that ran a check, looked at without
// following a final symlink. It writes only when the path, label or identity
// differs from the record, so an unchanged file costs no write.
func SaveCheckExecutable(dir, path, label string, at time.Time, expected ...FileIdentity) error {
	if !CleanPath(path) || len(label) > 256 {
		return ErrCheckExecutable
	}
	info, err := os.Lstat(path)
	if err != nil {
		return ErrCheckExecutable
	}
	id, ok := IdentityOf(info)
	if !ok || (len(expected) > 0 && id != expected[0]) {
		return ErrCheckExecutable
	}
	if cur, err := LoadCheckExecutable(dir); err == nil && cur != nil && cur.Path == path && cur.Label == label && cur.Identity == id {
		return nil
	}
	b, err := json.Marshal(CheckExecutable{Version: 1, Path: path, Label: label, At: at.UTC(), Identity: id})
	if err != nil {
		return ErrCheckExecutable
	}
	return saveAllowance(dir, CheckExecutableName, b)
}

// LoadCheckExecutable reads the record: nil with no error when there is none.
func LoadCheckExecutable(dir string) (*CheckExecutable, error) {
	path := filepath.Join(dir, CheckExecutableName)
	if info, err := snapshot.LstatRecord(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil || !info.Mode().IsRegular() || info.Size() > maxCheckExecutable {
		return nil, ErrCheckExecutable
	}
	b, err := snapshot.ReadRecord(path)
	if err != nil || len(b) > maxCheckExecutable {
		return nil, ErrCheckExecutable
	}
	var r CheckExecutable
	if StrictKeys(b, "version", "path", "label", "at", "identity") != nil || StrictDecode(b, &r) != nil ||
		StrictIdentity(b) != nil || r.Version != 1 || !CleanPath(r.Path) || r.At.IsZero() || r.Identity.Ino == 0 ||
		strings.ContainsFunc(r.Label, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, ErrCheckExecutable
	}
	return &r, nil
}

// StrictKeys says whether b is one JSON object whose keys are exactly some of
// keys, each spelled exactly and given once. encoding/json matches keys
// without regard to case and keeps the last of a repeated key; a record
// read for a decision must not be open to either.
func StrictKeys(b []byte, keys ...string) error {
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return errors.New("not an object")
	}
	seen := map[string]bool{}
	for d.More() {
		t, err := d.Token()
		k, ok := t.(string)
		if err != nil || !ok || !allowed[k] || seen[strings.ToLower(k)] {
			return fmt.Errorf("key %q", k)
		}
		seen[strings.ToLower(k)] = true
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return err
		}
		if bytes.HasPrefix(bytes.TrimSpace(v), []byte("{")) {
			if err := StrictNested(v); err != nil {
				return err
			}
		}
	}
	if t, err := d.Token(); err != nil || t != json.Delim('}') {
		return errors.New("not one object")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// StrictNested refuses a repeated or case-variant key in a nested object.
func StrictNested(b []byte) error {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return errors.New("not an object")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return StrictKeys(b, keys...)
}

// StrictDecode decodes one object with no unknown field.
func StrictDecode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// StrictIdentity checks the canonical identity member names as well as duplicates.
func StrictIdentity(b []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil {
		return ErrCheckExecutable
	}
	return StrictKeys(fields["identity"], "dev", "ino", "size", "modNs", "nlink")
}
