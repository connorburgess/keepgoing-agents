package main

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

func TestWindowsAccessPrivilegeGuard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     uint64
		attrs  uint32
		denied bool
	}{
		{"backup_enabled", 10, 2, true},
		{"restore_enabled", 20, 3, true},
		{"backup_disabled", 10, 1, false},
		{"unrelated_enabled", 30, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := make([]byte, 16)
			binary.LittleEndian.PutUint32(b, 1)
			binary.LittleEndian.PutUint64(b[4:], tc.id)
			binary.LittleEndian.PutUint32(b[12:], tc.attrs)
			err := windowsDirectoryPrivileges(b, [2]uint64{10, 20})
			if (err != nil) != tc.denied {
				t.Fatalf("unexpected privilege result: %v", err)
			}
		})
	}
	for _, b := range [][]byte{nil, {1, 0, 0, 0}, {255, 255, 255, 255}, make([]byte, 5)} {
		if windowsDirectoryPrivileges(b, [2]uint64{10, 20}) != errAccessUnavailable {
			t.Fatal("malformed token accepted")
		}
	}
	if _, _, err := windowsAccessRequest(accessSearch); err != errAccessUnavailable {
		t.Fatal("unsupported access mode accepted")
	}
}

func TestWindowsAccessNativeDirectoryAndMissingState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "example-state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := doctorOSAccess(dir, accessWrite|accessSearch); err != nil {
		t.Fatalf("native directory check: %v", err)
	}
	var r doctorReport
	doctorStateAccess(&r, filepath.Join(dir, "missing", "state"), doctorOSAccess)
	if doctorCheckByID(t, r, "state.write_access").Status != "ok" {
		t.Fatalf("missing ancestor check: %+v", r)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("check created runtime state")
	}
	// Deny FILE_ADD_FILE on this temporary fixture only. Owner retains
	// permission to restore the DACL and remove the fixture after the test.
	windowsAccessFixtureDACL(t, dir, "D:P(D;;0x2;;;WD)(A;;FA;;;OW)")
	if err := doctorOSAccess(dir, accessWrite|accessSearch); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("directory denial was not detected: %v", err)
	}
}

func TestWindowsAccessNativeAppendAndLockRights(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example-history")
	const content = "example history\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	for _, mode := range []uint32{accessWrite, accessRead | accessWrite} {
		if err := doctorOSAccess(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	// Deny FILE_WRITE_DATA but allow append. A GENERIC_WRITE probe would
	// incorrectly reject a file that Go can open for history appends.
	windowsAccessFixtureDACL(t, path, "D:P(D;;0x2;;;WD)(A;;FA;;;OW)")
	if err := doctorOSAccess(path, accessWrite); err != nil {
		t.Fatalf("append-only access rejected: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("fixture does not permit actual Go append open: %v", err)
	}
	f.Close() // Do not write, even in the fixture.
	if err := doctorOSAccess(path, accessRead|accessWrite); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("lock write denial was not detected: %v", err)
	}
	after, _ := os.Stat(path)
	b, err := os.ReadFile(path)
	if err != nil || string(b) != content || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("check changed file contents or modification time")
	}
}

func TestWindowsAccessNativeReadonlySharingAndNoCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example-lock")
	if err := doctorOSAccess(path, accessRead|accessWrite); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file was created: %v", err)
	}
	if err := os.WriteFile(path, []byte("example"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	for _, mode := range []uint32{accessWrite, accessRead | accessWrite} {
		if err := doctorOSAccess(path, mode); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("readonly file accepted: %v", err)
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	p, _ := syscall.UTF16PtrFromString(path)
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = doctorOSAccess(path, accessRead|accessWrite)
	syscall.CloseHandle(h)
	var r doctorReport
	doctorAccessResult(&r, "example", err, "allowed")
	if err != syscall.Errno(32) || doctorCheckByID(t, r, "example").Status != "unavailable" {
		t.Fatalf("sharing violation misclassified: %v %+v", err, r)
	}
	if err := doctorOSAccess(path, accessRead|accessWrite); err != nil {
		t.Fatal(err)
	}
	// Exclusive open proves the diagnostic did not leak its handle.
	h, err = syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatalf("diagnostic retained an open handle: %v", err)
	}
	syscall.CloseHandle(h)
}

// Install a test DACL only on the temporary fixture, never a runtime path.
func windowsAccessFixtureDACL(t *testing.T, path, sddl string) {
	t.Helper()
	set := func(sddl string) {
		t.Helper()
		p, _ := syscall.UTF16PtrFromString(sddl)
		var sd unsafe.Pointer
		ok, _, err := accessAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW").Call(uintptr(unsafe.Pointer(p)), 1, uintptr(unsafe.Pointer(&sd)), 0)
		if ok == 0 {
			t.Fatal(err)
		}
		defer syscall.LocalFree(syscall.Handle(sd))
		name, _ := syscall.UTF16PtrFromString(path)
		ok, _, err = accessAdvapi32.NewProc("SetFileSecurityW").Call(uintptr(unsafe.Pointer(name)), 4|0x80000000, uintptr(sd))
		if ok == 0 {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { set("D:P(A;;FA;;;OW)") })
	set(sddl)
}
