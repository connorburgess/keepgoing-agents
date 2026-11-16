//go:build unix

package main

import (
	"os"
	"syscall"
	"time"
)

// killParent kills the keepgoing process that ran this stand-in and returns
// only once it is gone. SIGKILL lands asynchronously: a stand-in that exits
// at once lets a parent that has not died yet see it end and start its next
// command, which then loses its parent and dies writing its answer. The
// harness would count that as a stand-in ended early. Staying alive until
// this process has been given to another parent keeps the killed one waiting
// on it, so it starts nothing more.
func killParent() {
	parent := os.Getppid()
	syscall.Kill(parent, syscall.SIGKILL)
	for deadline := time.Now().Add(10 * time.Second); os.Getppid() == parent && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
}
