//go:build unix

package main

import "syscall"

func liftFileSizeLimit() {
	var l syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_FSIZE, &l) == nil && l.Cur < l.Max {
		l.Cur = l.Max
		syscall.Setrlimit(syscall.RLIMIT_FSIZE, &l)
	}
}
