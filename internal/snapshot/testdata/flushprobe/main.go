// Command flushprobe is built by TestFlushCannotBeSkippedOutsideATestBinary
// as an ordinary program, the way the keepgoing command is built. It reports
// what the flush helper does there. It is never part of a release.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

func main() {
	dir, err := os.MkdirTemp("", "flushprobe-")
	if err != nil {
		fmt.Println("setup failed:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(dir)
	f, err := os.Create(filepath.Join(dir, "record"))
	if err != nil {
		fmt.Println("setup failed:", err)
		os.Exit(2)
	}
	fmt.Println("open file flushed:", snapshot.Sync(f) == nil)
	f.Close()
	// A closed file cannot be flushed, so an error shows the flush was tried.
	fmt.Println("flush attempted before skip:", snapshot.Sync(f) != nil)
	func() {
		defer func() { fmt.Println("skip refused:", recover() != nil) }()
		snapshot.SkipSyncInTests()
	}()
	fmt.Println("flush attempted after skip:", snapshot.Sync(f) != nil)
}
