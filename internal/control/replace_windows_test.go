package control

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

func TestWindowsControlSnapshotRenameFailurePreservesFiles(t *testing.T) {
	for _, code := range []syscall.Errno{50 /* not supported */, 87 /* invalid parameter */, syscall.ERROR_ACCESS_DENIED, 32 /* sharing violation */} {
		t.Run(code.Error(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "café-项目")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			source, target := filepath.Join(dir, "staging"), filepath.Join(dir, FileName)
			if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			err := replaceSnapshotWith(source, target, func(handle syscall.Handle, info *snapshotRenameInfo, size uint32) error {
				calls++
				if handle == syscall.InvalidHandle || info.Flags != snapshotReplaceExisting|snapshotPOSIXSemantics || info.RootDirectory != 0 {
					t.Fatal("unexpected rename authority")
				}
				if info.FileNameLength%2 != 0 || syscall.UTF16ToString(info.FileName[:info.FileNameLength/2]) != target {
					t.Fatal("incorrect target encoding")
				}
				if size != uint32(unsafe.Offsetof(info.FileName))+info.FileNameLength+2 {
					t.Fatal("incorrect rename buffer size")
				}
				return code
			})
			if !errors.Is(err, code) || calls != 1 {
				t.Fatalf("rename error/calls: %v / %d", err, calls)
			}
			for path, want := range map[string]string{source: "new", target: "old"} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("failure changed %s: %q %v", filepath.Base(path), got, err)
				}
			}
			// An explicit reader without DELETE sharing proves the staging handle
			// no longer retains DELETE access, regardless of future os.Open changes.
			name, err := syscall.UTF16PtrFromString(source)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatalf("staging handle not released: %v", err)
			}
			if err := syscall.CloseHandle(handle); err != nil {
				t.Fatal(err)
			}
		})
	}
}
