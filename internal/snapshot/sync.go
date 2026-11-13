package snapshot

import (
	"os"
	"sync/atomic"
	"testing"
)

var skipSync atomic.Bool

// Sync flushes a record to the device before it is published by a rename.
// Every runtime record that is flushed goes through it.
func Sync(f *os.File) error {
	if skipSync.Load() {
		return nil
	}
	return f.Sync()
}

// SkipSyncInTests makes Sync a no-op for the rest of a test binary. It
// panics anywhere else, so no command, flag, setting or environment variable
// can turn the flush off.
//
// A flush cannot be observed by a test: nothing here cuts the power. It is
// also the slowest thing a test does. On macOS a flush waits for the device
// (about 4 ms against 0.1 ms for the write itself) and flushes from every
// process on the machine queue behind one another, so a package that runs
// thousands of checks spends nearly all its time waiting. See "Slow test
// packages" in docs/testing.md.
//
// Call it from an init function in a _test.go file. Tests that run the built
// binary, such as the scenario suite, are not affected and keep the flush.
//
// Three tests hold this in place. TestOnlyNamedTestFilesSkipTheFlush lists
// every file allowed to call it. TestFlushCannotBeSkippedOutsideATestBinary
// builds an ordinary program and shows the call panics there and the flush
// is attempted. TestBuiltBinaryCannotSkipTheFlush, in the scenario suite,
// checks that the built keepgoing command does not contain this function at
// all; it is kept out of line so that its presence in a binary means
// something there can call it.
//
//go:noinline
func SkipSyncInTests() {
	if !testing.Testing() {
		panic("snapshot: SkipSyncInTests called outside a test binary")
	}
	skipSync.Store(true)
}
