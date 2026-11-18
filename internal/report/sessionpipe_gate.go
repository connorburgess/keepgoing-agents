package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

// SessionpipeLock serializes bounded network work and explicit report resume.
// It is distinct from the delivery-history lock, which never covers network I/O.
const SessionpipeLock = "sessionpipe"
const SessionpipeGateFile = "sessionpipe-gate.json"

var ErrSessionpipePaused = errors.New("Sessionpipe reports paused; inspect receiver credentials and run report resume; no report retried")
var errPipeGate = errors.New("Sessionpipe report gate unavailable; no delivery or reset attempted")

// Gate status is diagnostic only. It never affects loop controls or routing.
// pending is published before POST: exit before client.Do returns, or a failed
// final gate write, must not silently resume network delivery. It does not
// journal acknowledgement/history processing after client.Do returns. The file contains no destination or token.
type SessionpipeGate struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
}

func LoadSessionpipeGate(dir string) (SessionpipeGate, error) {
	if !filepath.IsAbs(dir) {
		return SessionpipeGate{}, errPipeGate
	}
	path := filepath.Join(dir, SessionpipeGateFile)
	info, err := snapshot.LstatRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return SessionpipeGate{Version: 1, Status: "ready"}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return SessionpipeGate{}, errPipeGate
	}
	f, err := snapshot.OpenRecord(path)
	if err != nil {
		return SessionpipeGate{}, errPipeGate
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() > 1024 || !os.SameFile(info, opened) {
		return SessionpipeGate{}, errPipeGate
	}
	b, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(b) > 1024 {
		return SessionpipeGate{}, errPipeGate
	}
	// Match the private schema exactly. encoding/json's struct decoder accepts
	// duplicate keys and case-insensitive aliases, which can hide a stored pause.
	dec := json.NewDecoder(bytes.NewReader(b))
	opening, err := dec.Token()
	if err != nil || opening != json.Delim('{') {
		return SessionpipeGate{}, errPipeGate
	}
	var g SessionpipeGate
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return SessionpipeGate{}, errPipeGate
		}
		seen[key] = true
		switch key {
		case "version":
			err = dec.Decode(&g.Version)
		case "status":
			err = dec.Decode(&g.Status)
		default:
			return SessionpipeGate{}, errPipeGate
		}
		if err != nil {
			return SessionpipeGate{}, errPipeGate
		}
	}
	closing, err := dec.Token()
	if err != nil || closing != json.Delim('}') || dec.Decode(new(any)) != io.EOF || len(seen) != 2 || g.Version != 1 {
		return SessionpipeGate{}, errPipeGate
	}
	switch g.Status {
	case "ready", "pending", "unauthorized":
		return g, nil
	}
	return SessionpipeGate{}, errPipeGate
}

func savePipeGate(dir, status string) error {
	b, err := json.Marshal(SessionpipeGate{Version: 1, Status: status})
	if err != nil {
		return errPipeGate
	}
	f, err := os.CreateTemp(dir, ".sessionpipe-gate-*")
	if err != nil {
		return errPipeGate
	}
	defer os.Remove(f.Name())
	n, err := f.Write(b)
	if err != nil || n != len(b) {
		f.Close()
		return errPipeGate
	}
	if snapshot.Sync(f) != nil {
		f.Close()
		return errPipeGate
	}
	if f.Close() != nil {
		return errPipeGate
	}
	if snapshot.Replace(f.Name(), filepath.Join(dir, SessionpipeGateFile)) != nil {
		return errPipeGate
	}
	return nil
}

// ResumeSessionpipe clears a valid report-only gate under the same exclusive
// guard as delivery. It never repairs corrupt state, changes identity/sequence,
// replays a report, calls the network, resumes a loop or clears its holds.
func ResumeSessionpipe(dir string) error {
	if !filepath.IsAbs(dir) {
		return errPipeGate
	}
	l, err := lock.AcquireNamed(dir, SessionpipeLock)
	if err != nil {
		return errPipeGate
	}
	defer l.Release()
	g, err := LoadSessionpipeGate(dir)
	if err != nil {
		return err
	}
	if g.Status == "ready" {
		return nil
	}
	return savePipeGate(dir, "ready")
}
