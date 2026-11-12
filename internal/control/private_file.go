package control

import "os"

// WritePrivateFile atomically replaces a 0600 record in a state directory. It
// uses the same replacement as control.json, so a reader holding a snapshot
// does not block it on Windows. It takes no lock and creates no directory.
func WritePrivateFile(path string, data []byte) error { return writeAtomic(path, data, 0o600) }

// OpenPrivateSnapshot opens a record written by WritePrivateFile without
// blocking a later replacement. The caller closes it.
func OpenPrivateSnapshot(path string) (*os.File, error) { return openSnapshotFile(path) }
