package snapshot

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
			source, target := filepath.Join(dir, "staging"), filepath.Join(dir, "control.json")
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

// Replace falls back to the ordinary rename only when the system reports that
// the POSIX-semantics rename is unsupported, classified by error code. A
// blocked or denied replacement must fail and leave both files as they were.
func TestWindowsControlSnapshotReplaceFallsBackOnlyWhenUnsupported(t *testing.T) {
	for _, tc := range []struct {
		code     syscall.Errno
		fallback bool
	}{
		{1 /* invalid function */, true},
		{50 /* not supported */, true},
		{87 /* invalid parameter */, true},
		{120 /* call not implemented */, true},
		{syscall.ERROR_ACCESS_DENIED, false},
		{32 /* sharing violation */, false},
		{2 /* file not found */, false},
	} {
		t.Run(tc.code.Error(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "café-项目")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			source, target := filepath.Join(dir, "staging"), filepath.Join(dir, "state.json")
			if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			err := replaceWithFallback(source, target, func(syscall.Handle, *snapshotRenameInfo, uint32) error {
				calls++
				return tc.code
			})
			if calls != 1 {
				t.Fatalf("rename attempts: %d", calls)
			}
			got, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.fallback {
				if err != nil || string(got) != "new" {
					t.Fatalf("unsupported rename did not fall back: %q %v", got, err)
				}
				if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
					t.Fatal("staging file left behind")
				}
				return
			}
			if !errors.Is(err, tc.code) || string(got) != "old" {
				t.Fatalf("failure was retried or changed the target: %q %v", got, err)
			}
			if staged, _ := os.ReadFile(source); string(staged) != "new" {
				t.Fatal("failure changed the staging file")
			}
		})
	}
	// The strict form used for control and goal files never falls back.
	dir := t.TempDir()
	source, target := filepath.Join(dir, "staging"), filepath.Join(dir, "control.json")
	for path, content := range map[string]string{source: "new", target: "old"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := replaceSnapshotWith(source, target, func(syscall.Handle, *snapshotRenameInfo, uint32) error { return syscall.Errno(50) })
	if got, _ := os.ReadFile(target); !errors.Is(err, syscall.Errno(50)) || string(got) != "old" {
		t.Fatalf("strict replacement fell back: %q %v", got, err)
	}
}
