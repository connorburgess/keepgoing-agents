package main

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Client status, hold, resume and transfer commands read control.json and
// state.json through readTransferRecord. A client polling them must not make a
// pause, a stop or the daemon's own record fail (on Windows an ordinary open
// handle blocks replacement).
func TestTransferRecordReadsDoNotBlockControlOrStateSaves(t *testing.T) {
	dir := t.TempDir()
	if err := control.Save(dir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(dir, state.File{Reprompts: 1}); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var reads atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				var ctl control.File
				var st state.File
				if err := readTransferRecord(dir, control.FileName, &ctl); err != nil || !ctl.Enabled {
					t.Errorf("control read during save: %+v %v", ctl, err)
					return
				}
				if err := readTransferRecord(dir, state.FileName, &st); err != nil || st.Reprompts < 1 {
					t.Errorf("state read during save: %+v %v", st, err)
					return
				}
				reads.Add(1)
			}
		}()
	}
	for i := 2; i <= 300 && !t.Failed(); i++ {
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
}
