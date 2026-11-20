package main

import (
	"encoding/binary"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	accessAdvapi32        = syscall.NewLazyDLL("advapi32.dll")
	accessOpenThreadToken = accessAdvapi32.NewProc("OpenThreadToken")
	accessLookupPrivilege = accessAdvapi32.NewProc("LookupPrivilegeValueW")
)

func doctorOSAccess(path string, mode uint32) error {
	// Keep the token guard and directory open on the same OS thread. An
	// impersonated thread is unavailable rather than checked as the process.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	access, flags, err := windowsAccessRequest(mode)
	if err != nil {
		return err
	}
	if mode == accessWrite|accessSearch {
		if err := windowsDirectoryIdentity(); err != nil {
			return err
		}
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// OPEN_EXISTING cannot create or truncate. Share all access so the
	// diagnostic does not impose a new read/write/delete exclusion.
	h, err := syscall.CreateFile(p, access,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, flags, 0)
	if err != nil {
		if err == syscall.Errno(19) { // ERROR_WRITE_PROTECT
			return os.ErrPermission
		}
		return err // Sharing violations and unknown failures remain unavailable.
	}
	return syscall.CloseHandle(h)
}

func windowsAccessRequest(mode uint32) (access, flags uint32, err error) {
	switch mode {
	case accessWrite | accessSearch:
		// FILE_ADD_FILE | FILE_ADD_SUBDIRECTORY | FILE_TRAVERSE.
		return 0x2 | 0x4 | 0x20, syscall.FILE_FLAG_BACKUP_SEMANTICS, nil
	case accessWrite:
		// Match Go's Windows O_CREATE|O_APPEND|O_WRONLY access mask, without
		// O_CREATE itself. In particular, do not demand FILE_WRITE_DATA.
		return syscall.FILE_APPEND_DATA | syscall.FILE_WRITE_ATTRIBUTES | 0x10 /* FILE_WRITE_EA */ |
			syscall.STANDARD_RIGHTS_WRITE | syscall.SYNCHRONIZE, 0, nil
	case accessRead | accessWrite:
		return syscall.GENERIC_READ | syscall.GENERIC_WRITE, 0, nil
	default:
		return 0, 0, errAccessUnavailable
	}
}

// FILE_FLAG_BACKUP_SEMANTICS is required to open directories, but can bypass
// normal checks with enabled backup/restore privileges. Do not report that as
// permission to create runtime files. No token or privilege is ever changed.
func windowsDirectoryIdentity() error {
	var threadToken syscall.Token
	ok, _, err := accessOpenThreadToken.Call(^uintptr(1), syscall.TOKEN_QUERY, 1, uintptr(unsafe.Pointer(&threadToken)))
	if ok != 0 {
		threadToken.Close()
		return errAccessUnavailable
	}
	if err != syscall.Errno(1008) { // ERROR_NO_TOKEN
		return errAccessUnavailable
	}
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return errAccessUnavailable
	}
	defer token.Close()
	// Bound the native result; unknown/oversized tokens are unavailable.
	buf := make([]byte, 64*1024)
	var n uint32
	if syscall.GetTokenInformation(token, syscall.TokenPrivileges, &buf[0], uint32(len(buf)), &n) != nil || n > uint32(len(buf)) {
		return errAccessUnavailable
	}
	var restricted [2]uint64
	for i, name := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		p, _ := syscall.UTF16PtrFromString(name)
		var luid [2]uint32
		ok, _, _ := accessLookupPrivilege.Call(0, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&luid[0])))
		if ok == 0 {
			return errAccessUnavailable
		}
		restricted[i] = uint64(luid[0]) | uint64(luid[1])<<32
	}
	return windowsDirectoryPrivileges(buf[:n], restricted)
}

func windowsDirectoryPrivileges(buf []byte, restricted [2]uint64) error {
	if len(buf) < 4 {
		return errAccessUnavailable
	}
	count := binary.LittleEndian.Uint32(buf)
	if uint64(count)*12+4 != uint64(len(buf)) {
		return errAccessUnavailable
	}
	for i := uint32(0); i < count; i++ {
		entry := buf[4+int(i)*12:]
		luid := binary.LittleEndian.Uint64(entry)
		attrs := binary.LittleEndian.Uint32(entry[8:])
		if attrs&2 != 0 && (luid == restricted[0] || luid == restricted[1]) { // SE_PRIVILEGE_ENABLED
			return errAccessUnavailable
		}
	}
	return nil
}
