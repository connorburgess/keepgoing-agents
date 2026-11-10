package main

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
)

type serviceBinding struct {
	State   string `json:"state"` // verified, different, unavailable
	Message string `json:"message"`
}

type systemdProcess struct {
	pid        int
	invocation string
}

const systemdBindingProperties = "Id,LoadState,ActiveState,FragmentPath,DropInPaths,MainPID,InvocationID"

// Bracket a fresh authenticated daemon challenge with manager observations.
// A changed or missing invocation never verifies identity, even if a PID matches.
func inspectSystemdBinding(ctx context.Context, h serviceHost, p servicePlan, queryDaemon func() daemonStatus) (daemonStatus, serviceBinding) {
	before := inspectSystemdProcess(ctx, h, p)
	d := queryDaemon()
	var after systemdProcess
	if before.pid > 0 && d.State == "responsive" {
		after = inspectSystemdProcess(ctx, h, p)
	}
	return d, assessServiceBinding(before, after, d)
}

func assessServiceBinding(before, after systemdProcess, d daemonStatus) serviceBinding {
	if before.pid < 1 || before.pid > 2147483647 || !daemoninfo.ValidInvocationID(before.invocation) || before != after || d.State != "responsive" || d.processID < 1 || d.processID > 2147483647 || !daemoninfo.ValidInvocationID(d.invocationID) {
		return serviceBinding{"unavailable", "A stable active systemd invocation and matching daemon identity could not be established. Older daemons, manual launches, transitions, or unavailable properties may explain this."}
	}
	if d.processID != before.pid || d.invocationID != before.invocation {
		return serviceBinding{"different", "The responding loop daemon reports a different process or invocation from the recorded systemd service. No process was stopped or restarted."}
	}
	return serviceBinding{"verified", "The authenticated loop responder's process and invocation match the active systemd main process before and after the challenge. This is a point-in-time identity observation, not proof of binary contents, provider health, or future execution."}
}

func inspectSystemdProcess(ctx context.Context, h serviceHost, p servicePlan) systemdProcess {
	out, code, err := h.query(ctx, "show", "--all", "--property="+systemdBindingProperties, p.Label)
	if err != nil || code != 0 || len(out) > 65536 {
		return systemdProcess{}
	}
	props := map[string]string{}
	allowed := strings.Split(systemdBindingProperties, ",")
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return systemdProcess{}
		}
		if _, dup := props[k]; dup {
			return systemdProcess{}
		}
		known := false
		for _, key := range allowed {
			known = known || k == key
		}
		if !known {
			return systemdProcess{}
		}
		props[k] = v
	}
	if len(props) != len(allowed) || props["Id"] != p.Label || props["LoadState"] != "loaded" || props["ActiveState"] != "active" || props["DropInPaths"] != "" {
		return systemdProcess{}
	}
	expected, e1 := canonicalFuturePath(p.Definition)
	actual, e2 := canonicalFuturePath(props["FragmentPath"])
	if !filepath.IsAbs(props["FragmentPath"]) || e1 != nil || e2 != nil || expected != actual {
		return systemdProcess{}
	}
	pid, err := strconv.ParseInt(props["MainPID"], 10, 32)
	if err != nil || pid < 1 || strconv.FormatInt(pid, 10) != props["MainPID"] || !daemoninfo.ValidInvocationID(props["InvocationID"]) {
		return systemdProcess{}
	}
	return systemdProcess{int(pid), props["InvocationID"]}
}
