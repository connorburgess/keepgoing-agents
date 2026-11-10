//go:build !unix && !windows

package main

import (
	"errors"
	"os/exec"
)

func detachDaemon(*exec.Cmd) error {
	return errors.New("background start is unsupported on this platform; use --foreground")
}
