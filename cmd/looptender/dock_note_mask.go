package main

import (
	"regexp"
	"strings"
)

// dockNoteHidden replaces each identifier in a hold's note shown without
// identifiers.
const dockNoteHidden = "[hidden]"

// The identifier shapes masked in a hold's note, in the order they are
// applied: a URL or email address is masked whole before its parts could
// look like a path or an ID.
var dockNoteIdentifiers = []*regexp.Regexp{
	// URLs with a scheme, and bare www. hosts.
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+|\bwww\.\S+`),
	// Email addresses.
	regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+`),
	// BB IDs: threads, registrations and loops, projects, environments and
	// hosts.
	regexp.MustCompile(`\b(thr|loop|proj|env|host)_[A-Za-z0-9_-]+`),
	// Absolute and home paths (/a/b, ~/a, C:\a), starting a word or after an
	// opening quote, bracket or "=".
	regexp.MustCompile(`(^|[\s"'` + "`" + `(\[{<=,;])(~?/[^\s"'` + "`" + `)\]}>]+|[A-Za-z]:\\[^\s"'` + "`" + `)\]}>]+)`),
}

// dockMaskNote returns a hold's note with every token shaped like an
// identifier replaced by dockNoteHidden, and whether any was. It looks only at
// the shape of tokens; nothing about the note's meaning is read, and no
// decision is made from its text. Sentence punctuation that ends a masked
// token is kept.
func dockMaskNote(note string) (string, bool) {
	out := note
	for i, re := range dockNoteIdentifiers {
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			lead := ""
			if i == len(dockNoteIdentifiers)-1 {
				// The path pattern also matches the character before it.
				if sub := re.FindStringSubmatch(m); sub != nil {
					lead, m = sub[1], sub[2]
				}
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
