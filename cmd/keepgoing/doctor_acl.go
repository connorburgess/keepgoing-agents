package main

type aclMetadata struct {
	Supported bool
	Access    string
	Default   string
	Format    string
}

const (
	aclPresent     = "present"
	aclNotReported = "not_reported"
	aclUnavailable = "unavailable"
	aclLinuxPOSIX  = "linux_posix"
	aclMacOS       = "macos_extended"
)

// doctorACL reports metadata presence, not effective permissions. It never
// displays ACL contents, named principals, attribute values, or raw errors.
func doctorACL(r *doctorReport, id, path string, directory bool, inspect func(string, bool) aclMetadata) {
	a := inspect(path, directory)
	if !a.Supported || (a.Format != aclLinuxPOSIX && a.Format != aclMacOS) {
		r.add(id, "unavailable", "ACL metadata inspection is implemented only for Linux POSIX and macOS extended ACLs; this platform's ACL privacy was not checked.", "Review ACLs using the platform's native tools; doctor never changes them.")
		return
	}
	status := "unavailable"
	message := "Linux POSIX access ACL: " + aclPresenceLabel(a.Access) + "."
	if a.Format == aclMacOS {
		message = "macOS extended ACL: " + aclPresenceLabel(a.Access) + "."
		if a.Access == aclNotReported {
			message = "macOS extended ACL: not reported (absent or unsupported)."
		}
	}
	defaultACL := directory && a.Format == aclLinuxPOSIX
	if defaultACL {
		message += " Default/inherited ACL: " + aclPresenceLabel(a.Default) + "."
	}
	if a.Access == aclPresent || (defaultACL && a.Default == aclPresent) {
		status = "warning"
		message += " ACL metadata is present; review it alongside Unix mode bits. Presence alone does not prove that another user has access."
	} else {
		message += " No ACL metadata was established; absent, hidden, or unsupported attributes do not establish privacy."
	}
	r.add(id, status, message, "Inspect access and inheritance rules in the supervisor's filesystem context. This is not an ACL permission evaluation or a check of ancestor directories; no ACL values or principals are displayed.")
}

func aclPresenceLabel(value string) string {
	switch value {
	case aclPresent:
		return "present"
	case aclNotReported:
		return "not reported (absent or hidden)"
	default:
		return "unavailable"
	}
}
