package daemoninfo

import (
	"path/filepath"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot/snapshottest"
)

// status reads the endpoint record while a restarting daemon replaces it. A
// record that is briefly absent or stale is not a failed read; a replacement
// that cannot be published is a daemon that cannot start.
func TestEndpointSnapshotSavesWhileReadersPoll(t *testing.T) {
	dir := t.TempDir()
	first, err := Start(dir, "example-build", true)
	if err != nil {
		t.Fatal(err)
	}
	// Leave the first record in place so each later start replaces a file.
	first.listener.Close()
	<-first.done
	read := func() error { _, _ = readEndpoint(filepath.Join(dir, FileName)); return nil }
	var servers []*Server
	snapshottest.ReadersPoll(t, 40, read, func(int) error {
		s, err := Start(dir, "example-build", true)
		if err == nil {
			servers = append(servers, s)
		}
		return err
	})
	for _, s := range servers {
		s.Close()
	}
}
