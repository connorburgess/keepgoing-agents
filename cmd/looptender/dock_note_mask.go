package main

import (
	"regexp"
	"strings"
	"unicode"
)

// dockNoteHidden replaces each identifier in a hold's note shown without
// identifiers.
const dockNoteHidden = "[hidden]"

// dockNoteShape is one identifier shape masked in a hold's note. lead is
// true when the pattern's first group is the character before the token,
// which is kept. When is set, a match is masked only if it returns true, for
// conditions Go's regexp cannot say (it has no lookaround).
type dockNoteShape struct {
	re   *regexp.Regexp
	lead bool
	when func(string) bool
}

// The identifier shapes masked in a hold's note, in the order they are
// applied: a URL or email address is masked whole before its parts could
// look like a path or an ID, and a path whole before a long code inside it.
var dockNoteIdentifiers = []dockNoteShape{
	// URLs with a scheme, and bare www. hosts.
	{re: regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+|\bwww\.\S+`)},
	// Email addresses.
	{re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+`)},
	// Absolute and home paths (/a/b, ~/a, C:\a), starting a word or after an
	// opening quote, bracket or "=".
	{re: regexp.MustCompile(`(^|[\s"'` + "`" + `(\[{<=,;])(~?/[^\s"'` + "`" + `)\]}>]+|[A-Za-z]:\\[^\s"'` + "`" + `)\]}>]+)`), lead: true},
	// BB IDs: threads, registrations and loops, projects, environments and
	// hosts.
	{re: regexp.MustCompile(`\b(thr|loop|proj|env|host)_[A-Za-z0-9_-]+`)},
	// IPv4 addresses: exactly four dotted groups of one to three digits, with
	// an optional port. The whole dotted run is matched, so version strings
	// (1.2.3, v0.2.0, 1.2.3.4.5) stay.
	{re: regexp.MustCompile(`\b\d+(\.\d+)+(:\d{1,5})?\b`), when: dockNoteIPv4},
	// Long codes: keys, tokens, hashes and catalog IDs, 20 or more characters
	// with at least one letter and one digit. Long words and long numbers stay.
	{re: regexp.MustCompile(`[A-Za-z0-9_-]{20,}`), when: dockNoteLongCode},
}

// dockMaskNote returns a hold's note with every token shaped like an
// identifier replaced by dockNoteHidden, and whether any was. It looks only at
// the shape of tokens; nothing about the note's meaning is read, and no
// decision is made from its text. Sentence punctuation that ends a masked
// token is kept.
func dockMaskNote(note string) (string, bool) {
	out := note
	for _, shape := range dockNoteIdentifiers {
		out = shape.re.ReplaceAllStringFunc(out, func(m string) string {
			lead := ""
			if shape.lead {
				if sub := shape.re.FindStringSubmatch(m); sub != nil {
					lead, m = sub[1], sub[2]
				}
			}
			if shape.when != nil && !shape.when(m) {
				return lead + m
			}
			body := strings.TrimRight(m, ".,;:!?")
			if body == "" || body == "/" {
				return lead + m
			}
			return lead + dockNoteHidden + m[len(body):]
		})
	}
	return out, out != note
}

func dockNoteIPv4(m string) bool {
	host, _, _ := strings.Cut(m, ":")
	groups := strings.Split(host, ".")
	if len(groups) != 4 {
		return false
	}
	for _, g := range groups {
		if len(g) > 3 {
			return false
		}
	}
	return true
}

func dockNoteLongCode(m string) bool {
	return strings.ContainsAny(m, "0123456789") && strings.IndexFunc(m, unicode.IsLetter) >= 0
}
