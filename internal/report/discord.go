package report

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
)

const maxDiscordReply = 64 * 1024

func discordSnowflake(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == s
}

// Accept the copied incoming webhook URL, pin its API version and request a
// saved-message acknowledgement. No thread creation/unarchive query is allowed.
func discordDestination(raw string) (string, string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", "", false
	}
	if !(u.Scheme == "https" && u.Host == "discord.com") && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return "", "", false
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) == 6 && parts[2] == "v10" {
		parts = append(parts[:2], parts[3:]...)
	}
	if len(parts) != 5 || parts[0] != "" || parts[1] != "api" || parts[2] != "webhooks" || !discordSnowflake(parts[3]) {
		return "", "", false
	}
	token := parts[4]
	if len(token) == 0 || len(token) > 256 || token == "." || token == ".." {
		return "", "", false
	}
	for _, r := range token {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return "", "", false
		}
	}
	u.Path = "/api/v10/webhooks/" + parts[3] + "/" + token
	u.RawQuery = "wait=true"
	return u.String(), parts[3], true
}

func discordBody(e Event, source string) ([]byte, error) {
	// Disable mentions independently of content escaping; labels are not commands.
	text := string(ntfyBody(e, source))
	text = strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "~", "\\~", "`", "\\`", "|", "\\|", ">", "\\>", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)").Replace(text)
	return json.Marshal(map[string]any{"content": text, "tts": false, "flags": 4, "allowed_mentions": map[string]any{"parse": []string{}}})
}

func validDiscordReply(body []byte, webhook string) bool {
	if len(body) > maxDiscordReply {
		return false
	}
	var msg struct {
		ID        string `json:"id"`
		WebhookID string `json:"webhook_id"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	return d.Decode(&msg) == nil && d.Decode(new(any)) == io.EOF && discordSnowflake(msg.ID) && msg.WebhookID == webhook
}
