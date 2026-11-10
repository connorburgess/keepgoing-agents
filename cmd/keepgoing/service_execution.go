package main

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
)

// This is manager-reported execution history, not process liveness or provider
// health. No raw manager text or service-supplied status text is retained.
type serviceExecution struct {
	State      string `json:"state"` // available, unavailable
	Result     string `json:"result,omitempty"`
	MainExit   string `json:"mainExit,omitempty"`
	MainStatus *int   `json:"mainStatus,omitempty"`
	Message    string `json:"message"`
}

const systemdExecutionProperties = "Id,LoadState,ActiveState,FragmentPath,DropInPaths,Result,ExecMainCode,ExecMainStatus"

func unavailableServiceExecution() serviceExecution {
	return serviceExecution{State: "unavailable", Message: "Structured execution details could not be verified. The unit may have changed between observations; raw manager output is withheld."}
}

func inspectSystemdExecution(ctx context.Context, h serviceHost, p servicePlan, active string) serviceExecution {
	out, code, err := h.query(ctx, "show", "--all", "--property="+systemdExecutionProperties, p.Label)
	if err != nil || code != 0 || len(out) > 65536 {
		return unavailableServiceExecution()
	}
	return parseSystemdExecution(out, p, active)
}

func parseSystemdExecution(out []byte, p servicePlan, active string) serviceExecution {
	unknown := unavailableServiceExecution()
	props := map[string]string{}
	allowed := strings.Split(systemdExecutionProperties, ",")
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return unknown
		}
		if _, dup := props[k]; dup {
			return unknown
		}
		known := false
		for _, key := range allowed {
			known = known || k == key
		}
		if !known {
			return unknown
		}
		props[k] = v
	}
	if len(props) != len(allowed) || props["Id"] != p.Label || props["LoadState"] != "loaded" || props["DropInPaths"] != "" || props["ActiveState"] != active {
		return unknown
	}
	expected, e1 := canonicalFuturePath(p.Definition)
	actual, e2 := canonicalFuturePath(props["FragmentPath"])
	if !filepath.IsAbs(props["FragmentPath"]) || e1 != nil || e2 != nil || expected != actual {
		return unknown
	}
	return describeSystemdExecution(props["Result"], props["ExecMainCode"], props["ExecMainStatus"])
}

// Result names follow systemd's service_result table. ExecMainCode is Linux's
// CLD_* reason (not an exit status): 1 exited, 2 killed, 3 dumped. Zero means no
// exit is reported, which cannot distinguish a running process from no run.
func describeSystemdExecution(result, code, status string) serviceExecution {
	unknown := unavailableServiceExecution()
	var explanation string
	switch result {
	case "success":
		explanation = "The manager reports no service failure; this does not prove a run occurred or is healthy."
	case "resources":
		explanation = "The manager reports a resource or service setup failure."
	case "protocol":
		explanation = "The manager reports a service startup protocol failure."
	case "timeout":
		explanation = "The manager reports a service operation timeout."
	case "exit-code":
		explanation = "The manager reports an unsuccessful process exit code."
	case "signal":
		explanation = "The manager reports termination by signal."
	case "core-dump":
		explanation = "The manager reports termination with a core dump."
	case "watchdog":
		explanation = "The manager reports a watchdog failure."
	case "start-limit-hit":
		explanation = "The manager reports that its service start-rate limit was reached."
	case "oom-kill":
		explanation = "The manager reports an out-of-memory kill."
	case "exec-condition":
		explanation = "The manager reports that an execution condition skipped the service."
	default:
		return unknown
	}
	n, err := strconv.Atoi(status)
	if err != nil || strconv.Itoa(n) != status {
		return unknown
	}
	x := serviceExecution{State: "available", Result: result, Message: explanation + " These properties can describe the last run; they do not identify the responding loop daemon or a provider failure."}
	switch code {
	case "0":
		if n != 0 {
			return unknown
		}
		x.MainExit = "not_reported"
	case "1":
		if n < 0 || n > 255 {
			return unknown
		}
		x.MainExit, x.MainStatus = "exited", &n
	case "2", "3":
		// Keep signals numeric: host Go constants would be wrong when fixtures
		// run on macOS. Accept the common Linux signal range conservatively;
		// larger architecture-specific values remain unavailable.
		if n < 1 || n > 64 {
			return unknown
		}
		x.MainExit, x.MainStatus = "killed", &n
		if code == "3" {
			x.MainExit = "dumped"
		}
	default:
		return unknown
	}
	return x
}
