package main

import (
	"encoding/json"
	"errors"
	"os/exec"
)

const loginUnverified = "Login status could not be established; the command may be unsupported, failed, timed out, or exceeded its output limit."
const loginFix = "Inspect the selected account through the provider's own status controls. Doctor does not sign in, sign out, or change credentials."

func doctorClaudeLogin(r *doctorReport, id string, raw []byte, cmdErr error) {
	// Decode only the documented status signal. Never publish account identity,
	// config paths, auth methods, or raw errors returned alongside it.
	var result struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if json.Unmarshal(raw, &result) == nil && result.LoggedIn != nil {
		if *result.LoggedIn && cmdErr == nil {
			r.add(id, "ok", "Claude Code reports logged in for this environment and working directory. Credential validity, model access, and quota were not tested.", "")
			return
		}
		var exitErr *exec.ExitError
		if !*result.LoggedIn && (cmdErr == nil || (errors.As(cmdErr, &exitErr) && exitErr.ExitCode() == 1)) {
			r.add(id, "warning", "Claude Code reports not logged in for this environment and working directory.", loginFix)
			return
		}
	}
	r.add(id, "unavailable", loginUnverified, loginFix)
}

func doctorCodexLogin(r *doctorReport, id string, cmdErr error) {
	// login status documents exit 0 as credentials present. Nonzero is
	// ambiguous (missing login, unsupported command, or operational failure).
	// Neither stdout nor stderr is parsed, even for a successful command.
	if cmdErr == nil {
		r.add(id, "ok", "Codex reports credentials present for this environment and working directory. Credential validity, model access, and quota were not tested.", "")
		return
	}
	r.add(id, "unavailable", loginUnverified, loginFix)
}
