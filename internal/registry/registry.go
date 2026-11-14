// Package registry stores explicit local loop references for optional clients.
// Registration is discovery metadata, never authority to run or control a loop.
package registry

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
	"runtime"
	"strings"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

const FileName = "registry.json"
const LockName = "registry"
const MaxEntries = 128
const maxBytes = 1 << 20

var ErrInvalid = errors.New("registry unavailable or invalid; inspect the private registry before editing")
var idPattern = regexp.MustCompile(`^loop_[0-9a-f]{32}$`)
var loopPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type Entry struct {
	ID         string `json:"id"`
	Loop       string `json:"loop"`
	ConfigPath string `json:"configPath"`
	StateDir   string `json:"stateDir"`
}
type File struct {
	SchemaVersion int     `json:"schemaVersion"`
	Entries       []Entry `json:"entries"`
}

// CheckDirectory does not create a missing directory or change existing modes.
// The caller must resolve ancestors and reject checkout destinations first.
func CheckDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return ErrInvalid
	}
	return nil
}
func privateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return ErrInvalid
	}
	return nil
}
func validText(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 4096 && !strings.ContainsAny(s, "\x00\r\n")
}
func validEntry(e Entry) bool {
	return idPattern.MatchString(e.ID) && loopPattern.MatchString(e.Loop) && validText(e.ConfigPath) && validText(e.StateDir) && filepath.IsAbs(e.ConfigPath) && filepath.IsAbs(e.StateDir) && filepath.Clean(e.ConfigPath) == e.ConfigPath && filepath.Clean(e.StateDir) == e.StateDir
}

// Load reads a bounded snapshot; a missing registry is empty, not an error.
// It does not inspect registered configs, lock, create directories or write.
func Load(dir string) (File, error) {
	empty := File{SchemaVersion: 1, Entries: []Entry{}}
	if CheckDirectory(dir) != nil {
		return File{}, ErrInvalid
	}
	path := filepath.Join(dir, FileName)
	if privateFile(path) != nil {
		return File{}, ErrInvalid
	}
	f, err := snapshot.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return File{}, ErrInvalid
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || len(b) > maxBytes {
		return File{}, ErrInvalid
	}
	var result File
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF || result.SchemaVersion != 1 || result.Entries == nil || len(result.Entries) > MaxEntries {
		return File{}, ErrInvalid
	}
	ids, paths, states := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range result.Entries {
		if !validEntry(e) || ids[e.ID] || paths[e.ConfigPath] || states[e.StateDir] {
			return File{}, ErrInvalid
		}
		ids[e.ID], paths[e.ConfigPath], states[e.StateDir] = true, true, true
	}
	return result, nil
}

// update serializes registry writers. It never writes loop files itself. The
// directory must have been verified by the command layer.
func update(dir string, fn func(*File) (bool, error)) error {
	if CheckDirectory(dir) != nil {
		return ErrInvalid
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ErrInvalid
	}
	if privateFile(filepath.Join(dir, LockName+".lock")) != nil {
		return ErrInvalid
	}
	held, err := lock.AcquireNamed(dir, LockName)
	if err != nil {
		if errors.Is(err, lock.ErrHeld) {
			return err
		}
		return ErrInvalid
	}
	defer held.Release()
	f, err := Load(dir)
	if err != nil {
		return err
	}
	changed, err := fn(&f)
	if err != nil || !changed {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil || len(b)+1 > maxBytes {
		return errors.New("registry size limit reached; no references were discarded")
	}
	tmp, err := os.CreateTemp(dir, ".registry-*")
	if err != nil {
		return ErrInvalid
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return ErrInvalid
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return ErrInvalid
	}
	if err := tmp.Close(); err != nil {
		return ErrInvalid
	}
	if err := snapshot.Replace(name, filepath.Join(dir, FileName)); err != nil {
		return ErrInvalid
	}
	return nil
}

// Add returns the existing ID for an identical registration. A changed binding
// or another config for the same runtime directory requires explicit removal.
func Add(dir, loop, configPath, stateDir string) (Entry, error) {
	entry := Entry{ID: "loop_00000000000000000000000000000000", Loop: loop, ConfigPath: configPath, StateDir: stateDir}
	if !validEntry(entry) {
		return Entry{}, errors.New("invalid loop reference")
	}
	err := update(dir, func(f *File) (bool, error) {
		for _, current := range f.Entries {
			// The same files under another spelling, as through the link a
			// renamed directory leaves, are the same registration.
			if current.ConfigPath != configPath && sameFile(current.ConfigPath, configPath) && sameFile(current.StateDir, stateDir) && current.Loop == loop {
				entry = current
				return false, nil
			}
			if current.ConfigPath == configPath {
				if current.Loop != loop || current.StateDir != stateDir {
					return false, errors.New("registered config binding changed; inspect and remove its old ID before registering again")
				}
				entry = current
				return false, nil
			}
			if current.StateDir == stateDir {
				return false, errors.New("runtime directory is already registered through another config")
			}
		}
		if len(f.Entries) >= MaxEntries {
			return false, errors.New("registry is full; remove an unused reference first")
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return false, errors.New("could not allocate registration identity")
		}
		entry.ID = "loop_" + hex.EncodeToString(id[:])
		f.Entries = append(f.Entries, entry)
		return true, nil
	})
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Remove forgets a reference even if its config is gone. It never stops a loop.
func Remove(dir, id string) error {
	if !idPattern.MatchString(id) {
		return errors.New("invalid registration ID")
	}
	return update(dir, func(f *File) (bool, error) {
		for i, entry := range f.Entries {
			if entry.ID == id {
				f.Entries = append(f.Entries[:i], f.Entries[i+1:]...)
				return true, nil
			}
		}
		return false, errors.New("registration ID not found; list the registry before retrying")
	})
}

// WithEntry pins an existing registration against cooperating add/remove calls
// during a short local operation. It never rewrites the registry. The callback
// may take nonblocking tick/control locks, but must not wait for a tick, query a
// provider or start a supervisor. External filesystem writers remain outside
// this advisory lock. A missing registration creates no directory or lock.
func WithEntry(dir, id string, fn func(Entry) error) error {
	if !idPattern.MatchString(id) {
		return ErrInvalid
	}
	find := func(f File) (Entry, bool) {
		for _, e := range f.Entries {
			if e.ID == id {
				return e, true
			}
		}
		return Entry{}, false
	}
	f, err := Load(dir)
	if err != nil {
		return err
	}
	if _, ok := find(f); !ok {
		return ErrInvalid
	}
	return update(dir, func(f *File) (bool, error) {
		e, ok := find(*f)
		if !ok {
			return false, ErrInvalid
		}
		return false, fn(e)
	})
}

// Rebase rewrites the paths of every reference with rewrite, which returns
// a path unchanged when it is not to move, and returns how many references
// changed. It is for a directory that has been renamed as a whole. It is
// safe to repeat: a second run finds nothing left to rewrite. A registry that
// does not exist is left absent. With dryRun it only counts.
func Rebase(dir string, rewrite func(string) string, dryRun bool) (int, error) {
	if _, err := os.Lstat(filepath.Join(dir, FileName)); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	plan := func(f *File) (int, error) {
		changed := 0
		paths, states := map[string]bool{}, map[string]bool{}
		for i, e := range f.Entries {
			moved := e
			moved.ConfigPath, moved.StateDir = rewrite(e.ConfigPath), rewrite(e.StateDir)
			if !validEntry(moved) || paths[moved.ConfigPath] || states[moved.StateDir] {
				return 0, errors.New("the references cannot be rewritten without two of them naming one place; nothing was changed")
			}
			paths[moved.ConfigPath], states[moved.StateDir] = true, true
			if moved != e {
				f.Entries[i] = moved
				changed++
			}
		}
		return changed, nil
	}
	if dryRun {
		f, err := Load(dir)
		if err != nil {
			return 0, err
		}
		return plan(&f)
	}
	changed := 0
	err := update(dir, func(f *File) (bool, error) {
		var err error
		changed, err = plan(f)
		return changed > 0, err
	})
	if err != nil {
		return 0, err
	}
	return changed, nil
}

// sameFile reports whether two paths lead to one existing file.
func sameFile(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	return err == nil && os.SameFile(ia, ib)
}
