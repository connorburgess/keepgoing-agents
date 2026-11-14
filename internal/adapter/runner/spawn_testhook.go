package runner

import (
	"os"
	"sync"
)

// ObserveSpawnsForTest installs a fixture callback at successful wrapper spawn,
// before cmd.Wait can release the owned process reference. It is only for tests
// retaining native handles for cleanup, not a production ownership/join API.
// Callbacks must finish promptly and must not wait for wrapper completion.
// The returned function removes this observer and drains callbacks already in
// flight. No callback or drain runs under the observer registry lock.
func ObserveSpawnsForTest(fn func(process *os.Process, workdir string)) func() {
	entry := &spawnTestObserver{fn: fn}
	spawnTestObservers.Lock()
	if spawnTestObservers.entries == nil {
		spawnTestObservers.entries = make(map[*spawnTestObserver]struct{})
	}
	spawnTestObservers.entries[entry] = struct{}{}
	spawnTestObservers.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			spawnTestObservers.Lock()
			delete(spawnTestObservers.entries, entry)
			spawnTestObservers.Unlock()
			entry.active.Wait()
		})
	}
}

type spawnTestObserver struct {
	fn     func(*os.Process, string)
	active sync.WaitGroup
}

var spawnTestObservers struct {
	sync.Mutex
	entries map[*spawnTestObserver]struct{}
}

func observeSpawnForTest(process *os.Process, workdir string) {
	spawnTestObservers.Lock()
	entries := make([]*spawnTestObserver, 0, len(spawnTestObservers.entries))
	for entry := range spawnTestObservers.entries {
		entry.active.Add(1)
		entries = append(entries, entry)
	}
	spawnTestObservers.Unlock()
	for _, entry := range entries {
		func() { defer entry.active.Done(); entry.fn(process, workdir) }()
	}
}
