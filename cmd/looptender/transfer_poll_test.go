package main

import (
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
// handle blocks replacement), and must never read a record as missing or as
// anything but a version that was saved.
//
// A read may fail as unavailable, on Windows only: while a record is replaced
// Windows can report its name missing, and the reader waits for a bounded
// time before giving up (snapshot.OpenRecord). A hosted Windows runner was
// measured taking up to 0.6 s for one replacement, longer than that wait, in
// about one run in thirty. Such a read is counted in the log, not failed: it
// is the documented answer, and what this test guards against is a read that
// would let a client act on a missing or wrong record. Elsewhere a replacement
// is a single rename and no read may fail.
func TestTransferRecordReadsDoNotBlockControlOrStateSaves(t *testing.T) {
	dir := t.TempDir()
	if err := control.Save(dir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(dir, state.File{Reprompts: 1}); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var reads, unavailable atomic.Int64
	var wg sync.WaitGroup
	read := func(name string, dst any) bool {
		err := readTransferRecord(dir, name, dst)
		if err != nil {
			if runtime.GOOS != "windows" {
				t.Errorf("%s read during save: %v", name, err)
			} else if err.Error() != "record unavailable" {
				t.Errorf("%s read during save failed with %v, not as unavailable", name, err)
			}
			unavailable.Add(1)
			return false
		}
		return true
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
				if read(control.FileName, &ctl) && !ctl.Enabled {
					t.Errorf("control read during save: %+v", ctl)
					return
				}
				if read(state.FileName, &st) && st.Reprompts < 1 {
					t.Errorf("state read during save: %+v", st)
					return
				}
				reads.Add(1)
			}
		}()
	}
	const last = 300
	for i := 2; i <= last && !t.Failed(); i++ {
		if err := control.Save(dir, control.File{Enabled: true, Paused: i%2 == 0}); err != nil {
			t.Errorf("control save %d blocked by a reader: %v", i, err)
		}
		if err := state.Save(dir, state.File{Reprompts: i}); err != nil {
			t.Errorf("state save %d blocked by a reader: %v", i, err)
		}
	}
	for reads.Load() < 8 && !t.Failed() {
		runtime.Gosched()
	}
	stop.Store(true)
	wg.Wait()
	// Once nothing is being replaced, every read succeeds and sees the last
	// save.
	var ctl control.File
	var st state.File
	if err := readTransferRecord(dir, control.FileName, &ctl); err != nil || !ctl.Enabled || !ctl.Paused {
		t.Errorf("control after the saves: %+v %v", ctl, err)
	}
	if err := readTransferRecord(dir, state.FileName, &st); err != nil || st.Reprompts != last {
		t.Errorf("state after the saves: reprompts %d %v", st.Reprompts, err)
	}
	t.Logf("%d polls, %d reads unavailable while a record was being replaced", reads.Load(), unavailable.Load())
}
