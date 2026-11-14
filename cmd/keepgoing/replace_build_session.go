package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

// A session owns the startup executable handle for the lifetime of one panel.
// The path is display-only: no replacement ever reopens it.
type buildSession struct {
	dir, registry, path, label, sha string
	source                          interface {
		io.ReaderAt
		Stat() (os.FileInfo, error)
		Close() error
	}
	err               error
	agent, daemonHeld bool
}

func newBuildSession(dir, registry string) *buildSession {
	b := &buildSession{dir: dir, registry: registry, label: dockBuildLabel(version), agent: os.Getenv("BB_THREAD_ID") != ""}
	b.path, b.err = buildSource()
	if b.err != nil {
		return b
	}
	if b.path == checkExecutable() {
		b.source, b.err = processExecutableImage.OpenReader()
	} else {
		b.source, b.err = os.Open(b.path)
	}
	if b.err != nil {
		return b
	}
	info, e := b.source.Stat()
	var data []byte
	if e == nil && info.Size() > 0 && info.Size() <= serviceBinaryLimit {
		data = make([]byte, info.Size())
		_, e = b.source.ReadAt(data, 0)
	}
	if e != nil || len(data) == 0 || int64(len(data)) > serviceBinaryLimit {
		b.err = errors.New("startup executable unreadable")
		b.close()
		return b
	}
	sum := sha256.Sum256(data)
	b.sha = hex.EncodeToString(sum[:])
	return b
}
func (b *buildSession) close() {
	if b != nil && b.source != nil {
		b.source.Close()
	}
}
func (b *buildSession) sourceBytes() ([]byte, error) {
	if b == nil || b.err != nil || b.source == nil {
		return nil, errors.New("source unavailable")
	}
	info, e := b.source.Stat()
	if e != nil || info.Size() > serviceBinaryLimit {
		return nil, errors.New("source unavailable")
	}
	data := make([]byte, info.Size())
	_, e = b.source.ReadAt(data, 0)
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != b.sha {
		return nil, errors.New("retained source changed")
	}
	return data, nil
}
func (b *buildSession) recordDir(entry registry.Entry) string {
	sum := sha256.Sum256([]byte(entry.ID + "\x00" + entry.StateDir + "\x00" + entry.ConfigPath))
	return filepath.Join(b.dir, "build-replacements", hex.EncodeToString(sum[:]))
}
func validBuildSHA(s string) bool {
	v, e := hex.DecodeString(s)
	return e == nil && len(v) == sha256.Size && strings.ToLower(s) == s
}
func validBuildBackup(s string) bool {
	t, e := time.Parse("20060102T150405.000000000Z", s)
	return e == nil && t.Format("20060102T150405.000000000Z") == s
}

func (b *buildSession) undoRefusal(entry registry.Entry, u *buildUpdate, daemon daemonStatus) string {
	if why := b.fileRefusal(entry, u.Target, daemon); why != "" {
		return why
	}
	if !buildIdentityMatches(u.Target, u.Identity) {
		return "The file has changed since this panel replaced it; Undo would overwrite someone else's change."
	}
	old, e := os.ReadFile(filepath.Join(b.recordDir(entry), buildBackupsDir, u.Backup, buildBackupFile))
	sum := sha256.Sum256(old)
	if e != nil || hex.EncodeToString(sum[:]) != u.FromSHA {
		return "The backed-up file is missing or changed; restore the file by hand from a trusted backup."
	}
	current, e := buildHashBytes(u.Target)
	if e != nil || current != u.ToSHA {
		return "The file changed since replacement; restore it by hand."
	}
	if e = buildSchemasMatch(filepath.Join(b.recordDir(entry), buildBackupsDir, u.Backup, buildBackupState), entry.StateDir); e != nil {
		return "A record format changed or could not be checked. Undo is unavailable. Stop checks and the daemon, copy the current configuration and state somewhere safe, then restore the binary and compatible configuration and state together from the private backup by hand. Restoring state discards later changes."
	}
	return ""
}
func buildHashBytes(path string) (string, error) {
	data, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Compare the schema markers in every JSON record, including records added or
// removed since replacement. Ambiguous or malformed JSON fails closed.
func buildSchemasMatch(backup, current string) error {
	read := func(root string) (map[string]map[string]json.RawMessage, error) {
		out := map[string]map[string]json.RawMessage{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if path != root && buildSkipState(d.Name()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
				return nil
			}
			if !d.Type().IsRegular() {
				return errors.New("nonregular record")
			}
			data, e := snapshot.ReadFile(path)
			if e != nil {
				return e
			}
			var object map[string]json.RawMessage
			if json.Unmarshal(data, &object) != nil || state.StrictNested(data) != nil {
				return errors.New("invalid record")
			}
			markers := map[string]json.RawMessage{}
			for _, key := range []string{"version", "schemaVersion"} {
				if v, ok := object[key]; ok {
					markers[key] = v
				}
			}
			if len(markers) > 0 {
				rel, _ := filepath.Rel(root, path)
				out[rel] = markers
			}
			return nil
		})
		return out, err
	}
	old, e := read(backup)
	if e != nil {
		return e
	}
	cur, e := read(current)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(old, cur) {
		return errors.New("schema changed")
	}
	return nil
}

// Include custom and default registries, including both on-disk tool names.
// Resolve and validate every candidate; an unreadable relevant registry is a
// refusal rather than evidence that no sibling exists.
func buildRelevantRegistries(custom string) ([]string, error) {
	var dirs []string
	seen := map[string]bool{}
	add := func(path string) error {
		dir, e := registryDirectory(path)
		if e != nil {
			return e
		}
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
		return nil
	}
	if e := add(custom); e != nil {
		return nil, e
	}
	if e := add(""); e != nil {
		return nil, e
	}
	base, e := buildUserConfigHome()
	if e != nil {
		return nil, e
	}
	for _, name := range []string{userdir.Name, userdir.LegacyName} {
		if e = add(filepath.Join(base, name, "registry")); e != nil {
			return nil, e
		}
	}
	return dirs, nil
}

var buildUserConfigHome = userdir.ConfigHome
