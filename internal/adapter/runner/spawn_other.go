//go:build !unix

package runner

import "errors"

var errUnsupported = errors.New("headless runners are not supported on this platform yet")

func spawn(argv []string, workdir, dir string, env []string) (int, error) { return 0, errUnsupported }
func alive(pid int) bool                                                  { return false }
func terminate(pid int) error                                             { return errUnsupported }
