package main

import (
	"path/filepath"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
)

type sessionStatus struct {
	State        string `json:"state"` // saved, missing, unavailable
	ID           string `json:"id,omitempty"`
	Source       string `json:"source"`
	LiveVerified bool   `json:"liveVerified"`
	Message      string `json:"message"`
}

func savedSession(stateDir, target, adapter string) *sessionStatus {
	if !runner.KeepsSessionFile(adapter) {
		return nil
	}
	s := &sessionStatus{State: "missing", Source: "runner_session_file", Message: "No saved resume ID is recorded; this does not establish whether a turn is running."}
	id, err := runner.Dir(filepath.Join(stateDir, "targets", target)).InspectSession()
	if err != nil {
		s.State, s.Message = "unavailable", "Saved resume metadata is inaccessible or unrecognized; its content is withheld."
	} else if id != "" {
		s.State, s.ID = "saved", id
		s.Message = "Saved resume ID only; live session identity and resumability are not verified."
	}
	return s
}
