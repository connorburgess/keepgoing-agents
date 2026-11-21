package report

import (
	"strings"
	"unicode"
)

// Restrict the preset to one literal topic, not ntfy's JSON-root or other APIs.
func validNtfyTopic(path string) bool {
	if len(path) < 2 || len(path) > 65 || path[0] != '/' {
		return false
	}
	for _, r := range path[1:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Source and loop are explicitly configured display labels. Do not add event
// text or arbitrary data: they may contain tasks, paths, accounts or transcripts.
func ntfyBody(e Event, source string) []byte {
	label := func(s string) string {
		var b strings.Builder
		n := 0
		for _, r := range s {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				continue
			}
			if n == 120 {
				break
			}
			b.WriteRune(r)
			n++
		}
		if b.Len() == 0 {
			return "unspecified"
		}
		return b.String()
	}
	return []byte("Loop: " + label(e.Loop) + "\nDevice label: " + label(source) + "\nEvent: " + eventName(e.Type))
}
