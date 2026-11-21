package report

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strings"
)

const maxPushoverReply = 16 * 1024

func validPushoverKey(s string) bool {
	if len(s) != 30 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func validPushoverURL(u *url.URL) bool {
	return u != nil && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawPath == "" && u.Path == "/1/messages.json" && (u.Scheme == "https" && u.Host == "api.pushover.net" || u.Scheme == "http" && isLoopback(u.Hostname()))
}

func validPushoverReply(body []byte) bool {
	if len(body) > maxPushoverReply {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := false
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		if strings.EqualFold(key, "status") {
			if key != "status" || seen {
				return false
			}
			seen = true
			var status *int
			if d.Decode(&status) != nil || status == nil || *status != 1 {
				return false
			}
		} else {
			var ignored json.RawMessage
			if d.Decode(&ignored) != nil {
				return false
			}
		}
	}
	token, err = d.Token()
	return err == nil && token == json.Delim('}') && seen && d.Decode(new(any)) == io.EOF
}
