// Package snapshot reads and atomically replaces small runtime records so that
// a reader and a writer in different processes do not block each other. The
// rules differ only on Windows, where an ordinary open handle prevents the
// file from being replaced.
package snapshot
