//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

func openServiceBinary(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
