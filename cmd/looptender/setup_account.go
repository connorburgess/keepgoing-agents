package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
)

// setupAccountsDir is the accounts catalog init and fallbacks add read; empty
// is the default directory. Tests set it.
var setupAccountsDir = ""

// setupCatalog reads the accounts catalog for a setup question. It is nil
// when the catalog cannot be read; a catalog never written is read as empty.
// It is a local file read only: nothing is run and no provider is asked.
func setupCatalog() *accounts.File {
	return routeAccountCatalog(setupAccountsDir)
}

// setupProviderOf is the provider word the catalog uses for an agent kind.
func setupProviderOf(kind, bbProvider string) string {
	switch kind {
	case "claude":
		return "claude-code"
	case "codex":
		return "codex"
	case "bb":
		return bbProvider
	}
	return kind
}

// setupAccountChoices are the catalog entries a setup question offers for a
// route of this kind. A headless route offers the entries for its provider
// that select a directory or the normal login; a BB thread route, whose
// account BB chooses per project, offers the entries for the thread's
// provider and for BB. The person picks; nothing is matched or guessed.
func setupAccountChoices(f *accounts.File, kind, bbProvider string) []accounts.Entry {
	if f == nil {
		return nil
	}
	var out []accounts.Entry
	for _, e := range f.Entries {
		switch kind {
		case "claude", "codex":
			if e.Provider == setupProviderOf(kind, "") && (e.Mode == "explicit_directory" || e.Mode == "inherited") {
				out = append(out, e)
			}
		case "bb":
			if e.Provider == "bb" || (bbProvider != "" && e.Provider == bbProvider) {
				out = append(out, e)
			}
		}
	}
	return out
}

// setupSelectionText says in plain words how an entry selects its account.
func setupSelectionText(e accounts.Entry) string {
	switch e.Mode {
	case "explicit_directory":
		return "its own signed-in directory"
	case "inherited":
		return "your normal login"
	}
	return "managed by the provider"
}

// setupLabelWidth bounds a name in a setup list or line, in characters.
const setupLabelWidth = 40

// setupLabel is an entry's name as setup shows it: printable and bounded.
func setupLabel(e accounts.Entry) string { return bbLabel(e.Label, setupLabelWidth) }

// printSetupAccountChoices lists the entries a person may pick by number,
// names padded by characters so the second column lines up.
func printSetupAccountChoices(w io.Writer, choices []accounts.Entry) {
	fmt.Fprintln(w, "  Your named accounts (keepgoing accounts list):")
	width := 0
	for _, e := range choices {
		width = max(width, utf8.RuneCountInString(setupLabel(e)))
	}
	for i, e := range choices {
		label := setupLabel(e)
		fmt.Fprintf(w, "    %d) %s%s  %s\n", i+1, label, strings.Repeat(" ", width-utf8.RuneCountInString(label)), setupSelectionText(e))
	}
}

// setupPickNumber reads an answer as a number from the list, if it is one.
func setupPickNumber(answer string, choices []accounts.Entry) (*accounts.Entry, bool, error) {
	n, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil {
		return nil, false, nil
	}
	if n < 1 || n > len(choices) {
		return nil, true, fmt.Errorf("Choose a number from 1 to %d, a directory, or leave it blank.", len(choices))
	}
	e := choices[n-1]
	return &e, true, nil
}

// setupAccountEntry finds the entry a person named with --account. An
// unreadable catalog, or an ID it does not hold, is refused: setup writes a
// statement only about an entry it could see.
func setupAccountEntry(id string) (*accounts.Entry, error) {
	id = strings.TrimSpace(id)
	f := setupCatalog()
	if f == nil {
		return nil, errors.New("--account: the accounts catalog could not be read, so the account cannot be named; leave --account out or check keepgoing accounts list")
	}
	e := accounts.Find(f, id)
	if e == nil {
		return nil, fmt.Errorf("--account: the accounts catalog has no entry with ID %q (keepgoing accounts list)", bbLabel(id, 40))
	}
	out := *e
	return &out, nil
}

// setupAccountDir is the account directory a route takes from the entry
// the person named, when they gave none themselves.
func setupAccountDir(e *accounts.Entry, given string) string {
	if strings.TrimSpace(given) == "" && e != nil && e.Mode == "explicit_directory" {
		return e.Directory
	}
	return given
}

// setupCheckAccount states the entry on the route and compares them, as
// validate does, and more strictly where validate cannot tell: a route that
// names its own directory does not fit an entry on the normal login, and a
// BB thread route fits only an entry for BB or for the thread's provider,
// the entries the guided questions offer. A mismatch is refused, naming the
// entries that fit: setup never writes a statement validate would reject or
// one the questions would not have offered.
func setupCheckAccount(a *config.Agent, e *accounts.Entry) error {
	if e == nil {
		return nil
	}
	kind := a.Adapter
	_, dir := accounts.RouteSelection(*a)
	var why string
	switch {
	case kind == "bb":
		if e.Provider != "bb" && e.Provider != a.Provider {
			why = "the entry is for " + e.Provider + ", not for BB or this thread's provider (" + a.Provider + ")"
		}
	case dir != "" && e.Mode != "explicit_directory":
		why = "the entry uses " + setupSelectionText(*e) + ", but this route names its own account directory"
	}
	a.Account = e.ID
	if why == "" {
		if r := accounts.CheckRoute(&accounts.File{Entries: []accounts.Entry{*e}}, *a); r.Refused {
			why = accounts.RouteCheckText(r.Check)
		}
	}
	if why == "" {
		return nil
	}
	a.Account = ""
	return fmt.Errorf("account %q does not fit this route: %s. %s", setupLabel(*e), why, setupFitting(kind, a.Provider, dir))
}

// setupFitting names the catalog entries that would fit a route of this
// kind, for a refusal.
func setupFitting(kind, provider, dir string) string {
	var names []string
	for _, c := range setupAccountChoices(setupCatalog(), kind, provider) {
		// A route with its own directory fits only an entry for that
		// directory; one without fits any, since an entry's directory is
		// used when none is given.
		if dir != "" && (c.Mode != "explicit_directory" || filepath.Clean(c.Directory) != dir) {
			continue
		}
		names = append(names, fmt.Sprintf("%q", setupLabel(c)))
	}
	if len(names) == 0 {
		return "No named account fits it; name one with keepgoing accounts add."
	}
	return "Named accounts that fit it: " + strings.Join(names, ", ") + "."
}

// setupGroupFromLabel is a usage-limit label made from an account's name, or
// "" when the name has no letters or digits to make one from.
func setupGroupFromLabel(label string) string {
	return fallbackAddSlug(label)
}
