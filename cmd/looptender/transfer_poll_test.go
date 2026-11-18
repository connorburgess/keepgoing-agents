package main

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/state"
)

// Client status, hold, resume and transfer commands read control.json and
// state.json through readTransferRecord. A client polling them must not make a
// pause, a stop or the daemon's own record fail (on Windows an ordinary open
// handle blocks replacement), and must never read a record as missing, torn
// or as anything but a version that was saved.
//
// One failure is allowed, on Windows only: while a record is replaced Windows
// can report its name missing, and the reader waits a bounded time before it
// gives up (snapshot.OpenRecord) with errRecordBusy. A hosted Windows runner
// was measured taking up to 0.6 s for one replacement, longer than that wait,
// in about one run in thirty. Such reads are counted in the log. Any other
// failed read fails the test, including a torn or undecodable one, and so does
// a run in which reads of either record did not succeed while saves were
// being made. Elsewhere a replacement is a single rename and no read may fail.
func TestTransferRecordReadsDoNotBlockControlOrStateSaves(t *testing.T) {
	dir := t.TempDir()
	if err := control.Save(dir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(dir, state.File{Reprompts: 1}); err != nil {
		t.Fatal(err)
	}
	// Reads that succeed while saves are still being made, per record.
	const wantRead = 8
	var saving atomic.Bool
	saving.Store(true)
	var stop atomic.Bool
	var polls, ctlRead, stRead, busy atomic.Int64
	var wg sync.WaitGroup
	read := func(name string, dst any) bool {
		err := readTransferRecord(dir, name, dst)
		switch {
		case err == nil:
			return true
		case runtime.GOOS == "windows" && errors.Is(err, errRecordBusy):
			busy.Add(1)
		default:
			t.Errorf("%s read during save: %v", name, err)
		}
		return false
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() && !t.Failed() {
				var ctl control.File
				var st state.File
				// A read that succeeds must be a version that was saved: a
				// missing record would read as a zero value here.
				if read(control.FileName, &ctl) {
					if !ctl.Enabled {
						t.Errorf("control read during save: %+v", ctl)
						return
					}
					if saving.Load() {
						ctlRead.Add(1)
					}
				}
				if read(state.FileName, &st) {
					if st.Reprompts < 1 {
						t.Errorf("state read during save: %+v", st)
						return
					}
					if saving.Load() {
						stRead.Add(1)
					}
				}
				polls.Add(1)
			}
		}()
	}
	// At least 300 saves of each, and on until each record has been read
	// while saves were being made; reads that never succeed fail the test
	// after 10000.
	last := 1
	for i := 2; (i <= 300 || ctlRead.Load() < wantRead || stRead.Load() < wantRead) && !t.Failed(); i++ {
		if i > 10000 {
			t.Fatalf("after %d saves of each, control was read %d times and state %d times while saving", i-1, ctlRead.Load(), stRead.Load())
		}
		if err := control.Save(dir, control.File{Enabled: true, Paused: i%2 == 0}); err != nil {
			t.Errorf("control save %d blocked by a reader: %v", i, err)
		}
		if err := state.Save(dir, state.File{Reprompts: i}); err != nil {
			t.Errorf("state save %d blocked by a reader: %v", i, err)
		}
		last = i
	}
	saving.Store(false)
	stop.Store(true)
	wg.Wait()
	// Once nothing is being replaced, every read succeeds and sees the last
	// save.
	var ctl control.File
	var st state.File
	if err := readTransferRecord(dir, control.FileName, &ctl); err != nil || !ctl.Enabled || ctl.Paused != (last%2 == 0) {
		t.Errorf("control after the saves: %+v %v", ctl, err)
	}
	if err := readTransferRecord(dir, state.FileName, &st); err != nil || st.Reprompts != last {
		t.Errorf("state after the saves: reprompts %d, want %d: %v", st.Reprompts, last, err)
	}
	t.Logf("%d saves of each, %d polls; read while saving: control %d, state %d; %d reads busy while a record was being replaced", last-1, polls.Load(), ctlRead.Load(), stRead.Load(), busy.Load())
}
