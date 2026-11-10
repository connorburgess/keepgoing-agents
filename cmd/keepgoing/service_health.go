package main

import "time"

// Assess only existing structured observations. The loop's diagnostic responder
// is not bound to the OS service, and no result authorizes a restart or resume.
type serviceHealth struct {
	Status   string           `json:"status"` // ok, warning, unavailable, not_installed
	Findings []serviceFinding `json:"findings"`
}
type serviceFinding struct {
	Code    string `json:"code"`
	Status  string `json:"status"` // warning, unavailable
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

const serviceOverdueGrace = time.Minute

func assessServiceHealth(r serviceReport) serviceHealth {
	h := serviceHealth{Status: "ok", Findings: []serviceFinding{}}
	add := func(code, status, message, fix string) {
		h.Findings = append(h.Findings, serviceFinding{code, status, message, fix})
		if status == "warning" || h.Status == "ok" {
			h.Status = status
		}
	}
	if r.Registration == "absent" && !r.Recorded && !r.DefinitionPresent {
		h.Status = "not_installed"
		return h
	}
	if !r.Recorded && (r.Registration == "loaded" || r.DefinitionPresent) {
		add("record_missing", "warning", "No managed recovery record exists for this service identity.", "Inspect the exact service manually; keepgoing cannot safely manage an unrecorded service.")
	} else if r.Recorded && !r.DefinitionPresent {
		add("definition_missing", "warning", "The recorded service definition is missing; installation or removal may be incomplete.", "Inspect the private recovery record and retry service remove if finishing cleanup is intended.")
	}
	switch r.Registration {
	case "absent":
		add("registration_missing", "warning", "The service is recorded locally but the manager reports no registration.", "Inspect service setup; remove the recorded installation before explicitly reinstalling. Do not resume the loop to repair registration.")
	case "loaded":
		if r.Backend == "systemd" {
			switch r.ActiveState {
			case "failed":
				add("unit_failed", "warning", "The systemd unit reports a failed state.", "Inspect the exact user unit's failure and configuration before an explicit restart; no recovery action was taken.")
			case "inactive":
				add("unit_inactive", "warning", "The systemd unit is loaded but inactive.", "Check whether it was intentionally stopped; inspect setup before starting it explicitly.")
			case "activating", "deactivating", "reloading":
				add("unit_transitioning", "warning", "The systemd unit is transitioning; steady operation is not established.", "Check again after the transition; inspect the unit if it persists.")
			case "active":
			default:
				add("unit_state_unknown", "unavailable", "The systemd active state is unavailable.", "Inspect the exact user unit; do not infer that the supervisor is running.")
			}
			switch r.UnitFileState {
			case "disabled":
				add("unit_disabled", "warning", "The systemd unit is disabled for automatic startup; a running instance may still exist.", "Check whether this was intentional before explicitly enabling the service. Loop enable/resume does not enable the OS unit.")
			case "enabled":
			default:
				add("unit_enablement_unknown", "unavailable", "The systemd unit's automatic-start setting is unavailable.", "Inspect the unit and any incomplete removal before relying on automatic startup.")
			}
		}
	default:
		add("registration_unknown", "unavailable", "Service registration could not be verified; unknown is not absence.", "Check manager access, service identity, and overrides. Raw manager output is withheld; no recovery action was taken.")
	}
	// An unrelated manual daemon may respond even for a missing or failed service.
	// Keep these findings explicitly scoped to the loop, never to the service PID.
	if r.Recorded {
		d := r.Daemon
		if d == nil || d.State != "responsive" {
			add("daemon_unverified", "warning", "No responding loop daemon was verified. This does not prove the service process is absent or identify a startup failure.", "Check status and doctor, inspect the service, and allow for startup/shutdown in progress. Do not clear loop holds as a repair.")
		} else {
			switch d.BinaryComparison {
			case "different":
				add("daemon_build_differs", "warning", "The responding loop daemon's build label differs from this CLI. It is not proven to belong to this service.", "Check the intended binary and service paths before explicitly restarting. Different labels do not establish which build is newer.")
			case "match":
			default:
				add("daemon_build_unknown", "unavailable", "The responding loop daemon's build label cannot be compared with this CLI.", "Use builds with identifiable version labels; this check does not inspect the service executable on disk.")
			}
			if d.DryRun {
				add("daemon_dry_run", "warning", "The responding loop daemon is in dry-run mode; it does not perform normal sends or probes.", "Confirm whether the dry-run daemon was intentional. It is not proven to be the service's process.")
			}
			if d.Phase == "waiting" && d.NextCheckAt != nil && d.ObservedAt != nil && d.ObservedAt.Sub(*d.NextCheckAt) > serviceOverdueGrace {
				add("daemon_check_overdue", "warning", "The responding loop daemon reports a normal check more than one minute overdue.", "Inspect status and doctor. Sleep, scheduling delays, or clock changes may explain this; responsiveness does not prove loop progress.")
			}
		}
	}
	return h
}
