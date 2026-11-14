package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestDoctorACLReportsPresenceWithoutClaimingPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		metadata  aclMetadata
		directory bool
		want      string
	}{
		{"unsupported", aclMetadata{}, true, "unavailable"},
		{"not_reported", aclMetadata{true, aclNotReported, aclNotReported, aclLinuxPOSIX}, true, "unavailable"},
		{"unknown", aclMetadata{true, aclUnavailable, aclUnavailable, aclLinuxPOSIX}, true, "unavailable"},
		{"access", aclMetadata{true, aclPresent, aclNotReported, aclLinuxPOSIX}, true, "warning"},
		{"default", aclMetadata{true, aclNotReported, aclPresent, aclLinuxPOSIX}, true, "warning"},
		{"partial", aclMetadata{true, aclPresent, aclUnavailable, aclLinuxPOSIX}, true, "warning"},
		{"file", aclMetadata{true, aclPresent, "", aclLinuxPOSIX}, false, "warning"},
		{"private_error", aclMetadata{true, doctorPrivate, doctorPrivate, aclLinuxPOSIX}, true, "unavailable"},
		{"macos_present", aclMetadata{true, aclPresent, "", aclMacOS}, true, "warning"},
		{"macos_not_reported", aclMetadata{true, aclNotReported, "", aclMacOS}, true, "unavailable"},
		{"macos_unknown", aclMetadata{true, aclUnavailable, "", aclMacOS}, true, "unavailable"},
		{"unknown_format", aclMetadata{true, aclPresent, "", doctorPrivate}, true, "unavailable"},
		{"windows_present", aclMetadata{true, aclPresent, "", aclWindows}, true, "warning"},
		{"windows_empty", aclMetadata{true, aclEmpty, "", aclWindows}, true, "warning"},
		{"windows_unrestricted", aclMetadata{true, aclUnrestricted, "", aclWindows}, true, "error"},
		{"windows_unknown", aclMetadata{true, doctorPrivate, "", aclWindows}, true, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r doctorReport
			doctorACL(&r, "example.acl", doctorPrivate, tc.directory, func(p string, dir bool) aclMetadata {
				if p != doctorPrivate || dir != tc.directory {
					t.Fatal("wrong query target")
				}
				return tc.metadata
			})
			c := doctorCheckByID(t, r, "example.acl")
			b, _ := json.Marshal(r)
			if c.Status != tc.want || strings.Contains(string(b), doctorPrivate) {
				t.Fatalf("%+v", c)
			}
			if !tc.directory && strings.Contains(c.Message, "Default/inherited") {
				t.Fatal("file reported directory inheritance")
			}
			if tc.metadata.Format == aclMacOS && (strings.Contains(c.Message, "Linux") || strings.Contains(c.Message, "Default/inherited")) {
				t.Fatal("macOS reported Linux ACL semantics")
			}
			if tc.metadata.Format == aclWindows && (strings.Contains(c.Message, "Linux") || strings.Contains(c.Message, "Unix mode") || strings.Contains(c.Message, "Default/inherited")) {
				t.Fatal("Windows reported Unix ACL semantics")
			}
		})
	}
}

func TestDoctorACLWorksOfflineWithoutChangingHeldState(t *testing.T) {
	f := newDoctorFixture(t)
	if err := control.Save(f.state, control.File{Enabled: true, Paused: true, Stop: true, Completed: true, Note: doctorPrivate}); err != nil {
		t.Fatal(err)
	}
	before := f.stateFiles(t)
	_, r, _ := f.run(t, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		t.Fatal("offline ACL inspection executed a command")
		return nil, nil
	}, "--offline")
	for _, id := range []string{"config.permissions.acl", "state.directory.acl", "state.control.json.acl"} {
		doctorCheckByID(t, r, id)
	}
	after := f.stateFiles(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("ACL inspection modified runtime state")
	}
}
