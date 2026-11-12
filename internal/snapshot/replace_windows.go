package snapshot

import (
	"os"
	"syscall"
	"unsafe"
)

var setSnapshotInformation = syscall.NewLazyDLL("kernel32.dll").NewProc("SetFileInformationByHandle")

const (
	snapshotDeleteAccess    = 0x00010000
	snapshotRenameInfoEx    = 22
	snapshotReplaceExisting = 0x1
	snapshotPOSIXSemantics  = 0x2
)

// The trailing array fixes the allocation bound and preserves HANDLE alignment
// on both 32- and 64-bit Windows. FileNameLength excludes the UTF-16 terminator.
type snapshotRenameInfo struct {
	Flags          uint32
	RootDirectory  syscall.Handle
	FileNameLength uint32
	FileName       [32768]uint16
}

// Replace moves a closed staging file over target. On Windows, MoveFileEx
// cannot replace an open destination, even with DELETE sharing, so this uses a
// POSIX-semantics rename: Windows 10 1709+ and a filesystem supporting it are
// required. Unsupported operations fail; no remove-first, legacy rename or
// retry fallback is safe to advertise as retaining snapshots with the same
// semantics. A reader that opened target without DELETE sharing, which Open
// never does, still blocks replacement.
func Replace(source, target string) error {
	return replaceSnapshotWith(source, target, setSnapshotRename)
}

func replaceSnapshotWith(source, target string, set func(syscall.Handle, *snapshotRenameInfo, uint32) error) error {
	wrap := func(err error) error { return &os.LinkError{Op: "rename", Old: source, New: target, Err: err} }
	from, err := snapshotWindowsPath(source)
	if err != nil {
		return wrap(err)
	}
	to, err := snapshotWindowsPath(target)
	if err != nil {
		return wrap(err)
	}
	info := &snapshotRenameInfo{Flags: snapshotReplaceExisting | snapshotPOSIXSemantics}
	if len(to) > len(info.FileName) {
		return wrap(syscall.ENAMETOOLONG)
	}
	copy(info.FileName[:], to)
	info.FileNameLength = uint32((len(to) - 1) * 2)
	// DELETE is requested only on the already closed staging file. Opening its
	// reparse point rather than a target preserves rename's entry semantics.
	handle, err := syscall.CreateFile(&from[0], snapshotDeleteAccess|syscall.SYNCHRONIZE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return wrap(err)
	}
	defer syscall.CloseHandle(handle)
	size := uint32(unsafe.Offsetof(info.FileName)) + uint32(len(to)*2)
	if err := set(handle, info, size); err != nil {
		return wrap(err)
	}
	return nil
}

func setSnapshotRename(handle syscall.Handle, info *snapshotRenameInfo, size uint32) error {
	if err := setSnapshotInformation.Find(); err != nil {
		return err
	}
	result, _, err := setSnapshotInformation.Call(uintptr(handle), snapshotRenameInfoEx, uintptr(unsafe.Pointer(info)), uintptr(size))
	if result == 0 {
		if err == syscall.Errno(0) {
			return syscall.EINVAL
		}
		return err
	}
	return nil
}
