package state

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// A status reader polls state.json while a daemon tick replaces it. Neither
// may fail: a failed save loses the tick's record, and on Windows an ordinary
// open handle used to make the replacement fail.
func TestStateSnapshotSavesWhileReadersPoll(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, File{Reprompts: 1}); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	readErrs := make(chan error, 4)
	var reads atomic.Int64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			last := 0
			for !stop.Load() {
				f, err := Load(dir)
				if err != nil {
					readErrs <- err
					return
				}
				// Replacement is atomic: never a partial or older record.
				if f.Reprompts < last || f.Reprompts < 1 {
					t.Errorf("read %d after %d", f.Reprompts, last)
					return
				}
				last = f.Reprompts
				reads.Add(1)
			}
		}()
	}
	const saves = 500
	failed := 0
	var firstErr error
	for i := 2; i <= saves; i++ {
		if err := Save(dir, File{Reprompts: i}); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	// Keep readers running until each has certainly overlapped the writer.
	for reads.Load() < 8 {
		if len(readErrs) > 0 {
			break
		}
		runtime.Gosched()
	}
	stop.Store(true)
	wg.Wait()
	close(readErrs)
	for err := range readErrs {
		t.Errorf("poll failed: %v", err)
	}
	if failed > 0 {
		t.Fatalf("%d of %d saves failed while readers polled; first: %v", failed, saves-1, firstErr)
	}
	if f, err := Load(dir); err != nil || f.Reprompts != saves {
		t.Fatalf("final record %+v %v", f, err)
	}
}
