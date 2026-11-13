package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
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
	b, err := snapshot.ReadFile(filepath.Join(dir, SendsName))
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
	b, _ := json.Marshal(struct {
		Version int   `json:"version"`
		Used    int64 `json:"used"`
	}{1, used + 1})
	if err := saveAllowance(dir, SendsName, b); err != nil {
		return ErrSendLedger
	}
	return nil
}

// RefundSend gives back one reservation. It is only for an attempt the
// provider answered, in a structured result, that it did not act on at all:
// nothing was sent and nothing was started. A failed or uncertain send is
// never refunded. A zero cap, or an empty ledger, changes nothing.
func RefundSend(dir string, max int64) error {
	if max == 0 {
		return nil
	}
	used, err := LoadSends(dir)
	if err != nil {
		return err
	}
	if used == 0 {
		return nil
	}
	b, _ := json.Marshal(struct {
		Version int   `json:"version"`
		Used    int64 `json:"used"`
	}{1, used - 1})
	if err := saveAllowance(dir, SendsName, b); err != nil {
		return ErrSendLedger
	}
	return nil
}

// saveAllowance syncs the replacement before publishing it. Callers hold tick.lock.
// As with other runtime records, this is not a power-loss/storage guarantee.
func saveAllowance(dir, name string, b []byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".allowance-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := snapshot.Replace(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return nil
}
