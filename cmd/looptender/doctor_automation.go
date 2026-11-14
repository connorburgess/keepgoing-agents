package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"regexp"
	"time"
)

// Only allowlisted metadata is decoded. Script text, prompts, paths, error
// messages, and run output are never inspected or included in the report.
type bbAutomationStatus struct {
	State         string     `json:"state"` // registered, unavailable, offline
	ID            string     `json:"id"`
	ProjectID     string     `json:"projectId"`
	Enabled       *bool      `json:"enabled,omitempty"`
	NextRunAt     *time.Time `json:"nextRunAt,omitempty"`
	LastRunAt     *time.Time `json:"lastRunAt,omitempty"`
	LastRunStatus string     `json:"lastRunStatus"`
	LoopVerified  bool       `json:"loopVerified"`
	Message       string     `json:"message"`
}

var (
	automationIDPattern      = regexp.MustCompile(`^auto_[A-Za-z0-9_-]{1,128}$`)
	automationProjectPattern = regexp.MustCompile(`^proj_[A-Za-z0-9_-]{1,128}$`)
)

func validAutomationSelection(id, project string) bool {
	return (id == "" && project == "") || (automationIDPattern.MatchString(id) && automationProjectPattern.MatchString(project))
}

func inspectBBAutomation(ctx context.Context, cli, id, project string, offline bool, timeout time.Duration, run doctorRun) bbAutomationStatus {
	s := bbAutomationStatus{State: "unavailable", ID: id, ProjectID: project, LastRunStatus: "unknown", Message: "Selected BB automation could not be verified; command output and error details are withheld."}
	if offline {
		s.State, s.Message = "offline", "BB automation query skipped in offline mode."
		return s
	}
	resolved, err := exec.LookPath(cli)
	if err != nil {
		s.Message = "BB executable for the selected automation is missing or not executable."
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := run(ctx, resolved, []string{"automation", "show", id, "--project", project, "--json"}, nil, "")
	if err != nil || ctx.Err() != nil {
		return s
	}
	var v struct {
		OK        *bool           `json:"ok"`
		Error     json.RawMessage `json:"error"`
		ID        string          `json:"id"`
		ProjectID string          `json:"projectId"`
		Enabled   *bool           `json:"enabled"`
		Execution struct {
			Mode string `json:"mode"`
		} `json:"execution"`
		NextRunAt     *int64 `json:"nextRunAt"`
		LastRunAt     *int64 `json:"lastRunAt"`
		LastRunStatus string `json:"lastRunStatus"`
	}
	if json.Unmarshal(out, &v) != nil || (v.OK != nil && !*v.OK) || (len(v.Error) > 0 && string(v.Error) != "null") || v.ID != id || v.ProjectID != project || v.Enabled == nil {
		return s
	}
	if v.Execution.Mode != "script" {
		s.Message = "Selected BB automation is not a supported script automation; keepgoing does not manage agent automations."
		return s
	}
	next, nextOK := automationTimestamp(v.NextRunAt)
	last, lastOK := automationTimestamp(v.LastRunAt)
	if !nextOK || !lastOK {
		return s
	}
	s.State, s.Enabled, s.NextRunAt, s.LastRunAt = "registered", v.Enabled, next, last
	s.Message = "BB returned the selected script automation's registration metadata. Script contents, loop binding, installed binary, and scheduler process liveness are unverified."
	switch v.LastRunStatus {
	case "succeeded", "failed", "skipped", "running":
		s.LastRunStatus = v.LastRunStatus
	}
	return s
}

func automationTimestamp(ms *int64) (*time.Time, bool) {
	if ms == nil {
		return nil, true
	}
	if *ms <= 0 || *ms > 253402300799999 { // Positive Unix milliseconds, through year 9999.
		return nil, false
	}
	t := time.UnixMilli(*ms).UTC()
	return &t, true
}

func doctorAutomationResult(r *doctorReport, s bbAutomationStatus) {
	status := "unavailable"
	message := s.Message
	if s.State == "registered" {
		status = "ok"
		if !*s.Enabled {
			status = "warning"
			message += " The selected automation is disabled; scheduled runs are not expected."
		} else if s.NextRunAt == nil {
			status = "warning"
			message += " It is enabled, but no next run is reported."
		} else {
			message += " Next scheduled run: " + s.NextRunAt.Format(time.RFC3339) + "."
			if !s.NextRunAt.After(r.CheckedAt) {
				status = "warning"
				message += " That time is already due; metadata alone cannot establish the cause or whether a run is active."
			}
		}
		if s.LastRunAt != nil {
			message += " Last run recorded at " + s.LastRunAt.Format(time.RFC3339) + "; reported outcome: " + s.LastRunStatus + "."
		}
		if s.LastRunStatus == "failed" {
			status = "warning"
			message += " The last reported run failed; its error text is withheld."
		}
	}
	r.add("scheduler.bb", status, message, "Review the selected automation in BB. Its registration and run history do not prove it invokes this loop; compare with tick history. Doctor never runs, resumes, pauses, or edits an automation.")
}
