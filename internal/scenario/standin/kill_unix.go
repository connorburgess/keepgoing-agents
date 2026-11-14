//go:build unix

package main

import (
	"os"
	"syscall"
)

func killParent() { syscall.Kill(os.Getppid(), syscall.SIGKILL) }
