package snapshot

import (
	"errors"
	"os"
	"syscall"
	"time"
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

// ReplaceStrict moves a closed staging file over target. On Windows, MoveFileEx
// cannot replace an open destination, even with DELETE sharing, so this uses a
// POSIX-semantics rename: Windows 10 1709+ and a filesystem supporting it are
// required. Unsupported operations fail; no remove-first, legacy rename or
// retry fallback is safe to advertise as retaining snapshots with the same
// semantics. A reader that opened target without DELETE sharing, which Open
// never does, still blocks replacement.
func ReplaceStrict(source, target string) error {
	return replaceSnapshotWith(source, target, setSnapshotRename, time.Sleep)
}

// Replace is ReplaceStrict with one fallback: where the system or filesystem
// reports that it does not support the POSIX-semantics rename, it uses the
// ordinary rename that these records used before. That keeps a volume such as
// exFAT or some network shares working, at the old cost: there, a reader
// holding target open blocks the replacement. A sharing violation or access
// denial is never retried this way.
func Replace(source, target string) error {
	return replaceWithFallback(source, target, setSnapshotRename, time.Sleep)
}

func replaceWithFallback(source, target string, set func(syscall.Handle, *snapshotRenameInfo, uint32) error, sleep func(time.Duration)) error {
	err := replaceSnapshotWith(source, target, set, sleep)
	if err == nil || !renameUnsupported(err) {
		return err
	}
	return os.Rename(source, target)
}

// renameUnsupported classifies by Windows error code, never by message text.
func renameUnsupported(err error) bool {
	var code syscall.Errno
	if !errors.As(err, &code) {
		return false
	}
	switch code {
	case 1 /* ERROR_INVALID_FUNCTION */, 50 /* ERROR_NOT_SUPPORTED */, 87 /* ERROR_INVALID_PARAMETER */, 120 /* ERROR_CALL_NOT_IMPLEMENTED */ :
		return true
	}
	return false
}

// replaceSnapshotWith renames source over target with POSIX semantics. A
// rename refused because another program has one of the files open without
// allowing it to be replaced (access denied, a sharing violation: a reader,
// or antivirus scanning a file just written) is tried again, with the same
// bounded wait as reads (retryBudget). The retry changes nothing else: the
// rename either happens whole or not at all, and one still refused after
// the bound fails, closed, with that error, leaving both files as they were.
func replaceSnapshotWith(source, target string, set func(syscall.Handle, *snapshotRenameInfo, uint32) error, sleep func(time.Duration)) error {
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
	size := uint32(unsafe.Offsetof(info.FileName)) + uint32(len(to)*2)
	var openErr error
	err = retryReplacing(func() error {
		// DELETE is requested only on the already closed staging file.
		// Opening its reparse point rather than a target preserves rename's
		// entry semantics. The handle is closed before any retry.
		handle, err := syscall.CreateFile(&from[0], snapshotDeleteAccess|syscall.SYNCHRONIZE,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
			nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			openErr = err
			return err
		}
		openErr = nil
		defer syscall.CloseHandle(handle)
		return set(handle, info, size)
	}, heldByAnother, sleep)
	if err != nil && openErr != nil {
		// Name the step: another program holding the new staging file is a
		// different condition from one holding the destination.
		return &os.LinkError{Op: "open staging file for rename", Old: source, New: target, Err: err}
	}
	if err != nil {
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
