//go:build !windows

package control

import "os"

func openSnapshotFile(path string) (*os.File, error) { return os.Open(path) }

func replaceSnapshotFile(source, target string) error { return os.Rename(source, target) }
