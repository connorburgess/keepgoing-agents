//go:build !windows

package snapshot

import "os"

// Open opens path read-only. An open file never blocks Replace here, and it
// remains the old snapshot afterwards.
func Open(path string) (*os.File, error) { return os.Open(path) }

// Replace atomically moves a closed staging file over target.
func Replace(source, target string) error { return os.Rename(source, target) }

// ReplaceStrict is Replace; the two differ only on Windows.
func ReplaceStrict(source, target string) error { return os.Rename(source, target) }

// Lstat is os.Lstat. On Windows it asks again before believing a record that
// is replaced by rename is missing.
func Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }
