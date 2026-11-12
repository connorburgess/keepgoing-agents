// Package snapshottest holds the shared shape of the tests that replace a
// runtime record while other readers poll it. It is imported by tests only.
package snapshottest

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// TB is the part of testing.TB used here.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Failed() bool
}

// ReadersPoll runs save(1..saves) while four goroutines call read in a loop.
// Every save and every read must succeed: on Windows an ordinary open handle
// makes a replacement fail, and a legacy replacement makes a read fail. It
// returns once every reader has completed at least two reads.
func ReadersPoll(t TB, saves int, read func() error, save func(i int) error) {
	t.Helper()
	var stop atomic.Bool
	var wg sync.WaitGroup
	const readers = 4
	counts := make([]atomic.Int64, readers)
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if err := read(); err != nil {
					t.Errorf("read while a save was in progress: %v", err)
					return
				}
				counts[r].Add(1)
			}
		}()
	}
	for i := 1; i <= saves && !t.Failed(); i++ {
		if err := save(i); err != nil {
			t.Errorf("save %d of %d while readers polled: %v", i, saves, err)
		}
	}
	for done := false; !done && !t.Failed(); runtime.Gosched() {
		done = true
		for r := range counts {
			done = done && counts[r].Load() >= 2
		}
	}
	stop.Store(true)
	wg.Wait()
}
