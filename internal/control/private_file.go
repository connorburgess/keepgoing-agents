package control

import (
	"os"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// WritePrivateFile atomically replaces a 0600 record in a state directory. It
// uses the same replacement as control.json, so a reader holding a snapshot
// does not block it on Windows. The data is flushed to disk before the
// replacement: callers of this function refuse to rewrite a record they cannot
// parse, so a file truncated by power loss would otherwise stay unusable until
// someone removed it. It takes no lock and creates no directory.
func WritePrivateFile(path string, data []byte) error {
	return writeAtomicSync(path, data, 0o600, true)
}

// OpenPrivateSnapshot opens a record written by WritePrivateFile without
// blocking a later replacement. The caller closes it.
func OpenPrivateSnapshot(path string) (*os.File, error) { return snapshot.OpenRecord(path) }
