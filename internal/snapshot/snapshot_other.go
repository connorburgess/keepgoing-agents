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

// OpenRecord is Open. Only on Windows does it also wait out "not found".
func OpenRecord(path string) (*os.File, error) { return os.Open(path) }

// Lstat is os.Lstat.
func Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }

// LstatRecord is os.Lstat. Only on Windows does it also wait out "not found".
func LstatRecord(path string) (os.FileInfo, error) { return os.Lstat(path) }

// Remove deletes a record. On Windows it also deletes the record's marker.
func Remove(path string) error { return os.Remove(path) }
