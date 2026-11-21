package report

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var errBBPushCLI = errors.New("BB notification executable is unavailable or conflicts with BB_CLI")

// resolveBBPushCLI performs metadata-only resolution. It never probes the CLI
// and never removes BB caller, connection or authentication environment values.
// Resolve again immediately before launch: this is a configured-runtime check,
// not immutable executable admission. Interpreters and wrappers remain trusted.
func resolveBBPushCLI(selected, inherited string) (string, error) {
	resolve := func(path string) (string, error) {
		if !filepath.IsAbs(path) {
			return "", errBBPushCLI
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", errBBPushCLI
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.Mode().IsRegular() {
			return "", errBBPushCLI
		}
		return filepath.Clean(canonical), nil
	}
	canonical, err := resolve(selected)
	if err != nil {
		return "", err
	}
	// Match the entrypoint's JavaScript String.trim, not Go's larger/different
	// Unicode whitespace set. Only the comparison value changes; the literal
	// selected path and inherited environment remain untouched.
	inherited = strings.TrimFunc(inherited, func(r rune) bool {
		return (r >= '\t' && r <= '\r') || r == ' ' || r == '\u00a0' ||
			r == '\u1680' || (r >= '\u2000' && r <= '\u200a') ||
			r == '\u2028' || r == '\u2029' || r == '\u202f' ||
			r == '\u205f' || r == '\u3000' || r == '\ufeff'
	})
	if inherited != "" {
		override, err := resolve(inherited)
		if err != nil || override != canonical {
			return "", errBBPushCLI
		}
	}
	return canonical, nil
}
