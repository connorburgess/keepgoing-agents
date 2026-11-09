//go:build windows

package runner

import (
	"os/exec"
	"strconv"
	"syscall"
)

const (
	createNewProcessGroup  = 0x00000200
	detachedProcess        = 0x00000008
	processQueryLimitedInf = 0x1000
	stillActive            = 259
)

func spawn(argv []string, workdir, dir string, env []string) (int, error) {
	cmd, err := wrapperCommand(argv, workdir, dir, env)
	if err != nil {
		return 0, err
	}
	// Detached and in its own process group, so the turn outlives the tick
	// and can be ended as a tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess, HideWindow: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go cmd.Wait()
	return pid, nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInf, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// terminate ends the wrapper and every process it started.
func terminate(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
	if err != nil && !alive(pid) {
		return nil // already gone
	}
	return err
}
