package runner

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestSpawnObserverRemovalDrainsWithoutRegistryLock(t *testing.T) {
	entered, release, removed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	remove := ObserveSpawnsForTest(func(*os.Process, string) { close(entered); <-release })
	go observeSpawnForTest(&os.Process{}, "fictional-project")
	<-entered
	go func() { remove(); close(removed) }()
	// Another observer can be installed/removed while the first callback is held.
	other := ObserveSpawnsForTest(func(*os.Process, string) {})
	other()
	select {
	case <-removed:
		t.Fatal("removal did not drain callback")
	default:
	}
	close(release)
	select {
	case <-removed:
	case <-time.After(time.Second):
		t.Fatal("removal did not finish")
	}
	remove()                                                // idempotent
	observeSpawnForTest(&os.Process{}, "fictional-project") // removed callback cannot run again
}

func TestSpawnObserversConcurrentInstallRemove(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			remove := ObserveSpawnsForTest(func(*os.Process, string) {})
			observeSpawnForTest(&os.Process{}, "fictional-project")
			remove()
		}()
	}
	wg.Wait()
}
