//go:build darwin

package main

import (
	"encoding/binary"
	"runtime"
	"syscall"
	"unsafe"
)

func doctorACLMetadata(path string, _ bool) aclMetadata {
	a := aclMetadata{Supported: true, Format: aclMacOS, Access: aclUnavailable}
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return a
	}
	// Darwin attrlist: five uint32 bitmaps after bitmapcount/reserved.
	// Request only ATTR_CMN_EXTENDED_SECURITY (sys/attr.h).
	var attrs [24]byte
	binary.LittleEndian.PutUint16(attrs[:2], 5)
	binary.LittleEndian.PutUint32(attrs[4:8], 0x00400000)
	// Copy out only length + attrreference, never the variable security
	// payload. FSOPT_REPORT_FULLSIZE returns the total required size even
	// though our buffer intentionally stops before any ACL contents.
	var header [12]byte
	_, _, errno := syscall.Syscall6(syscall.SYS_GETATTRLIST,
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&attrs[0])),
		uintptr(unsafe.Pointer(&header[0])), uintptr(len(header)), 0x4, 0)
	runtime.KeepAlive(p)
	runtime.KeepAlive(attrs)
	runtime.KeepAlive(header)
	if errno != 0 {
		return a
	}
	a.Access = darwinACLHeader(header[:])
	return a
}

// darwinACLHeader validates the fixed header without fetching or interpreting
// security payloads. Current Darwin arm64/amd64 both use little-endian fields.
func darwinACLHeader(header []byte) string {
	if len(header) != 12 {
		return aclUnavailable
	}
	total := uint64(binary.LittleEndian.Uint32(header[:4]))
	offset := int32(binary.LittleEndian.Uint32(header[4:8]))
	size := uint64(binary.LittleEndian.Uint32(header[8:12]))
	// Only one variable attribute was requested; its data follows this
	// header. Reject inconsistent layouts instead of inferring privacy.
	if offset != 8 || total != 12+size {
		return aclUnavailable
	}
	if size == 0 {
		return aclNotReported
	}
	return aclPresent
}
