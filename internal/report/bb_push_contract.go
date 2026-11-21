package report

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
)

const maxBBPushReply = 4096

// BB budgets JavaScript UTF-16 units, not UTF-8 bytes or Unicode scalars. Keep
// the event in the title so even maximal configured labels cannot hide it.
func bbPushContent(e Event, source string) (title, body string) {
	label := func(s string) string {
		var b strings.Builder
		units := 0
		for _, r := range s {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				continue
			}
			n := 1
			if r > 0xffff {
				n = 2
			}
			if units+n > 80 {
				break
			}
			b.WriteRune(r)
			units += n
		}
		if b.Len() == 0 {
			return "unspecified"
		}
		return b.String()
	}
	return "Looptender: " + eventName(e.Type), "Loop: " + label(e.Loop) + " · Device: " + label(source)
}

// The CLI acknowledgement is not phone receipt. No values are projected into
// history; only its exact, bounded shape establishes process acceptance.
func validBBPushReply(body []byte) bool {
	if len(body) > maxBBPushReply {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seenOK, seenChannels := false, false
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		switch key {
		case "ok":
			if seenOK {
				return false
			}
			seenOK = true
			var value *bool
			if d.Decode(&value) != nil || value == nil || !*value {
				return false
			}
		case "channels":
			if seenChannels {
				return false
			}
			seenChannels = true
			var channels []string
			if d.Decode(&channels) != nil || len(channels) < 1 || len(channels) > 3 {
				return false
			}
			seen := map[string]bool{}
			for _, channel := range channels {
				if (channel != "web" && channel != "desktop" && channel != "mobile") || seen[channel] {
					return false
				}
				seen[channel] = true
			}
		default:
			return false
		}
	}
	token, err = d.Token()
	return err == nil && token == json.Delim('}') && seenOK && seenChannels && d.Decode(new(any)) == io.EOF
}
