package runner

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Private cooperative channel, not authentication against a writer with access
// to runtime state. Each wrapper knows only its own unpredictable per-turn key.
// A fresh request ID prevents a stale acknowledgement from confirming a stop.
type stopMessage struct {
	Key     string `json:"key"`
	Request string `json:"request"`
}

func randomStopID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func validStopID(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

func readStopMessage(path string) (stopMessage, error) {
	var m stopMessage
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return m, errors.New("runner: invalid stop message")
	}
	b, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(b) > 1024 {
		return m, errors.New("runner: invalid stop message")
	}
	if err := json.Unmarshal(b, &m); err != nil || !validStopID(m.Key) || !validStopID(m.Request) {
		return stopMessage{}, errors.New("runner: invalid stop message")
	}
	return m, nil
}

func writeStopMessage(path string, m stopMessage) error {
	b, _ := json.Marshal(m)
	f, err := os.CreateTemp(filepath.Dir(path), ".stop-*")
	if err != nil {
		return errors.New("runner: cannot create stop message")
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return errors.New("runner: cannot write stop message")
	}
	if err := f.Close(); err != nil {
		return errors.New("runner: cannot close stop message")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.New("runner: cannot save stop message")
	}
	return nil
}

func (d Dir) requestStop(key string) error {
	if !validStopID(key) {
		return errors.New("runner: stop refused: no owned stop channel; legacy turns require manual inspection")
	}
	request, err := randomStopID()
	if err != nil {
		return err
	}
	m := stopMessage{Key: key, Request: request}
	if err := writeStopMessage(d.path("stop-request.json"), m); err != nil {
		return err
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if ack, err := readStopMessage(d.path("stop-ack.json")); err == nil && ack == m {
			return nil
		}
		select {
		case <-deadline.C:
			return errors.New("runner: stop unconfirmed: owned wrapper did not acknowledge; request remains pending")
		case <-tick.C:
		}
	}
}

func watchStop(d Dir, key string, terminate func() error, done <-chan struct{}) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if m, err := readStopMessage(d.path("stop-request.json")); err == nil && m.Key == key {
			// Acknowledgement means accepted by the owned wrapper, not proof of exit.
			// Persist it before the wrapper terminates its own group/job.
			if writeStopMessage(d.path("stop-ack.json"), m) == nil {
				if terminate() == nil {
					return
				}
				// A failed termination must not remain positively acknowledged.
				_ = os.Remove(d.path("stop-ack.json"))
			}
		}
		select {
		case <-done:
			return
		case <-tick.C:
		}
	}
}
