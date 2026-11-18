package main

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/looptender/looptender/internal/accounts"
)

// accountDefaultDirWarning names the problem and the fix when an
// explicit_directory entry selects Claude Code's default config directory.
// It is shared by accounts add/update and doctor.
const accountDefaultDirWarning = "this entry selects Claude Code's default config directory (~/.claude) as an explicit directory. Claude Code treats CLAUDE_CONFIG_DIR set to that directory as a different login context from the default login, so the entry can report no login while you are signed in"

// accountDefaultDirFix is the fix for accountDefaultDirWarning.
const accountDefaultDirFix = "Use --selection inherited for the default login; keep explicit_directory for a second, separate config directory."

// accountDefaultDirectory returns the config directory a provider uses when
// its account variable is unset, or "" when it is not known here. Only Claude
// Code is covered (~/.claude, used when CLAUDE_CONFIG_DIR is unset; home is
// os.UserHomeDir, so %USERPROFILE%\.claude on Windows): the
// different login context was observed for it. Codex is not covered, because
// nothing in this repository establishes the same behaviour for CODEX_HOME.
func accountDefaultDirectory(provider, home string) string {
	if provider != "claude-code" || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// accountExplicitIsDefault reports whether an explicit_directory entry names
// the provider's default config directory. Entry directories are stored with
// symlinks resolved (existingDirectory), so the default is resolved the same
// way when it exists, and compared after cleaning otherwise. It reads no
// credential and runs nothing.
func accountExplicitIsDefault(e accounts.Entry, home string) bool {
	if e.Mode != "explicit_directory" || e.Directory == "" {
		return false
	}
	def := accountDefaultDirectory(e.Provider, home)
	if def == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(def); err == nil {
		def = resolved
	}
	return accountSameDirectory(e.Directory, def)
}

// accountSameDirectory compares two directories after cleaning; Windows
// paths compare without regard to case.
func accountSameDirectory(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// doctorAccountsCatalog is the catalog directory doctor reads for
// doctorAccountDefaultDirectories; "" is the person's own. Tests point it at
// a private directory.
var doctorAccountsCatalog = ""

// doctorAccountDefaultDirectories adds one warning per catalog entry that
// selects the provider's default directory explicitly. A missing or
// unreadable catalog adds nothing here; it never stops anything.
func doctorAccountDefaultDirectories(r *doctorReport, catalogDir, home string) {
	f := routeAccountCatalog(catalogDir)
	if f == nil {
		return
	}
	for _, e := range f.Entries {
		if accountExplicitIsDefault(e, home) {
			r.add("accounts."+e.ID+".directory", "warning", "Account "+e.Label+": "+accountDefaultDirWarning+".", accountDefaultDirFix)
		}
	}
}
