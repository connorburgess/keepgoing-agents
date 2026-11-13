// Package evidence defines diagnostic metadata only. Its fields never drive
// supervision decisions. Unknown strings are discarded, not copied as prose.
package evidence

import "strings"

type Detail struct {
	Source     string `json:"source"`
	Category   string `json:"category"`
	Code       string `json:"code"`
	HTTPStatus *int   `json:"httpStatus"`
	ExitCode   *int   `json:"exitCode"`
}

// Code keeps a deliberately finite diagnostic vocabulary, independent of the
// decision engine's classification policy. New provider tokens need review.
func Code(raw string) string {
	switch s := strings.ToLower(strings.TrimSpace(raw)); s {
	case "rate_limit", "rate-limit", "rate_limited", "rate_limit_error", "usage_limit", "usage_limit_reached", "quota_exceeded", "insufficient_quota", "too_many_requests", "subscription_window_blocked",
		"auth", "unauthorized", "unauthenticated", "authentication_error", "permission_denied", "permission_error", "restricted", "forbidden", "oauth_org_not_allowed", "refusal", "refused", "policy", "cyberpolicy", "misalignmentpolicyviolation", "invalid_api_key", "oauth_token_expired",
		"overloaded", "overloaded_error", "service_unavailable", "capacity", "server_overloaded",
		"thread_command_failed",
		"network", "econnreset", "econnrefused", "etimedout", "enotfound", "timeout", "connection_error", "stream-disconnected", "stream_disconnected", "responsestreamdisconnected":
		return s
	}
	return "unavailable"
}

func Number(n int) *int { return &n }

func Clean(d Detail) Detail {
	switch d.Source {
	case "bb_status", "bb_provider_error", "bb_system_error", "bb_failure_event", "bb_error_summary", "bb_rate_limits", "process_json", "process_exit", "runner_state", "runner_exit", "claude_result", "cursor_events", "codex_events", "codex_rate_limits":
	default:
		return Detail{Source: "unavailable", Category: "unavailable", Code: "unavailable"}
	}
	d.Category = Code(d.Category)
	d.Code = Code(d.Code)
	if d.HTTPStatus != nil {
		n := *d.HTTPStatus
		if n < 100 || n > 599 {
			d.HTTPStatus = nil
		} else {
			d.HTTPStatus = Number(n)
		}
	}
	if d.ExitCode != nil {
		n := *d.ExitCode
		if n < 0 {
			d.ExitCode = nil
		} else {
			d.ExitCode = Number(n)
		}
	}
	return d
}
