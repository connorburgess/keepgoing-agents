package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// SendsName is separate from diagnostic state so a failed check or stale record
// cannot refund a send. Only attempts made with max_sends enabled are counted.
const SendsName = "sends.json"

var ErrSendLimit = errors.New("loop send limit reached")
var ErrSendLedger = errors.New("send allowance unavailable; inspect private sends.json and runtime access")

// LoadSends reads the reserved attempt count. Missing means a new allowance;
// malformed, unknown-version, or incomplete records fail closed without echoing
// file contents. The caller serializes reservations with the loop's tick lock.
func LoadSends(dir string) (int64, error) {
	b, err := os.ReadFile(filepath.Join(dir, SendsName))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	var record struct {
		Version int    `json:"version"`
		Used    *int64 `json:"used"`
	}
	if err != nil || json.Unmarshal(b, &record) != nil || record.Version != 1 || record.Used == nil || *record.Used < 0 {
		return 0, ErrSendLedger
	}
	return *record.Used, nil
}

// ReserveSend persists an attempt BEFORE calling an adapter. Failed or uncertain
// sends are never refunded. A synced temporary file and atomic rename preserve
// the count across process restarts; this is not a power-loss/storage guarantee.
// A zero cap disables accounting and leaves any earlier ledger untouched.
func ReserveSend(dir string, max int64) error {
	if max == 0 {
		return nil
	}
	used, err := LoadSends(dir)
	if err != nil {
		return err
	}
	if used >= max {
		return ErrSendLimit
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ErrSendLedger
	}
	f, err := os.CreateTemp(dir, ".sends-*")
	if err != nil {
		return ErrSendLedger
	}
	defer os.Remove(f.Name())
	b, _ := json.Marshal(struct {
		Version int   `json:"version"`
		Used    int64 `json:"used"`
	}{1, used + 1})
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrSendLedger
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, SendsName)); err != nil {
		return ErrSendLedger
	}
	return nil
}
