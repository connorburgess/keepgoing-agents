package main

import (
	"path/filepath"

	"github.com/looptender/looptender/internal/adapter/runner"
)

type sessionStatus struct {
	State        string `json:"state"` // saved, missing, unavailable
	ID           string `json:"id,omitempty"`
	Source       string `json:"source"`
	LiveVerified bool   `json:"liveVerified"`
	Message      string `json:"message"`
}

func savedSession(stateDir, target, adapter string) *sessionStatus {
	if adapter != "antigravity" && !runner.KeepsSessionFile(adapter) {
		return nil
	}
	s := &sessionStatus{State: "missing", Source: "runner_session_file", Message: "No saved resume ID is recorded; this does not establish whether a turn is running."}
	dir := runner.Dir(filepath.Join(stateDir, "targets", target))
	var id string
	var err error
	if adapter == "antigravity" {
		s.Source = "antigravity_acp_session_file"
		id, err = dir.InspectAntigravitySession()
	} else {
		id, err = dir.InspectSession()
	}
	if err != nil {
		s.State, s.Message = "unavailable", "Saved resume metadata is inaccessible or unrecognized; its content is withheld."
	} else if id != "" {
		s.State, s.ID = "saved", id
		s.Message = "Saved resume ID only; live session identity and resumability are not verified."
		if adapter == "antigravity" {
			s.Message += " For manual continuation, use your configured Antigravity CLI with --conversation " + id + " and review any permission request there. This does not resume supervision or approve a cancelled request."
		}
	}
	return s
}
