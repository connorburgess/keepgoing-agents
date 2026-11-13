package main

import (
	"encoding/json"
	"os"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
)

func doctorCursorVersion(r *doctorReport, id, version string) {
	if runner.CursorInspectedVersions[version] {
		r.add(id, "ok", "CLI version: "+version+". Its status command and print-mode stream are the ones keepgoing's Cursor support was inspected against and walked through live once on macOS.", "")
		return
	}
	r.add(id, "warning", "CLI version: "+version+". This is not a version keepgoing's Cursor support was inspected against, so its status and stream formats are unverified.", "Review docs/cursor.md for the inspected versions before unattended use.")
}

func doctorCursorModels(configured string) string {
	const base = "The Cursor CLI lists models only as human-readable text, which is not parsed. "
	if configured == "" {
		return base + "No model is configured, so turns use Cursor's own default, which is not checked."
	}
	return base + "The configured model is passed to each turn but is not checked against the account, and a turn reports only a display name."
}

// doctorCursorEnvCredential reports whether a turn would be given a credential
// through the environment. Only presence is examined, never the value.
func doctorCursorEnvCredential(overrides map[string]string) bool {
	for _, key := range []string{"CURSOR_API_KEY", "CURSOR_AUTH_TOKEN"} {
		if _, ok := overrides[key]; ok {
			return true
		}
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

// doctorCursorLogin reads the status command's JSON form. It uses the status
// token, the three booleans and whether an account identity was returned. The
// identity itself, the message text and every other field are never reported.
func doctorCursorLogin(r *doctorReport, id string, raw []byte, cmdErr error) {
	const fix = "Check authentication with Cursor's own status and login commands; doctor never signs in or out."
	var result struct {
		Status          *string `json:"status"`
		IsAuthenticated *bool   `json:"isAuthenticated"`
		HasAccessToken  *bool   `json:"hasAccessToken"`
		HasRefreshToken *bool   `json:"hasRefreshToken"`
		UserInfo        *struct {
			Email  json.RawMessage `json:"email"`
			UserID json.RawMessage `json:"userId"`
		} `json:"userInfo"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Status == nil || result.IsAuthenticated == nil || result.HasAccessToken == nil || result.HasRefreshToken == nil {
		r.add(id, "unavailable", loginUnverified, fix)
		return
	}
	// An identity is an email that is a non-empty string or a user ID that is
	// a number. Anything else, such as {} or false, is not one.
	identity := false
	if result.UserInfo != nil {
		var email string
		var userID json.Number
		identity = (json.Unmarshal(result.UserInfo.Email, &email) == nil && email != "") ||
			(len(result.UserInfo.UserID) > 0 && result.UserInfo.UserID[0] != '"' && json.Unmarshal(result.UserInfo.UserID, &userID) == nil && userID.String() != "")
	}
	if cmdErr != nil {
		r.add(id, "unavailable", loginUnverified, fix)
		return
	}
	switch {
	case *result.Status == "authenticated" && *result.IsAuthenticated && *result.HasAccessToken && *result.HasRefreshToken:
		if identity {
			r.add(id, "ok", "Cursor reports a signed-in account and returned its identity, which is not displayed. Model access, quota and whether a turn will be accepted were not tested.", "")
			return
		}
		// The CLI reports stored tokens as authenticated even when the account
		// lookup fails, so this is not a confirmed login.
		r.add(id, "unavailable", "Cursor has stored credentials but did not return an account identity, so the login is not confirmed.", fix)
		return
	case (*result.Status == "unauthenticated" || *result.Status == "partially-authenticated") && !*result.IsAuthenticated:
		// These replies were read in the CLI's source and never observed, so
		// they are reported without being treated as a finding.
		r.add(id, "unavailable", "Cursor's status reply indicates no usable stored login for this environment and working directory. This reply shape has not been observed from a real CLI, so it is not confirmed.", fix)
		return
	}
	r.add(id, "unavailable", loginUnverified, fix)
}
