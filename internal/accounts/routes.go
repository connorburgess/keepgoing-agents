package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"github.com/looptender/looptender/internal/config"
)

// Results of checking a route's stated account against the catalog. The
// check is about consistency only: it never says which account a provider
// actually used.
const (
	// RouteConsistent: a headless route's entry exists, has the route's
	// provider, and names the same directory when both name one.
	RouteConsistent = "consistent"
	// RouteNotVerified: a BB thread route. BB has no account per thread, so
	// the person's statement is accepted as given.
	RouteNotVerified = "not_verified"
	// RouteNotInCatalog: the catalog was read and holds no entry with the ID.
	RouteNotInCatalog = "not_in_catalog"
	// RouteProviderMismatch: the entry is for another provider.
	RouteProviderMismatch = "provider_mismatch"
	// RouteDirectoryMismatch: the route and the entry name different
	// account or config directories.
	RouteDirectoryMismatch = "directory_mismatch"
	// RouteCatalogUnavailable: the catalog could not be read, so nothing
	// was compared. It never stops a loop.
	RouteCatalogUnavailable = "catalog_unavailable"
)

// RouteAccount is what a route says about its account, for status and the
// panel. Account and Label are set only for a route that names an entry;
// Context only for one that does not. No path is included.
type RouteAccount struct {
	// Account is the catalog ID the person stated.
	Account string `json:"id,omitempty"`
	// Label is that entry's label, the person's own name for it, when the
	// catalog was read and holds the entry.
	Label string `json:"label,omitempty"`
	// Assigned is "by_you" when the route names an entry, else "none".
	Assigned string `json:"assigned"`
	// Check is one of the Route* results, for a route that names an entry.
	Check string `json:"check,omitempty"`
	// Context is the derived account-context key of a route that names no
	// entry: a hash of provider, selection, directory and quota group.
	Context string `json:"context,omitempty"`
	// Refused is true when the result is a configuration error: a route
	// other than a BB thread whose stated entry does not match. An
	// unreadable catalog and a route with no account are never refused.
	Refused bool `json:"-"`
}

// RouteSelection is how a route selects its account, in the catalog's
// selection words plus bb_thread, and the directory it names, if any.
// A headless Claude Code or Codex route names a directory through
// CLAUDE_CONFIG_DIR or CODEX_HOME in its environment.
func RouteSelection(a config.Agent) (selection, dir string) {
	switch {
	case a.BB != nil:
		return "bb_thread", ""
	case a.Claude != nil:
		if d := a.Claude.Env["CLAUDE_CONFIG_DIR"]; d != "" {
			return "explicit_directory", filepath.Clean(d)
		}
		return "inherited", ""
	case a.Codex != nil:
		if d := a.Codex.Env["CODEX_HOME"]; d != "" {
			return "explicit_directory", filepath.Clean(d)
		}
		return "inherited", ""
	case a.Cursor != nil:
		return "inherited", ""
	}
	return "externally_managed", ""
}

// RouteContext is the derived account-context key of a route: provider,
// selection or directory, and quota group, hashed as usage identities are,
// so no path is stored or shown. Two routes with the same key select the
// same account context as configured; it does not say they are the same
// account, or that two different keys are different accounts.
func RouteContext(a config.Agent) string {
	selection, dir := RouteSelection(a)
	b, _ := json.Marshal([]string{a.Provider, selection, dir, a.Group})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// CheckRoute compares the account a route names with the catalog. f is nil
// when the catalog could not be read. The result for a route that names no
// entry carries only its derived context.
func CheckRoute(f *File, a config.Agent) RouteAccount {
	if a.Account == "" {
		return RouteAccount{Assigned: "none", Context: RouteContext(a)}
	}
	out := RouteAccount{Account: a.Account, Assigned: "by_you"}
	if f == nil {
		out.Check = RouteCatalogUnavailable
		return out
	}
	e := Find(f, a.Account)
	if e == nil {
		out.Check, out.Refused = RouteNotInCatalog, a.BB == nil
		return out
	}
	out.Label = e.Label
	if a.BB != nil {
		// D2: BB has no account per thread; the statement is taken as given.
		out.Check = RouteNotVerified
		return out
	}
	if !sameProvider(e.Provider, a.Provider) {
		out.Check, out.Refused = RouteProviderMismatch, true
		return out
	}
	if _, dir := RouteSelection(a); dir != "" && e.Directory != "" && filepath.Clean(e.Directory) != dir {
		out.Check, out.Refused = RouteDirectoryMismatch, true
		return out
	}
	out.Check = RouteConsistent
	return out
}

// sameProvider: an entry for "other" stands for any provider the catalog has
// no word of its own for, such as a defined agent's name.
func sameProvider(entry, route string) bool {
	if entry == "other" {
		switch route {
		case "claude-code", "codex", "cursor", "bb":
			return false
		}
		return route != ""
	}
	return entry == route
}

// RouteCheckText is the plain-language result, naming no path.
func RouteCheckText(check string) string {
	switch check {
	case RouteConsistent:
		return "assigned by you; the entry's provider and directory match this route"
	case RouteNotVerified:
		return "assigned by you; not verified"
	case RouteNotInCatalog:
		return "the accounts catalog has no entry with this ID"
	case RouteProviderMismatch:
		return "the accounts catalog entry is for another provider"
	case RouteDirectoryMismatch:
		return "the accounts catalog entry names another account directory than this route"
	case RouteCatalogUnavailable:
		return "the accounts catalog could not be read, so the stated account was not compared"
	}
	return ""
}
