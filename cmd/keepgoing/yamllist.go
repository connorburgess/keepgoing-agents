package main

import (
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlListFile is a YAML file with a top-level list located line by line, so
// an edit can cut, paste, insert or change one entry and leave every other
// byte of the file as it was. Each edit is verified by the caller parsing the
// result.
type yamlListFile struct {
	lines []string        // the file's lines, each with its line ending
	spans []candidateSpan // one per entry, with its attached comment lines
	items []*yaml.Node    // the entries' parsed nodes
	key   *yaml.Node      // the list's key, or nil when the key is absent
	list  *yaml.Node      // the list, or nil when the key is absent
	eol   string
}

// listSpans locates the entries of the top-level list under key. A list
// written in flow style on its line, or entries the parser places out of
// order, are refused with byHand. An empty or absent list has no spans.
func listSpans(raw []byte, key, byHand, invalid string) (*yamlListFile, error) {
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil, errors.New(invalid)
	}
	f := &yamlListFile{eol: "\n"}
	f.lines = strings.SplitAfter(string(raw), "\n")
	if f.lines[len(f.lines)-1] == "" {
		f.lines = f.lines[:len(f.lines)-1]
	}
	if len(f.lines) > 0 && strings.HasSuffix(f.lines[0], "\r\n") {
		f.eol = "\r\n"
	}
	root := yamlRoot(&doc)
	if root == nil {
		if len(strings.TrimSpace(string(raw))) == 0 {
			return f, nil
		}
		return nil, errors.New(byHand)
	}
	if root.Kind != yaml.MappingNode {
		return nil, errors.New(byHand)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			f.key, f.list = root.Content[i], root.Content[i+1]
		}
	}
	if f.list == nil {
		return f, nil
	}
	if f.list.Kind == yaml.ScalarNode && (f.list.Tag == "!!null" || f.list.Value == "") {
		return f, nil // "key:" with nothing after it
	}
	if f.list.Kind != yaml.SequenceNode {
		return nil, errors.New(byHand)
	}
	if f.list.Style&yaml.FlowStyle != 0 {
		if len(f.list.Content) == 0 {
			return f, nil // "key: []"
		}
		return nil, errors.New(byHand)
	}
	// The list ends where the next top-level key begins, or at the end of the file.
	limit := len(f.lines)
	for i := 0; i+1 < len(root.Content); i += 2 {
		if l := root.Content[i].Line - 1; l > f.list.Line-1 && l < limit {
			limit = l
		}
	}
	f.items = f.list.Content
	f.spans = make([]candidateSpan, len(f.items))
	for i, item := range f.items {
		start := item.Line - 1
		if start < 0 || start >= limit || !strings.HasPrefix(strings.TrimSpace(f.lines[start]), "-") || (i > 0 && start <= f.spans[i-1].start+f.spans[i-1].comments) {
			return nil, errors.New(byHand)
		}
		// Only comments at the entry's own indentation, directly above it,
		// belong to it. A heading less indented than the entries, or a
		// commented-out key of the entry before (more indented), stays put.
		dash := strings.Index(f.lines[start], "-")
		first := start
		for first > 0 && yamlComment(f.lines[first-1]) && indentOf(f.lines[first-1]) == dash {
			first--
		}
		f.spans[i] = candidateSpan{start: first, comments: start - first}
	}
	for i := range f.spans {
		end := limit
		if i+1 < len(f.spans) {
			end = f.spans[i+1].start
		}
		for end > f.spans[i].start+f.spans[i].comments+1 && (yamlBlank(f.lines[end-1]) || yamlComment(f.lines[end-1])) {
			end--
		}
		f.spans[i].end = end
	}
	return f, nil
}

func terminateLines(ls []string, eol string) {
	if n := len(ls); n > 0 && !strings.HasSuffix(ls[n-1], "\n") {
		ls[n-1] += eol
	}
}

// cutPasteEntry removes the entry at index at, or moves it to the 1-based
// place to, by cutting and pasting its lines. It returns the new lines and the
// comment lines that travel with the entry.
func cutPasteEntry(lines []string, spans []candidateSpan, at, to int) ([]string, []string) {
	eol := "\n"
	if len(lines) > 0 && strings.HasSuffix(lines[0], "\r\n") {
		eol = "\r\n"
	}
	cut := spans[at]
	block := append([]string{}, lines[cut.start:cut.end]...)
	comments := append([]string{}, block[:cut.comments]...)
	out := append(append([]string{}, lines[:cut.start]...), lines[cut.end:]...)
	if to > 0 {
		rest := append(append([]candidateSpan{}, spans[:at]...), spans[at+1:]...)
		shift := func(i int) int { // a line index after the cut
			if i >= cut.end {
				return i - (cut.end - cut.start)
			}
			return i
		}
		insert := shift(rest[len(rest)-1].end)
		if to-1 < len(rest) {
			insert = shift(rest[to-1].start)
		}
		terminateLines(block, eol)
		head := append([]string{}, out[:insert]...)
		terminateLines(head, eol)
		out = append(append(head, block...), out[insert:]...)
	}
	return out, comments
}

// insertEntry inserts entry (one line, without indentation or ending) as the
// 1-based place at in the list, or last when at is 0 or past the end. The
// entry's indentation follows the list's first entry, or two spaces.
func (f *yamlListFile) insertEntry(key string, entry string, at int) []string {
	lines := append([]string{}, f.lines...)
	if len(f.spans) == 0 {
		item := "  - " + entry + f.eol
		if f.key == nil {
			terminateLines(lines, f.eol)
			return append(lines, key+":"+f.eol, item)
		}
		// "key:" stays as it is, with any comment after it; "key: []" or
		// "key: null" becomes "key:", keeping its comment. The entry goes
		// below it.
		i := f.key.Line - 1
		indent := lines[i][:indentOf(lines[i])]
		if f.list != nil && f.list.Value != "" || f.list != nil && f.list.Style&yaml.FlowStyle != 0 {
			comment := f.key.LineComment
			if comment == "" {
				comment = f.list.LineComment
			}
			if comment != "" {
				comment = " " + comment
			}
			lines[i] = indent + key + ":" + comment + f.eol
		}
		head := append([]string{}, lines[:i+1]...)
		terminateLines(head, f.eol)
		return append(append(head, indent+item), lines[i+1:]...)
	}
	first := f.lines[f.spans[0].start+f.spans[0].comments]
	indent := first[:strings.Index(first, "-")]
	item := indent + "- " + entry + f.eol
	pos := f.spans[len(f.spans)-1].end
	if at > 0 && at <= len(f.spans) {
		pos = f.spans[at-1].start
	}
	head := append([]string{}, lines[:pos]...)
	terminateLines(head, f.eol)
	return append(append(head, item), lines[pos:]...)
}

// setDisabled switches the entry at index at off (disabled: true) or on, by
// changing, adding or removing only that entry's disabled setting. It returns
// false with no change when the entry cannot be edited line by line.
func (f *yamlListFile) setDisabled(at int, off bool) ([]string, bool) {
	item := f.items[at]
	if item.Kind != yaml.MappingNode {
		return nil, false
	}
	lines := append([]string{}, f.lines...)
	sp := f.spans[at]
	for i := 0; i+1 < len(item.Content); i += 2 {
		k, v := item.Content[i], item.Content[i+1]
		if k.Value != "disabled" {
			continue
		}
		if v.Kind != yaml.ScalarNode || v.Style != 0 || v.Line < 1 {
			return nil, false
		}
		l := v.Line - 1
		col := v.Column - 1
		if l >= len(lines) || col < 0 || col+len(v.Value) > len(lines[l]) || lines[l][col:col+len(v.Value)] != v.Value {
			return nil, false
		}
		want := "false"
		if off {
			want = "true"
		}
		lines[l] = lines[l][:col] + want + lines[l][col+len(v.Value):]
		return lines, true
	}
	if !off {
		return lines, true // no disabled setting: already on
	}
	if item.Style&yaml.FlowStyle != 0 {
		// One entry on one line: "- {id: x, ...}" gains ", disabled: true".
		if sp.end-sp.start-sp.comments != 1 {
			return nil, false
		}
		l := sp.start + sp.comments
		line := lines[l]
		open := item.Column - 1
		if item.Line-1 != l || open < 0 || open >= len(line) || line[open] != '{' {
			return nil, false
		}
		close := flowClose(line, open)
		if close < 0 {
			return nil, false
		}
		sep := ", "
		if strings.TrimSpace(line[open+1:close]) == "" {
			sep = ""
		}
		lines[l] = line[:close] + sep + "disabled: true" + line[close:]
		return lines, true
	}
	// A block entry gains "disabled: true" below its first key, at that key's indentation.
	if len(item.Content) == 0 {
		return nil, false
	}
	k0, v0 := item.Content[0], item.Content[1]
	last := v0.Line - 1
	if v0.Kind != yaml.ScalarNode || last < 0 || last >= len(lines) || k0.Column < 1 {
		return nil, false
	}
	add := strings.Repeat(" ", k0.Column-1) + "disabled: true" + f.eol
	head := append([]string{}, lines[:last+1]...)
	terminateLines(head, f.eol)
	return append(append(head, add), lines[last+1:]...), true
}

// flowClose is the index of the bracket that closes the flow collection
// opening at line[open], or -1 when it does not close on this line. Quoted
// text is skipped; the caller verifies the edit by parsing it.
func flowClose(line string, open int) int {
	depth := 0
	for i := open; i < len(line); i++ {
		switch c := line[i]; c {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i
			}
		case '"':
			for i++; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' {
					i++
				}
			}
		case '\'':
			for i++; i < len(line); i++ {
				if line[i] == '\'' {
					if i+1 < len(line) && line[i+1] == '\'' {
						i++
						continue
					}
					break
				}
			}
		case '#':
			if i > 0 && (line[i-1] == ' ' || line[i-1] == '\t') {
				return -1
			}
		}
	}
	return -1
}

// indentOf is the number of spaces a line starts with.
func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }
