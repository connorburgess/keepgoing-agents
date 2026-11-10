//go:build darwin

package main

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinACLHeaderRejectsMalformedLayouts(t *testing.T) {
	for _, tc := range []struct {
		name                string
		total, offset, size uint32
		want                string
	}{
		{"not_reported", 12, 8, 0, aclNotReported},
		{"present", 80, 8, 68, aclPresent},
		{"huge_payload_not_loaded", 1<<30 + 12, 8, 1 << 30, aclPresent},
		{"short_total", 8, 8, 0, aclUnavailable},
		{"wrong_offset", 80, 12, 68, aclUnavailable},
		{"negative_offset", 80, 0xffffffff, 68, aclUnavailable},
		{"inconsistent_length", 12, 8, 68, aclUnavailable},
		{"overflow", 11, 8, 0xffffffff, aclUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b [12]byte
			binary.LittleEndian.PutUint32(b[:4], tc.total)
			binary.LittleEndian.PutUint32(b[4:8], tc.offset)
			binary.LittleEndian.PutUint32(b[8:], tc.size)
			if got := darwinACLHeader(b[:]); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
	for _, b := range [][]byte{nil, make([]byte, 11), make([]byte, 13)} {
		if darwinACLHeader(b) != aclUnavailable {
			t.Fatal("accepted incomplete or unexpected header")
		}
	}
}

func TestDarwinACLMetadataDetectsACLHiddenByModeBits(t *testing.T) {
	for _, directory := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "fixture")
		mode := os.FileMode(0o600)
		if directory {
			mode = 0o700
			if err := os.Mkdir(path, mode); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte("example fixture"), mode); err != nil {
			t.Fatal(err)
		}
		chmod := func(args ...string) {
			t.Helper()
			if err := exec.Command("/bin/chmod", append(args, path)...).Run(); err != nil {
				t.Fatalf("fixture chmod: %v", err)
			}
		}
		chmod("-N")
		if a := doctorACLMetadata(path, directory); a.Access != aclNotReported || a.Format != aclMacOS {
			t.Fatalf("unexpected initial metadata: %+v", a)
		}
		chmod("+a", "everyone allow read")
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("fixture did not preserve owner-only mode bits")
		}
		var r doctorReport
		doctorFile(&r, "fixture", path, directory)
		if doctorCheckByID(t, r, "fixture").Status != "ok" || doctorCheckByID(t, r, "fixture.acl").Status != "warning" {
			t.Fatalf("ACL hidden by mode bits was missed: %+v", r)
		}
		after, err := os.Stat(path)
		if err != nil || after.Mode() != info.Mode() || !after.ModTime().Equal(info.ModTime()) || doctorACLMetadata(path, directory).Access != aclPresent {
			t.Fatal("inspection changed mode, modification time, or ACL presence")
		}
		if !directory {
			b, err := os.ReadFile(path)
			if err != nil || string(b) != "example fixture" {
				t.Fatal("inspection changed file contents")
			}
		}
		link := path + "-link"
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if doctorACLMetadata(link, directory).Access != aclPresent {
			t.Fatal("query did not follow configured symlink target")
		}
		chmod("-N")
		if doctorACLMetadata(path, directory).Access != aclNotReported {
			t.Fatal("removed ACL still reported present")
		}
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), "invalid\x00path"} {
		if doctorACLMetadata(path, false).Access != aclUnavailable {
			t.Fatal("failed query did not stay unavailable")
		}
	}
}
