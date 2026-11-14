// Package userdir says where the tool keeps the files that belong to a user
// and to no single loop: the loop registry, the standard route list and the
// default home of new loops under the configuration directory, and the
// session claims and the panel's runtime files under the state directory.
//
// Those files live in one directory under each. On a computer that has
// never run the tool it is named looptender. On a computer that already has
// a directory named keepgoing, the tool's earlier name, under either place,
// that name is kept for both, and nothing is moved: a build from before the
// rename still finds its files, and every command on the computer agrees on
// where the session claims are.
package userdir

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const (
	// Name is the directory's name on a computer with no earlier files.
	Name = "looptender"
	// LegacyName is the directory's name from before the tool was renamed.
	LegacyName = "keepgoing"
)

// ConfigHome is the user's configuration directory: $XDG_CONFIG_HOME or
// ~/.config on Linux and other Unix systems, Application Support on macOS,
// and the roaming application data folder on Windows.
func ConfigHome() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("the user configuration directory is not an absolute path")
	}
	return base, nil
}

// StateHome is the user's state directory, for runtime files:
// $XDG_STATE_HOME or ~/.local/state on Linux and other Unix systems,
// Application Support on macOS, and the local application data folder on
// Windows.
func StateHome() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return os.UserConfigDir() // ~/Library/Application Support
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return d, nil
		}
		return os.UserConfigDir()
	}
	if d := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

// NameIn decides the directory's name from the places it could be in: the
// earlier name when an entry of that name is under any of them, otherwise
// the current one. An empty base is a place that could not be determined
// and is skipped. A place that cannot be inspected counts as holding the
// earlier name: the choice that leaves an existing setup where it is.
//
// The decision is made afresh by every command and never remembered, so
// every process on the computer makes the same one at the same moment. A
// directory under the current name does not change it.
func NameIn(bases ...string) string {
	for _, base := range bases {
		if base == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(base, LegacyName)); err == nil || !errors.Is(err, os.ErrNotExist) {
			return LegacyName
		}
	}
	return Name
}

// DirName is NameIn for this user's configuration and state directories.
func DirName() string {
	config, _ := ConfigHome()
	state, _ := StateHome()
	return NameIn(config, state)
}

// Config returns the tool's directory under the user's configuration
// directory, joined with elem. It creates nothing.
func Config(elem ...string) (string, error) {
	base, err := ConfigHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{base, DirName()}, elem...)...), nil
}

// State returns the tool's directory under the user's state directory,
// joined with elem. It creates nothing.
func State(elem ...string) (string, error) {
	base, err := StateHome()
	if err != nil || base == "" {
		return "", errors.New("no user state directory")
	}
	return filepath.Join(append([]string{base, DirName()}, elem...)...), nil
}

// Found is what is on disk under the user's directories, for doctor.
type Found struct {
	Name    string   // the name in use: DirName
	Used    []string // the directories of that name that exist
	Ignored []string // directories of the current name that exist while the earlier name is in use
}

// Look reports the name in use and which of the tool's directories exist.
// It reads directory entries only and changes nothing.
func Look() Found {
	config, _ := ConfigHome()
	state, _ := StateHome()
	f := Found{Name: NameIn(config, state)}
	seen := map[string]bool{}
	for _, base := range []string{config, state} {
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		if p := filepath.Join(base, f.Name); exists(p) {
			f.Used = append(f.Used, p)
		}
		if f.Name == LegacyName {
			if p := filepath.Join(base, Name); exists(p) {
				f.Ignored = append(f.Ignored, p)
			}
		}
	}
	return f
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
