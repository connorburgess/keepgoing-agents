package report

import (
	"errors"
	"os"
	"path/filepath"
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
	if inherited != "" {
		override, err := resolve(inherited)
		if err != nil || override != canonical {
			return "", errBBPushCLI
		}
	}
	return canonical, nil
}
