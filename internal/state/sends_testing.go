package state

import "encoding/json"

// SpendForTest counts one attempt against a send allowance without
// recording an intended send, for tests that start from a spent allowance.
// It is not used outside tests: every send in the supervisor is recorded
// with Reserve.
func SpendForTest(dir string, max int64) error {
	l, err := LoadLedger(dir)
	if err != nil {
		return err
	}
	if max > 0 && *l.Used >= max {
		return ErrSendLimit
	}
	used := *l.Used + 1
	l.Used = &used
	b, _ := json.Marshal(l)
	return saveAllowance(dir, SendsName, b)
}
