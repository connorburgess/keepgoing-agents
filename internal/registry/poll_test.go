package registry

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot/snapshottest"
)

// Client commands list the registry while another registers a loop.
func TestRegistrySnapshotSavesWhileReadersPoll(t *testing.T) {
	dir := privateDir(t)
	read := func() error { _, err := Load(dir); return err }
	const saves = 40
	snapshottest.ReadersPoll(t, saves, read, func(i int) error {
		name := fmt.Sprintf("loop-%d", i)
		_, err := Add(dir, name, filepath.Join(filepath.Dir(dir), name+".yaml"), filepath.Join(filepath.Dir(dir), name+"-state"))
		return err
	})
	if f, err := Load(dir); err != nil || len(f.Entries) != saves {
		t.Fatalf("entries=%d %v", len(f.Entries), err)
	}
}
