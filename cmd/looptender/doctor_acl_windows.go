//go:build windows

package main

import (
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	aclAdvapi32 = syscall.NewLazyDLL("advapi32.dll")
	aclGetNamed = aclAdvapi32.NewProc("GetNamedSecurityInfoW")
	aclGetDACL  = aclAdvapi32.NewProc("GetSecurityDescriptorDacl")
	aclGetInfo  = aclAdvapi32.NewProc("GetAclInformation")
)

func doctorACLMetadata(path string, _ bool) aclMetadata {
	a := aclMetadata{Supported: true, Format: aclWindows, Access: aclUnavailable}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return a
	}
	p, err := syscall.UTF16PtrFromString(resolved)
	if err != nil || aclGetNamed.Find() != nil {
		return a
	}
	// Request only DACL information for SE_FILE_OBJECT. Windows allocates
	// the descriptor; we inspect its presence/count via native APIs without
	// parsing ACEs or resolving SIDs. Free the native allocation on return.
	var sd unsafe.Pointer
	code, _, _ := aclGetNamed.Call(uintptr(unsafe.Pointer(p)), 1, 4, 0, 0, 0, 0, uintptr(unsafe.Pointer(&sd)))
	runtime.KeepAlive(p)
	if code != 0 || sd == nil {
		return a
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	a.Access = windowsDACLMetadata(sd)
	return a
}

func windowsDACLMetadata(sd unsafe.Pointer) string {
	if sd == nil || aclGetDACL.Find() != nil || aclGetInfo.Find() != nil {
		return aclUnavailable
	}
	var present, defaulted int32 // Windows BOOL is 32 bits on both architectures.
	var dacl unsafe.Pointer
	ok, _, _ := aclGetDACL.Call(uintptr(sd), uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&defaulted)))
	if ok == 0 {
		return aclUnavailable
	}
	if present == 0 || dacl == nil {
		return aclUnrestricted
	}
	var info struct { // ACL_SIZE_INFORMATION, all DWORDs.
		Entries, BytesInUse, BytesFree uint32
	}
	ok, _, _ = aclGetInfo.Call(uintptr(dacl), uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 2) // AclSizeInformation
	runtime.KeepAlive(sd)
	if ok == 0 {
		return aclUnavailable
	}
	if info.Entries == 0 {
		return aclEmpty
	}
	return aclPresent
}
