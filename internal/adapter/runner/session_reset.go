package runner

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/looptender/looptender/internal/snapshot"
)

// This fence and its Windows .present companion belong to one turn. Setup
// moves/restores/discards both under turn.lock; an unexpected generation is
// unavailable, never authority to restore a deliberately forgotten session.
const sessionResetFile = "session-reset"
const maxSessionResetBytes = 1024

func (d Dir) sameGeneration(st Status) error {
	rec, err := d.record()
	if errors.Is(err, os.ErrNotExist) && st.Phase == NeverRun {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Phase == NeverRun || rec != st.Record {
		return errSuperseded
	}
	return nil
}

func resetFenceBytes(rec Record) ([]byte, error) {
	// Legacy records need not have a stop key, but their timestamp still names
	// their generation. Delimiters and line breaks cannot enter a fence key.
	if strings.ContainsAny(rec.StopKey, "|\r\n") {
		return nil, errors.New("runner: invalid reset generation")
	}
	b := []byte(waitKey(rec) + "\n")
	if len(b) > maxSessionResetBytes {
		return nil, errors.New("runner: reset generation exceeds limit")
	}
	return b, nil
}

// Called with a shared/exclusive turn guard: no cooperating setup or reset
// can remove/replace a fence while this bounded read is in progress.
func (d Dir) sessionWasReset(rec Record) (bool, error) {
	want, err := resetFenceBytes(rec)
	if err != nil {
		return false, err
	}
	p := d.path(sessionResetFile)
	info, err := snapshot.LstatRecord(p)
	if errors.Is(err, os.ErrNotExist) {
		// A failed setup/rollback may leave the fence in the aside location.
		// Absence then is unavailable, not permission to restore the old result.
		for _, name := range []string{sessionResetFile, sessionResetFile + ".present"} {
			if _, asideErr := os.Lstat(d.aside(name)); !errors.Is(asideErr, os.ErrNotExist) {
				return false, errors.New("runner: session reset rollback is incomplete")
			}
		}
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSessionResetBytes {
		return false, errors.New("runner: session reset record unavailable")
	}
	f, err := snapshot.OpenRecord(p)
	if err != nil {
		return false, errors.New("runner: session reset record unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return false, errors.New("runner: session reset record changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSessionResetBytes+1))
	if err != nil || len(b) > maxSessionResetBytes || !bytes.Equal(b, want) {
		return false, errors.New("runner: invalid or mismatched session reset record")
	}
	return true, nil
}

// Narrow publication seams for short writes, close/rename failure and partial
// reset tests. No other runner record is migrated to snapshot publication.
var writeResetFence = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
var closeResetFence = func(f *os.File) error { return f.Close() }
var replaceResetFence = snapshot.ReplaceStrict
var removeResetSession = os.Remove
var removeResetFence = snapshot.Remove

func (d Dir) publishResetFence(rec Record) error {
	b, err := resetFenceBytes(rec)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(string(d), ".session-reset-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, writeErr := writeResetFence(f, b)
	closeErr := closeResetFence(f)
	// Always release the real handle, including a deliberately failing seam.
	_ = f.Close()
	if writeErr != nil {
		return writeErr
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	if closeErr != nil {
		return closeErr
	}
	// Closed staging is atomically published before deleting the session.
	// This is not a claim of durability across power loss/directory loss.
	return replaceResetFence(f.Name(), d.path(sessionResetFile))
}

// Caller owns turn.lock throughout sampling, publication and deletion. A
// first-start reset also writes a zero-generation fence, which first setup
// moves aside normally. Repeated reset never truncates the previous fence.
func (d Dir) resetSession(st Status) (had bool, err error) {
	if _, err := os.Lstat(d.path("session")); err == nil {
		had = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(string(d), 0700); err != nil {
		return had, err
	}
	if err := d.publishResetFence(st.Record); err != nil {
		return had, err
	}
	if err := removeResetSession(d.path("session")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return had, errors.New("runner: reset fence saved but session removal failed; reset is incomplete")
	}
	return had, nil
}
