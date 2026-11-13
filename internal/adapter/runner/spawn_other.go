//go:build !unix && !windows

package runner

import (
	"errors"
	"os/exec"
)

var errUnsupported = errors.New("headless runners are not supported on this platform yet")

func spawn(argv []string, workdir, dir string, env []string, acp string) (int, error) {
	return 0, errUnsupported
}
func alive(pid int) bool                     { return false }
func ownedTerminator() (func() error, error) { return nil, errUnsupported }

func configureWrapper(cmd *exec.Cmd) {}
