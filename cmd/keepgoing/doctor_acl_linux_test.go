//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestLinuxACLMetadataQueriesOnlyKnownSizes(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		err  error
		want string
	}{
		{"present", 44, nil, aclPresent},
		{"huge", 1 << 30, nil, aclPresent},
		{"empty", 0, nil, aclUnavailable},
		{"negative", -1, nil, aclUnavailable},
		{"missing_or_hidden", -1, syscall.ENODATA, aclNotReported},
		{"unsupported", -1, syscall.ENOTSUP, aclUnavailable},
		{"denied", -1, syscall.EACCES, aclUnavailable},
		{"unknown", -1, errors.New(doctorPrivate), aclUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, directory := range []bool{false, true} {
				var names []string
				a := linuxACLMetadata("example-path", directory, func(p, name string) (int, error) {
					if p != "example-path" {
						t.Fatal("wrong path")
					}
					names = append(names, name)
					return tc.n, tc.err
				})
				wantNames := []string{"system.posix_acl_access"}
				if directory {
					wantNames = append(wantNames, "system.posix_acl_default")
				}
				if !a.Supported || a.Access != tc.want || (directory && a.Default != tc.want) || !reflect.DeepEqual(names, wantNames) {
					t.Fatalf("%+v %v", a, names)
				}
			}
		})
	}
}

func TestLinuxACLMetadataDetectsDefaultACLHiddenByModeBits(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Linux POSIX ACL xattr v2: owner rwx, group read, other none.
	// A default ACL does not change this directory's own mode bits.
	acl := []byte{2, 0, 0, 0}
	for _, entry := range []struct{ tag, perm uint16 }{{1, 7}, {4, 4}, {32, 0}} {
		acl = binary.LittleEndian.AppendUint16(acl, entry.tag)
		acl = binary.LittleEndian.AppendUint16(acl, entry.perm)
		acl = binary.LittleEndian.AppendUint32(acl, 0xffffffff)
	}
	if err := syscall.Setxattr(dir, "system.posix_acl_default", acl, 0); err != nil {
		if errors.Is(err, syscall.ENOTSUP) {
			t.Skip("test filesystem does not support POSIX ACLs")
		}
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o700 {
		t.Fatal("fixture changed directory mode bits")
	}
	var r doctorReport
	doctorACL(&r, "example.acl", dir, true, doctorACLMetadata)
	if doctorCheckByID(t, r, "example.acl").Status != "warning" {
		t.Fatalf("default ACL was missed: %+v", r)
	}
	// Confirm inspection leaves the actual metadata and directory unchanged.
	buf := make([]byte, len(acl))
	n, err := syscall.Getxattr(dir, "system.posix_acl_default", buf)
	if err != nil || n != len(acl) || !reflect.DeepEqual(buf, acl) {
		t.Fatal("inspection changed ACL")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("inspection created files")
	}
	if err := syscall.Removexattr(dir, "system.posix_acl_default"); err != nil {
		t.Fatal(err)
	}
	if a := doctorACLMetadata(dir, true); a.Default != aclNotReported {
		t.Fatalf("removed ACL still present: %+v", a)
	}
	file := filepath.Join(dir, "example-file")
	if err := os.WriteFile(file, []byte("example"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a := doctorACLMetadata(file, false); a.Access != aclNotReported || a.Default != "" {
		t.Fatalf("unexpected file metadata: %+v", a)
	}
}
