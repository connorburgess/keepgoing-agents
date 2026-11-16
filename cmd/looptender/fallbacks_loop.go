package main

import (
	"errors"
	"strings"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/registry"
)

// fallbacksLoopFlagHelp is said in each fallbacks command's help.
const fallbacksLoopFlagHelp = `Name a registered loop with --loop NAME instead of -c, as the routes commands
do (--directory DIR selects another private registry). The command then runs
exactly as with -c and that loop's configuration file.
`

// errFallbacksLoopUsage is a malformed --loop request.
var errFallbacksLoopUsage = errors.New("usage")

// fallbacksByLoop replaces --loop NAME, and --directory DIR with it, by -c and
// the configuration path the loop registry holds for NAME, so a person (or a
// page that never shows paths) can name a loop as the routes commands do.
// Every other argument is kept, in order, and the command then runs as with
// -c: the same checks, owner rules and records. Without --loop the arguments
// are returned unchanged.
func fallbacksByLoop(e env, sub string, args []string) ([]string, int, bool) {
	var out []string
	var name, dir string
	var hasLoop, hasDir, hasConfig bool
	take := func(i int, tok, flagName string) (string, int, bool) {
		if v, ok := strings.CutPrefix(tok, flagName+"="); ok {
			return v, i, true
		}
		if i+1 >= len(args) {
			return "", i, false
		}
		return args[i+1], i + 1, true
	}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			out = append(out, args[i:]...)
			break
		}
		flagName, _, _ := strings.Cut(strings.TrimLeft(tok, "-"), "=")
		isFlag := strings.HasPrefix(tok, "-") && tok != "-"
		switch {
		case isFlag && flagName == "loop":
			v, next, ok := take(i, tok, strings.SplitN(tok, "=", 2)[0])
			if !ok || hasLoop || v == "" {
				e.errf("fallbacks %s: --loop takes one registered loop name", sub)
				return nil, exitUsage, false
			}
			name, hasLoop, i = v, true, next
		case isFlag && flagName == "directory":
			v, next, ok := take(i, tok, strings.SplitN(tok, "=", 2)[0])
			if !ok || hasDir {
				e.errf("fallbacks %s: --directory takes one private registry directory", sub)
				return nil, exitUsage, false
			}
			dir, hasDir, i = v, true, next
		default:
			if isFlag && flagName == "c" {
				hasConfig = true
			}
			out = append(out, tok)
		}
	}
	switch {
	case !hasLoop && hasDir:
		e.errf("fallbacks %s: --directory applies only with --loop NAME", sub)
		return nil, exitUsage, false
	case !hasLoop:
		return args, exitOK, true
	case hasConfig:
		e.errf("fallbacks %s: pass either --loop NAME or -c FILE, not both", sub)
		return nil, exitUsage, false
	}
	// Messages name no path: the person named the loop, not its files.
	path, err := registryDirectory(dir)
	var f registry.File
	if err == nil {
		f, err = registry.Load(path)
	}
	if err != nil {
		e.errf("fallbacks %s: refused, nothing written.\n  The loop registry cannot be read.", sub)
		return nil, exitFailed, false
	}
	var found []registry.Entry
	for _, entry := range f.Entries {
		if entry.Loop == name {
			found = append(found, entry)
		}
	}
	switch len(found) {
	case 0:
		e.errf("fallbacks %s: no registered loop named %q (keepgoing registry list)", sub, bbLabel(name, 40))
		return nil, exitUsage, false
	case 1:
		// A configuration moved or deleted since it was registered is
		// refused as routes --loop refuses it, without the path.
		if _, err := config.Load(found[0].ConfigPath); err != nil {
			e.errf("fallbacks %s: refused, nothing written.\n  Loop %s's configuration cannot be loaded; inspect it with keepgoing validate.", sub, bbLabel(name, 40))
			return nil, exitUsage, false
		}
		// After the leading positional arguments (move ID, handoff on), which
		// the commands read before their flags.
		pos, rest := splitPositional(out)
		return append(append(pos, "-c", found[0].ConfigPath), rest...), exitOK, true
	default:
		e.errf("fallbacks %s: %d registered loops are named %q; pass -c with the one you mean (keepgoing registry list --private)", sub, len(found), bbLabel(name, 40))
		return nil, exitUsage, false
	}
}
