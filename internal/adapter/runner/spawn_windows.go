//go:build windows

package runner

import (
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup  = 0x00000200
	detachedProcess        = 0x00000008
	processQueryLimitedInf = 0x1000
	stillActive            = 259
)

func spawn(argv []string, workdir, dir string, env []string, acp string) (int, error) {
	cmd, err := wrapperCommand(argv, workdir, dir, env, acp)
	if err != nil {
		return 0, err
	}
	// Detached and in its own process group, so the turn outlives the tick
	// and can be ended as a tree.
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

// Hold an unnamed native job for this wrapper and its descendants. Termination
// uses the retained kernel handle, never a PID lookup or taskkill subprocess.
func ownedTerminator() (func() error, error) {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	job, _, err := kernel.NewProc("CreateJobObjectW").Call(0, 0)
	if job == 0 {
		return nil, err
	}
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		syscall.CloseHandle(syscall.Handle(job))
		return nil, err
	}
	ok, _, err := kernel.NewProc("AssignProcessToJobObject").Call(job, uintptr(process))
	if ok == 0 {
		syscall.CloseHandle(syscall.Handle(job))
		return nil, err
	}
	// This handle lives until wrapper exit. It is not inherited or published.
	return func() error {
		ok, _, err := kernel.NewProc("TerminateJobObject").Call(job, 1)
		if ok == 0 {
			return err
		}
		return nil
	}, nil
}

func configureWrapper(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess, HideWindow: true}
}
