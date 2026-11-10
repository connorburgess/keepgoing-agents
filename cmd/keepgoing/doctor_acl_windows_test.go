//go:build windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestWindowsACLDescriptorsDistinguishNullEmptyAndEntries(t *testing.T) {
	convert := aclAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	if err := convert.Find(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, sddl, want string }{
		{"empty", "D:", aclEmpty},
		{"entries", "D:(A;;GR;;;WD)", aclPresent},
		{"absent", "O:WD", aclUnrestricted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := syscall.UTF16PtrFromString(tc.sddl)
			if err != nil {
				t.Fatal(err)
			}
			var sd unsafe.Pointer
			ok, _, err := convert.Call(uintptr(unsafe.Pointer(p)), 1, uintptr(unsafe.Pointer(&sd)), 0)
			runtime.KeepAlive(p)
			if ok == 0 || sd == nil {
				t.Fatalf("fixture conversion failed: %v", err)
			}
			defer syscall.LocalFree(syscall.Handle(sd))
			if got := windowsDACLMetadata(sd); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
	// Construct a NULL DACL in memory only. No filesystem permissions change.
	init := aclAdvapi32.NewProc("InitializeSecurityDescriptor")
	set := aclAdvapi32.NewProc("SetSecurityDescriptorDacl")
	var sd [40]byte // Large enough for the absolute descriptor on 32/64-bit Windows.
	p := unsafe.Pointer(&sd[0])
	if ok, _, err := init.Call(uintptr(p), 1); ok == 0 {
		t.Fatal(err)
	}
	if ok, _, err := set.Call(uintptr(p), 1, 0, 0); ok == 0 {
		t.Fatal(err)
	}
	if got := windowsDACLMetadata(p); got != aclUnrestricted {
		t.Fatalf("NULL DACL: %s", got)
	}
	runtime.KeepAlive(sd)
	if windowsDACLMetadata(nil) != aclUnavailable {
		t.Fatal("nil descriptor was accepted")
	}
}

func TestWindowsACLReadsNativeTemporaryFilesWithoutMutation(t *testing.T) {
	for _, directory := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "example-fixture")
		if directory {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte("example content"), 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		a := doctorACLMetadata(path, directory)
		if !a.Supported || a.Format != aclWindows || a.Access != aclPresent {
			t.Fatalf("native fixture DACL not read: %+v", a)
		}
		var r doctorReport
		doctorFile(&r, "example", path, directory)
		if doctorCheckByID(t, r, "example.acl").Status != "warning" {
			t.Fatalf("%+v", r)
		}
		after, err := os.Stat(path)
		if err != nil || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || doctorACLMetadata(path, directory) != a {
			t.Fatal("inspection changed file metadata")
		}
		if directory {
			entries, err := os.ReadDir(path)
			if err != nil || len(entries) != 0 {
				t.Fatal("inspection created runtime files")
			}
		} else {
			b, err := os.ReadFile(path)
			if err != nil || string(b) != "example content" {
				t.Fatal("inspection changed file contents")
			}
		}
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), "invalid\x00path"} {
		if doctorACLMetadata(path, false).Access != aclUnavailable {
			t.Fatal("failed native query did not stay unavailable")
		}
	}
}
