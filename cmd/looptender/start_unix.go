//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

func detachDaemon(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
