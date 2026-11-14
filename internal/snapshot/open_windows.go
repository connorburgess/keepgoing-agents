package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Open opens path read-only. The handle allows read/write/delete sharing, so
// holding it does not block Replace. It remains the old snapshot afterwards.
//
// While another process is replacing path, Windows can briefly refuse to open
// it with access denied or a sharing violation. Those two answers are retried
// until retryBudget has been slept; a lasting refusal is therefore returned
// after about 255 ms, and any other error, "not found" included, at once.
func Open(path string) (*os.File, error) {
	return openShared(path, "open", syscall.GENERIC_READ, syscall.FILE_ATTRIBUTE_NORMAL, heldByAnother)
}

// OpenRecord is Open for a record whose absence a reader would act on (see
// the package comment). While a record is replaced by rename, Windows reports
// the existing name missing for a moment. On "not found" OpenRecord looks at
// the record's marker, which Replace and ReplaceStrict create before a
// record's first rename and never replace: with no marker the record has
// never been written and "not found" is returned at once; with one, the
// record has been published or its first save is incomplete. "Not found" is
// retried as Open retries a refusal; an exhausted marked read fails unavailable,
// never missing. Choose this call whenever a false absence could permit an
// action, hide a hold, or reset a cap (see the package comment).
func OpenRecord(path string) (*os.File, error) {
	retry, marked := recordRetryState(path)
	f, err := openShared(path, "open", syscall.GENERIC_READ, syscall.FILE_ATTRIBUTE_NORMAL, retry)
	return f, recordReadError(path, err, *marked)
}

// fileReadAttributes is FILE_READ_ATTRIBUTES: enough to describe a file
// without being allowed to read it.
const fileReadAttributes = 0x80

// Lstat describes path, without following a link, as os.Lstat does, from a
// handle it opens itself, sharing the file fully and asking only to read its
// attributes, and retries a refusal as Open does. It does not use os.Lstat:
// when GetFileAttributesEx fails, Go 1.24 falls back to a CreateFile that
// shares nothing. A lookup through that fallback was seen to report an
// existing record missing during replacement. The shared handle keeps our
// metadata and content reads on the same sharing policy; this is not a claim
// that the attribute-only fallback handle blocks POSIX replacement.
func Lstat(path string) (os.FileInfo, error) { return lstatWith(path, heldByAnother) }

// LstatRecord is Lstat with OpenRecord's handling of "not found".
func LstatRecord(path string) (os.FileInfo, error) {
	retry, marked := recordRetryState(path)
	info, err := lstatWith(path, retry)
	return info, recordReadError(path, err, *marked)
}

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
// 30 ms). A never-written or deliberately removed record has no marker
// and is returned missing immediately.
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

// markerSuffix names a record's existence marker: a zero-byte file beside it,
// created before the record's first rename and never replaced, so it never
// has the moment in which a replaced name is reported missing.
const markerSuffix = ".present"

func markerFor(path string) string { return path + markerSuffix }

// ensureMarker creates target's marker before publication. Failure refuses
// the save: publishing without the marker could hide a later replacement.
func ensureMarker(target string) error {
	m := markerFor(target)
	f, err := os.OpenFile(m, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		return f.Close()
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(m)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != 0 {
		return fmt.Errorf("invalid record marker: %s", m)
	}
	return nil
}

// holdMarker denies deletion until publication (including fallback) finishes.
// Request data read access: an attribute-only handle does not enforce the
// deletion exclusion this guard needs (covered by the transition test).
// Remove requests DELETE access on the same marker before touching the record,
// so a concurrent delete cannot take away a publishing writer's marker.
func holdMarker(target string) (*os.File, error) {
	if err := ensureMarker(target); err != nil {
		return nil, err
	}
	path := markerFor(target)
	name, err := snapshotWindowsPath(path)
	if err != nil {
		return nil, err
	}
	handle, err := openWithRetry(func() (syscall.Handle, error) {
		return syscall.CreateFile(&name[0], syscall.GENERIC_READ,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING,
			syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	}, heldByAnother, time.Sleep)
	if err != nil {
		return nil, &os.PathError{Op: "hold record marker", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(handle), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		f.Close()
		return nil, fmt.Errorf("invalid record marker: %s", path)
	}
	return f, nil
}

// Remove deletes a record and then the exact marker it acquired. Saves and
// other removers cannot acquire a conflicting marker handle meanwhile. A
// marker replaced while this call waited is refused before touching a record.
func Remove(path string) error { return removeRecordWith(path, os.Remove) }

func removeRecordWith(path string, remove func(string) error) error {
	return removeRecordObserved(path, remove, nil)
}

func removeRecordObserved(path string, remove func(string) error, beforeGuard func()) error {
	before, beforeErr := Lstat(path)
	if beforeErr != nil && !errors.Is(beforeErr, os.ErrNotExist) {
		return beforeErr
	}
	// Guard legacy/unwritten records too: an absent marker must not let a
	// deletion cross a concurrent first publication.
	if err := ensureMarker(path); err != nil {
		return err
	}
	markerPath := markerFor(path)
	expected, err := Lstat(markerPath)
	if err != nil {
		return err
	}
	if !expected.Mode().IsRegular() || expected.Size() != 0 {
		return fmt.Errorf("invalid record marker: %s", markerPath)
	}
	if beforeGuard != nil {
		beforeGuard()
	}
	name, err := snapshotWindowsPath(markerPath)
	if err != nil {
		return err
	}
	handle, err := openWithRetry(func() (syscall.Handle, error) {
		return syscall.CreateFile(&name[0], syscall.GENERIC_READ|snapshotDeleteAccess,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING,
			syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	}, heldByAnother, time.Sleep)
	if err != nil {
		return &os.PathError{Op: "hold marker for removal", Path: markerPath, Err: err}
	}
	marker := os.NewFile(uintptr(handle), markerPath)
	defer marker.Close()
	info, err := marker.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != 0 || !os.SameFile(expected, info) {
		return fmt.Errorf("record marker changed or invalid: %s", markerPath)
	}
	// Refuse a save that finished while deletion waited for the marker.
	// This includes a first publication that was absent at our observation.
	current, currentErr := Lstat(path)
	if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
		return currentErr
	}
	beforeMissing, currentMissing := errors.Is(beforeErr, os.ErrNotExist), errors.Is(currentErr, os.ErrNotExist)
	if beforeMissing != currentMissing || (!beforeMissing && !os.SameFile(before, current)) {
		return fmt.Errorf("record changed before removal: %s", path)
	}
	err = remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Deleting by this handle, not by name, cannot unlink a newer marker.
	// FileDispositionInfo marks this object for deletion when our guard closes.
	disposition := byte(1)
	result, _, deleteErr := setSnapshotInformation.Call(uintptr(handle), 4, uintptr(unsafe.Pointer(&disposition)), 1)
	if result == 0 {
		if deleteErr == syscall.Errno(0) {
			deleteErr = syscall.EINVAL
		}
		return &os.PathError{Op: "remove record marker", Path: markerPath, Err: deleteErr}
	}
	if closeErr := marker.Close(); closeErr != nil {
		return closeErr
	}
	return err
}

// A marked record still absent after the budget is unavailable, not empty.
// Do not wrap the not-found error: record loaders use errors.Is to create a
// new record, which could reset a cap or permit a duplicate send.
func recordReadError(path string, err error, marked bool) error {
	if errors.Is(err, os.ErrNotExist) && marked {
		return &os.PathError{Op: "read record", Path: path, Err: errors.New("record unavailable after replacement wait")}
	}
	return err
}

// recordRetry is the retry rule of the Record reads of path: a refusal, as
// for Open, and "not found" only while the record's marker exists, by Windows
// error code and never by text. During a POSIX-semantics replacement Windows
// reports the existing name missing for a moment, even to a reader that
// shares everything (CI saw state.json and control.json read as missing,
// once for longer than 30 ms); a missing directory is never retried. The
// marker is looked at once, on the first "not found".
func recordRetry(path string) func(error) bool {
	retry, _ := recordRetryState(path)
	return retry
}

// Preserve the marker observation made by the retry rule. Looking again
// after a no-marker miss could see a first publisher's new marker and turn
// a legitimate initial absence into an unavailable error without waiting.
func recordRetryState(path string) (func(error) bool, *bool) {
	checked, present := false, false
	return func(err error) bool {
		if heldByAnother(err) {
			return true
		}
		var code syscall.Errno
		if !errors.As(err, &code) || code != syscall.ERROR_FILE_NOT_FOUND {
			return false
		}
		if !checked {
			_, merr := os.Lstat(markerFor(path))
			checked, present = true, !errors.Is(merr, os.ErrNotExist)
		}
		return present
	}, &present
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
