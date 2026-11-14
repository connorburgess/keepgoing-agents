package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// spendSends counts n attempts against a send allowance without recording
// an intended send, for tests that start from a spent allowance. It writes
// sends.json as the ledger does.
func spendSends(dir string, max int64) error {
	l, err := state.LoadLedger(dir)
	if err != nil {
		return err
	}
	if max > 0 && *l.Used >= max {
		return state.ErrSendLimit
	}
	used := *l.Used + 1
	l.Used = &used
	b, _ := json.Marshal(l)
	return os.WriteFile(filepath.Join(dir, state.SendsName), b, 0o600)
}
