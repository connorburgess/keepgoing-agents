package report

import (
	"encoding/json"
	"net/url"
	"strings"
)

// The credential is the incoming webhook URL itself. No generic anonymous
// endpoint or API method is admitted by this format.
func validSlackURL(u *url.URL) bool {
	if u == nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	if !(u.Scheme == "http" && isLoopback(u.Hostname())) && !(u.Scheme == "https" && (u.Host == "hooks.slack.com" || u.Host == "hooks.slack-gov.com")) {
		return false
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "services" {
		return false
	}
	for _, p := range parts[2:] {
		if len(p) < 1 || len(p) > 128 {
			return false
		}
		for _, r := range p {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return false
			}
		}
	}
	return true
}

func slackBody(e Event, source string) ([]byte, error) {
	// Keep a constant fallback for clients that do not render blocks. Configured
	// labels are only present in a plain_text composition, never mrkdwn.
	return json.Marshal(map[string]any{
		"text":         "Looptender notification",
		"unfurl_links": false, "unfurl_media": false,
		"blocks": []any{map[string]any{"type": "section", "text": map[string]any{
			"type": "plain_text", "emoji": false, "text": string(ntfyBody(e, source)),
		}}},
	})
}
