//go:build unix

package runner

import (
	"os"
	"os/exec"
	"syscall"
)

// The shell wrapper records the exit status atomically after the command
// ends, so a reader never sees a half-written code.
const wrapper = `"$@" < "$KG_DIR/prompt" > "$KG_DIR/stdout" 2> "$KG_DIR/stderr"; echo $? > "$KG_DIR/exit.tmp" && mv "$KG_DIR/exit.tmp" "$KG_DIR/exit"`

func spawn(argv []string, workdir, dir string, env []string) (int, error) {
	args := append([]string{"-c", wrapper, "keepgoing-turn"}, argv...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.Dir = workdir
	cmd.Env = append(append(os.Environ(), env...), "KG_DIR="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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

func terminate(pid int) error {
	if pid <= 0 {
		return nil
	}
	// Negative pid signals the whole session's process group.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
