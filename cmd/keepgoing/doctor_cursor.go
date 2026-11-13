package main

import (
	"encoding/json"
	"os"
)

// cursorInspectedVersions lists the Cursor CLI versions whose help, status
// command and print-mode stream were inspected read-only (see docs/cursor.md).
// One live walkthrough ran on the listed version on macOS only.
var cursorInspectedVersions = map[string]bool{"2026.09.18-9a7762b": true}

func doctorCursorVersion(r *doctorReport, id, version string) {
	if cursorInspectedVersions[version] {
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
	present := func(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" && string(v) != `""` }
	switch {
	case *result.Status == "authenticated" && *result.IsAuthenticated && *result.HasAccessToken && *result.HasRefreshToken:
		if cmdErr != nil {
			break
		}
		if result.UserInfo != nil && (present(result.UserInfo.Email) || present(result.UserInfo.UserID)) {
			r.add(id, "ok", "Cursor reports a signed-in account and returned its identity, which is not displayed. Model access, quota and whether a turn will be accepted were not tested.", "")
			return
		}
		// The CLI reports stored tokens as authenticated even when the account
		// lookup fails, so this is not a confirmed login.
		r.add(id, "unavailable", "Cursor has stored credentials but did not return an account identity, so the login is not confirmed.", fix)
		return
	case (*result.Status == "unauthenticated" || *result.Status == "partially-authenticated") && !*result.IsAuthenticated:
		r.add(id, "warning", "Cursor reports no usable stored login for this environment and working directory.", fix)
		return
	}
	r.add(id, "unavailable", loginUnverified, fix)
}
