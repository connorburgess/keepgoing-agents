//go:build unix

package runner

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func spawn(argv []string, workdir, dir string, env []string) (int, error) {
	cmd, err := wrapperCommand(argv, workdir, dir, env)
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	// The turn outlives this tick. Waiting in the background reaps the child
	// if it ends while this process is still alive (the daemon case); a
	// short-lived tick exits first and init adopts the detached session.
	go cmd.Wait()
	return pid, nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// The caller is the live session leader itself, so its group ID cannot be
// recycled between checking ownership and signalling. Never accept a saved PID.
func ownedTerminator() (func() error, error) {
	pid := os.Getpid()
	group, err := syscall.Getpgid(0)
	if err != nil || group != pid {
		return nil, errors.New("runner: wrapper does not own its process group")
	}
	return func() error { return syscall.Kill(-pid, syscall.SIGTERM) }, nil
}

func configureWrapper(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
