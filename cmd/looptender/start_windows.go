package main

import (
	"os/exec"
	"syscall"
)

func detachDaemon(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200 | 0x00000008, HideWindow: true}
	return nil
}
