package main

type aclMetadata struct {
	Supported bool
	Access    string
	Default   string
	Format    string
}

const (
	aclPresent      = "present"
	aclNotReported  = "not_reported"
	aclUnavailable  = "unavailable"
	aclLinuxPOSIX   = "linux_posix"
	aclMacOS        = "macos_extended"
	aclWindows      = "windows_dacl"
	aclEmpty        = "empty"
	aclUnrestricted = "unrestricted"
)

// doctorACL reports metadata presence, not effective permissions. It never
// displays ACL contents, named principals, attribute values, or raw errors.
func doctorACL(r *doctorReport, id, path string, directory bool, inspect func(string, bool) aclMetadata) {
	a := inspect(path, directory)
	if a.Supported && a.Format == aclWindows {
		doctorWindowsACLResult(r, id, a.Access)
		return
	}
	if !a.Supported || (a.Format != aclLinuxPOSIX && a.Format != aclMacOS) {
		r.add(id, "unavailable", "ACL metadata inspection is implemented only for Linux, macOS, and Windows; this platform's ACL privacy was not checked.", "Review ACLs using the platform's native tools; doctor never changes them.")
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

func doctorWindowsACLResult(r *doctorReport, id, access string) {
	status, message := "unavailable", "Windows DACL metadata could not be established; raw errors and security descriptor contents are withheld."
	switch access {
	case aclUnrestricted:
		status, message = "error", "Windows DACL is null or absent, allowing unrestricted access through the DACL. Other security mechanisms were not evaluated."
	case aclEmpty:
		status, message = "warning", "Windows DACL is empty and grants no access through its entries. Review whether the supervisor can operate; this is not proof of privacy or effective access."
	case aclPresent:
		status, message = "warning", "Windows DACL contains entries. Their allow/deny rules and inheritance were not evaluated; presence does not establish privacy."
	}
	r.add(id, status, message, "Review the DACL in the supervisor's execution context. Doctor never changes permissions or displays ACEs, SIDs, account names, or security descriptor contents.")
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
