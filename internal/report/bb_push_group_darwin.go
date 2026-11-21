//go:build darwin

package report

import (
	"encoding/binary"
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const bbPushMaxGroupMembers = 256

var errBBPushGroup = errors.New("notification process cleanup unconfirmed")

// bbPushGroupDrained may only inspect a group created by this invocation while
// its leader is still an owned, unreaped child. It neither signals nor reaps.
// An absent leader, partial result, or unknown state cannot establish cleanup.
func bbPushGroupDrained(leader int) (bool, error) {
	if leader <= 1 || int64(leader) > 1<<31-1 {
		return false, errBBPushGroup
	}
	// Darwin sys/sysctl.h: CTL_KERN, KERN_PROC, KERN_PROC_PGRP.
	mib := [4]int32{1, 14, 2, int32(leader)}
	// One fixed-buffer syscall, no size-probe allocation or ENOMEM retry loop.
	buf := make([]byte, bbPushMaxGroupMembers*unix.SizeofKinfoProc)
	n := uintptr(len(buf))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	runtime.KeepAlive(mib)
	runtime.KeepAlive(buf)
	if errno != 0 || n > uintptr(len(buf)) {
		return false, errBBPushGroup
	}
	return bbPushDecodeGroup(buf[:n], int32(leader))
}

func bbPushDecodeGroup(buf []byte, leader int32) (bool, error) {
	if leader <= 1 || len(buf) == 0 || len(buf)%unix.SizeofKinfoProc != 0 || len(buf) > bbPushMaxGroupMembers*unix.SizeofKinfoProc {
		return false, errBBPushGroup
	}
	var layout unix.KinfoProc
	pidOffset := int(unsafe.Offsetof(layout.Proc) + unsafe.Offsetof(layout.Proc.P_pid))
	groupOffset := int(unsafe.Offsetof(layout.Eproc) + unsafe.Offsetof(layout.Eproc.Pgid))
	stateOffset := int(unsafe.Offsetof(layout.Proc) + unsafe.Offsetof(layout.Proc.P_stat))
	seen := make(map[int32]bool)
	drained := true
	for offset := 0; offset < len(buf); offset += unix.SizeofKinfoProc {
		b := buf[offset : offset+unix.SizeofKinfoProc]
		pid := int32(binary.LittleEndian.Uint32(b[pidOffset:]))
		group := int32(binary.LittleEndian.Uint32(b[groupOffset:]))
		state := b[stateOffset]
		// sys/proc.h: states 1..4 are live; SZOMB=5 is awaiting reaping.
		if pid <= 1 || group != leader || seen[pid] || state < 1 || state > 5 {
			return false, errBBPushGroup
		}
		seen[pid] = true
		if state != 5 {
			drained = false
		}
	}
	if !seen[leader] {
		return false, errBBPushGroup
	}
	return drained, nil
}
