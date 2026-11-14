package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Open opens path read-only. The handle allows read/write/delete sharing, so
// holding it does not block Replace. It remains the old snapshot afterwards.
//
// While another process is replacing path, Windows can briefly refuse to open
// it with an access-denied or sharing error. Only those two codes are retried,
// a few times over about thirty milliseconds; a lasting denial is returned
// after that short delay, and any other error at once.
func Open(path string) (*os.File, error) {
	return openShared(path, "open", syscall.GENERIC_READ, syscall.FILE_ATTRIBUTE_NORMAL, heldByAnother)
}

// OpenRecord is Open for a record replaced by rename whose absence a reader
// would act on (see the package comment): it also retries "not found", for
// up to retryBudget, because Windows reports the existing name missing for a
// moment while it is replaced.
func OpenRecord(path string) (*os.File, error) {
	return openShared(path, "open", syscall.GENERIC_READ, syscall.FILE_ATTRIBUTE_NORMAL, replacementInProgress)
}

// fileReadAttributes is FILE_READ_ATTRIBUTES: enough to describe a file
// without being allowed to read it.
const fileReadAttributes = 0x80

// Lstat describes path, without following a link, as os.Lstat does. On
// Windows it does not use os.Lstat: when GetFileAttributesEx fails, Go 1.24
// falls back to CreateFile with no sharing, which during a replacement both
// reports an existing record missing and, while that handle is open, makes
// the replacing save fail with access denied. Lstat opens the path itself,
// sharing it fully and asking only to read its attributes, retries as Open
// does, and describes the open handle.
func Lstat(path string) (os.FileInfo, error) { return lstatWith(path, heldByAnother) }

// LstatRecord is Lstat with OpenRecord's retry of "not found".
func LstatRecord(path string) (os.FileInfo, error) { return lstatWith(path, replacementInProgress) }

func lstatWith(path string, retry func(error) bool) (os.FileInfo, error) {
	f, err := openShared(path, "lstat", fileReadAttributes, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, retry)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

// openShared opens path with the given access and flags, allowing others to
// read, write and delete or replace it meanwhile, and retrying the answers a
// replacement in progress gives. No create, truncate, privilege adjustment
// or inheritable handle; DELETE sharing permits replacement and does not
// grant delete access.
func openShared(path, op string, access, flags uint32, retry func(error) bool) (*os.File, error) {
	name, err := snapshotWindowsPath(path)
	if err != nil {
		return nil, &os.PathError{Op: op, Path: path, Err: err}
	}
	handle, err := openWithRetry(func() (syscall.Handle, error) {
		return syscall.CreateFile(&name[0], access,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
			nil, syscall.OPEN_EXISTING, flags, 0)
	}, retry, time.Sleep)
	if err != nil {
		return nil, &os.PathError{Op: op, Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, &os.PathError{Op: op, Path: path, Err: syscall.EINVAL}
	}
	return file, nil
}

// retryBudget bounds the waiting for a replacement to finish: retries stop
// once this much time has been slept. A loaded hosted runner was seen to
// report a record missing for longer than the earlier five tries (about
// 30 ms). It is also what a record that really is missing costs a reader.
const retryBudget = 250 * time.Millisecond

// maxRetryDelay caps one wait between tries.
const maxRetryDelay = 25 * time.Millisecond

func openWithRetry(open func() (syscall.Handle, error), retry func(error) bool, sleep func(time.Duration)) (syscall.Handle, error) {
	var handle syscall.Handle
	err := retryReplacing(func() error {
		var err error
		handle, err = open()
		return err
	}, retry, sleep)
	return handle, err
}

// retryReplacing calls try until it succeeds, fails in a way retry does not
// accept, or retryBudget has been slept, and returns its last error.
func retryReplacing(try func() error, retry func(error) bool, sleep func(time.Duration)) error {
	delay := 2 * time.Millisecond
	var slept time.Duration
	for {
		err := try()
		if err == nil || !retry(err) || slept >= retryBudget {
			return err
		}
		sleep(delay)
		slept += delay
		delay = min(delay*2, maxRetryDelay)
	}
}

// heldByAnother reports access denied or a sharing violation: what a rename
// gets while another program, such as a reader or antivirus, has the file
// open without allowing it to be replaced.
func heldByAnother(err error) bool {
	var code syscall.Errno
	return errors.As(err, &code) && (code == syscall.ERROR_ACCESS_DENIED || code == 32 /* ERROR_SHARING_VIOLATION */)
}

// replacementInProgress reports, by Windows error code and never by text,
// the answers Windows can give a reader that arrives while another process
// replaces the file: access denied, a sharing violation, or the file not
// found at all. A record a reader finds missing
// is otherwise believed absent and read as new, so "not found" is retried
// the same bounded way before it is believed (CI saw existing state.json and
// control.json reported missing during their replacement). A missing directory is not: no
// replacement makes the directory vanish.
func replacementInProgress(err error) bool {
	var code syscall.Errno
	return errors.As(err, &code) && (code == syscall.ERROR_ACCESS_DENIED || code == 32 /* ERROR_SHARING_VIOLATION */ || code == syscall.ERROR_FILE_NOT_FOUND)
}

func snapshotWindowsPath(path string) ([]uint16, error) {
	// CreateFile does not apply os.Open's extended-path conversion. Preserve
	// support for long local/UNC paths, including relative and non-ASCII inputs.
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if len(absolute) >= 248 && !strings.HasPrefix(absolute, `\\?\`) && !strings.HasPrefix(absolute, `\\.\`) {
		if strings.HasPrefix(absolute, `\\`) {
			absolute = `\\?\UNC\` + absolute[2:]
		} else {
			absolute = `\\?\` + absolute
		}
	}
	return syscall.UTF16FromString(absolute)
}
