package state

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot/snapshottest"
)

// status reads the send and progress allowances while a tick replaces them. A
// blocked reservation fails the tick before it sends.
func TestStateLedgerSnapshotSavesWhileReadersPoll(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var previous *Progress
	read := func() error {
		if _, err := LoadSends(dir); err != nil {
			return err
		}
		_, err := LoadProgress(dir)
		return err
	}
	snapshottest.ReadersPoll(t, 300, read, func(i int) error {
		p, err := ObserveProgress(dir, previous, "fictional-sources", strconv.Itoa(i), start.Add(time.Duration(i)*time.Second), false)
		if err != nil {
			return errors.Join(errors.New("progress"), err)
		}
		previous = &p
		return reserve(dir, 1000)
	})
	if used, err := LoadSends(dir); err != nil || used != 300 {
		t.Fatalf("reserved %d %v", used, err)
	}
}
