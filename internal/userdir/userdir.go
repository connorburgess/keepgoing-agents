// Package userdir says where the tool keeps the files that belong to a user
// and to no single loop: the loop registry, the standard route list and the
// default home of new loops under the configuration directory, and the
// session claims and the panel's runtime files under the state directory.
//
// Those files live in one directory under each. On a computer that has
// never run the tool it is named looptender. On a computer where the tool
// already keeps such files in a directory named keepgoing, its earlier name,
// under either place, that name is kept for both, and nothing is moved: a
// build from before the rename still finds its files there. What a person
// keeps in a directory of that name, such as a loop's own state, does not
// decide the name.
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

// Entries are the names of what the tool itself keeps in its directory: the
// loop registry, the standard route list and the default home of new loops
// under the configuration directory, and the session claims and the panel's
// runtime files under the state directory. On macOS the two are one place.
var Entries = []string{"registry", "routes.yaml", "loops", "sessions", "panel"}

// NameIn decides the directory's name from the places it could be in: the
// earlier name when one of the tool's own entries (Entries) is in a
// directory of that name under any of them, otherwise the current one.
//
// Only the tool's own entries count. A directory named keepgoing that holds
// something else, such as a loop's state_dir or configuration that a person
// put there, decides nothing: otherwise the first check of such a loop on a
// new install would change where every other command looks.
//
// An empty base is a place that could not be determined and is skipped. A
// place that cannot be inspected counts as holding the earlier name: the
// choice that leaves an existing setup where it is.
//
// The decision is made afresh by every command and never remembered. Two
// commands can still decide differently: around the moment an entry of the
// earlier name first appears (a build from before the rename ran), and when
// they are given different places (on Linux, a service started without the
// XDG_CONFIG_HOME its user's shell has). The session claims are kept safe
// through that by the claim itself, which looks under both names; see
// session.Take.
func NameIn(bases ...string) string {
	for _, base := range bases {
		if found, unknown := earlierEntry(base); found != "" || unknown {
			return LegacyName
		}
	}
	return Name
}

// earlierEntry returns one of the tool's own entries under the earlier name
// in base, or unknown when the place cannot be inspected.
func earlierEntry(base string) (found string, unknown bool) {
	if base == "" {
		return "", false
	}
	dir := filepath.Join(base, LegacyName)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", false
	case err != nil:
		return "", true
	case !info.IsDir() && info.Mode()&os.ModeSymlink == 0:
		return "", false // a file of that name is not the tool's directory
	}
	for _, name := range Entries {
		_, err := os.Lstat(filepath.Join(dir, name))
		switch {
		case err == nil:
			return filepath.Join(dir, name), false
		case !errors.Is(err, os.ErrNotExist):
			return "", true
		}
	}
	return "", false
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
	Used    []string // the tool's entries under that name that exist
	Unread  []string // the tool's entries under the other name, which are not read while Name is in use
	Unknown []string // places that could not be inspected, which is why the earlier name is in use
}

// Look reports the name in use and which of the tool's own entries exist
// under each name. It reads directory entries only and changes nothing.
func Look() Found {
	config, _ := ConfigHome()
	state, _ := StateHome()
	f := Found{Name: NameIn(config, state)}
	other := LegacyName
	if f.Name == LegacyName {
		other = Name
	}
	seen := map[string]bool{}
	for _, base := range []string{config, state} {
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		if _, unknown := earlierEntry(base); unknown {
			f.Unknown = append(f.Unknown, base)
		}
		for _, name := range Entries {
			if p := filepath.Join(base, f.Name, name); exists(p) {
				f.Used = append(f.Used, p)
			}
			if p := filepath.Join(base, other, name); exists(p) {
				f.Unread = append(f.Unread, p)
			}
		}
	}
	return f
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
